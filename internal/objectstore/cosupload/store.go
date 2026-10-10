// Package cosupload 是 objectstore.Store 的腾讯云 COS 实现。
//
// 它做四件事：用长期密钥向临时凭证服务换取一份被策略限定的写入凭证、读取对象
// 的元数据、读出对象字节、删除对象，以及签发短时读取地址。**策略怎么构造**在
// policy.go —— 那是这套机制的安全核心，与"用哪个 SDK 调哪个接口"分开。
//
// 桶是**默认私有读写**：本实现依赖"没有签名取不到东西"这一前提，而签发读取
// 地址这个动作的意义也正在于此。**唯一的例外是公开区的对象**——它们由
// PublicWriter 在写入时逐个设成公开读，桶本身没有公开读策略（见
// docs/design/galaxy/asset-library.md）。
package cosupload

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/tencentyun/cos-go-sdk-v5"

	"github.com/poetlife/aladdin/internal/objectstore"
)

// httpTimeout 是单次读取、删除与元数据读取的超时。
//
// 有超时是必须的：默认的 http.Client 会一直等下去，而一个卡住的读取会一直占着
// 一个连接。**换临时凭证不受它影响**（那是另一次请求），签发读取地址也不发起
// 请求。
//
// 它取得比较宽：上架到公开区那一步会把一份完整对象读进内存，而资产上限是
// 100 MiB，慢链路上需要时间。
const httpTimeout = 5 * time.Minute

// credentialTTL 是换取到的临时凭证的有效期。
//
// 它取的是换取接口的固定时长（半小时）。要更短只能自行调用凭证服务——客户端
// 库把那个时长写死在换取调用里（见 docs/design/objectstore/README.md 的待定
// 决策）。这里给出的是**给客户端的提示值**，实际失效时刻以对象存储的判断为准。
const credentialTTL = 30 * time.Minute

// deleteMultiLimit 是批量删除单次请求的键数上限（接口的规定值：最多 1000 个）。
//
// 超过就切块：一次工程删除可能上千个对象，而"切块"这件事是**这个实现**的细节，
// 调用方按一批交下来即可。
const deleteMultiLimit = 1000

// readManyConcurrency 是批量读的并发度。
//
// 它有上界：并发是为了把一条跨境链路上的等待时间填满（串行读时每次往返链路都
// 是闲的——见 docs/design 里那条延迟实测），但无上限会同时占满本机的连接与桶侧
// 的并发配额，而收益到某个点就饱和了。
const readManyConcurrency = 16

// Config 是构造 COS 直传存储所需的取值。
type Config struct {
	// BucketURL 是私有桶地址，形如
	// https://<桶名>-<APPID>.cos.<地域>.myqcloud.com。
	//
	// 它是配置文件的键：桶地址不是秘密，没有签名取不到东西。
	BucketURL string
	// SecretID 与 SecretKey 是子账号密钥。
	//
	// **它们只从环境变量来，不是配置文件的键**：配置文件会进版本库、进镜像、
	// 被贴给别人排查问题（见 docs/design/config/credentials.md）。
	SecretID  string
	SecretKey string
}

// Store 是 objectstore.Store 的 COS 实现。
type Store struct {
	client    *cos.Client
	parts     bucketParts
	secretID  string
	secretKey string
}

// newClient 按桶地址与密钥构造一个 COS 客户端。
//
// 它是本包唯一的客户端构造入口：**怎么用对象存储只在一个包里**，而不是"上传
// 一处、上架一处"各写一遍 SDK 的用法。
//
// 只接受 https：用明文把密钥与用户素材送出去，与"桶私有"这个前提直接冲突——
// 配置校验也会拒绝它，这里再挡一次是因为本构造函数是唯一入口。
func newClient(bucketURL, secretID, secretKey string) (*cos.Client, error) {
	bucket, err := url.Parse(bucketURL)
	if err != nil {
		return nil, fmt.Errorf("解析桶地址失败: %w", err)
	}
	if bucket.Scheme != "https" || bucket.Host == "" {
		return nil, fmt.Errorf("桶地址必须是带主机名的 https 地址，当前是 %q", bucketURL)
	}
	return cos.NewClient(
		&cos.BaseURL{BucketURL: bucket},
		&http.Client{
			Timeout: httpTimeout,
			Transport: &cos.AuthorizationTransport{
				SecretID:  secretID,
				SecretKey: secretKey,
			},
		},
	), nil
}

// New 构造 COS 直传存储。
func New(cfg Config) (*Store, error) {
	parts, err := parseBucketURL(cfg.BucketURL)
	if err != nil {
		return nil, err
	}
	client, err := newClient(cfg.BucketURL, cfg.SecretID, cfg.SecretKey)
	if err != nil {
		return nil, err
	}
	return &Store{
		client:    client,
		parts:     parts,
		secretID:  cfg.SecretID,
		secretKey: cfg.SecretKey,
	}, nil
}

// IssueUpload 实现 objectstore.Store。
//
// 换取临时凭证走的是对象存储 SDK 里的那条路：它把策略原样交给凭证服务，而
// **策略才是约束的载体**——凭证本身只是一对临时密钥加一个令牌。
//
// 换取失败不重试：重试属于调用方的策略（用户再点一次上传），而在这里悄悄重试
// 会把一次凭证服务的故障表现成一次"上传按钮没反应"。
func (s *Store) IssueUpload(ctx context.Context, key string, rules []objectstore.TypeRule, forbidOverwrite bool) (objectstore.Credential, error) {
	policy, err := buildPolicy(s.parts, key, rules)
	if err != nil {
		return objectstore.Credential{}, err
	}
	transport := &cos.StsCredentialTransport{
		SecretID:  s.secretID,
		SecretKey: s.secretKey,
		Policy:    policy,
		Region:    s.parts.Region,
	}
	secretID, secretKey, token, err := transport.GetCredential()
	if err != nil {
		return objectstore.Credential{}, fmt.Errorf("%w: 换取直传凭证失败: %w", objectstore.ErrStoreUnavailable, err)
	}
	if secretID == "" || secretKey == "" || token == "" {
		return objectstore.Credential{}, fmt.Errorf("%w: 换到的直传凭证不完整", objectstore.ErrStoreUnavailable)
	}
	return objectstore.Credential{
		Bucket:          s.parts.Bucket,
		Region:          s.parts.Region,
		Key:             key,
		SecretID:        secretID,
		SecretKey:       secretKey,
		SessionToken:    token,
		ExpiresAt:       time.Now().Add(credentialTTL),
		ForbidOverwrite: forbidOverwrite,
	}, nil
}

// Head 实现 objectstore.Store。
func (s *Store) Head(ctx context.Context, key string) (objectstore.ObjectStat, error) {
	resp, err := s.client.Object.Head(ctx, key, nil)
	if err != nil {
		if cos.IsNotFoundError(err) {
			return objectstore.ObjectStat{}, objectstore.ErrObjectNotFound
		}
		return objectstore.ObjectStat{}, fmt.Errorf("%w: 读取对象元数据失败: %w", objectstore.ErrStoreUnavailable, err)
	}
	if resp.ContentLength < 0 {
		return objectstore.ObjectStat{}, fmt.Errorf("%w: 对象没有给出字节数", objectstore.ErrStoreUnavailable)
	}
	return objectstore.ObjectStat{SizeBytes: resp.ContentLength}, nil
}

// Read 实现 objectstore.Store。
//
// 它的唯一用途是把私有区对象上架到公开区（见 objectstore.Store 的说明）。
func (s *Store) Read(ctx context.Context, key string) ([]byte, error) {
	resp, err := s.client.Object.Get(ctx, key, nil)
	if err != nil {
		if cos.IsNotFoundError(err) {
			return nil, objectstore.ErrObjectNotFound
		}
		return nil, fmt.Errorf("%w: 读取对象失败: %w", objectstore.ErrStoreUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: 读取对象失败: %w", objectstore.ErrStoreUnavailable, err)
	}
	return data, nil
}

// Delete 实现 objectstore.Store。对象不存在时也成功（幂等）。
func (s *Store) Delete(ctx context.Context, key string) error {
	if _, err := s.client.Object.Delete(ctx, key); err != nil {
		if cos.IsNotFoundError(err) {
			return nil
		}
		return fmt.Errorf("%w: 删除对象失败: %w", objectstore.ErrStoreUnavailable, err)
	}
	return nil
}

// DeleteMany 实现 objectstore.Store。
//
// 一次请求最多 1000 个键（deleteMultiLimit），超出由本实现切块。**批量删除是
// 逐键给结果的**："整批成功"不是对象存储能给的结论，因此失败的键被点名返回——
// 调用方是尽力而为的清理，它只需要知道哪些没删掉。
func (s *Store) DeleteMany(ctx context.Context, keys []string) error {
	var failed []string
	for start := 0; start < len(keys); start += deleteMultiLimit {
		end := start + deleteMultiLimit
		if end > len(keys) {
			end = len(keys)
		}
		objects := make([]cos.Object, 0, end-start)
		for _, key := range keys[start:end] {
			objects = append(objects, cos.Object{Key: key})
		}
		res, _, err := s.client.Object.DeleteMulti(ctx, &cos.ObjectDeleteMultiOptions{Objects: objects})
		if err != nil {
			return fmt.Errorf("%w: 批量删除对象失败: %w", objectstore.ErrStoreUnavailable, err)
		}
		if res == nil {
			continue
		}
		for _, item := range res.Errors {
			failed = append(failed, fmt.Sprintf("%s(%s)", item.Key, item.Code))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("批量删除有 %d 个对象失败: %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}

// ReadMany 实现 objectstore.Store：并发 GET，逐个键归类结果。
//
// "键上没有对象"不进 error，而由返回的 map 里有没有这个键表达（见
// objectstore.Store.ReadMany）。真故障才返回 error，此时返回的 map 不作数。
func (s *Store) ReadMany(ctx context.Context, keys []string) (map[string][]byte, error) {
	bytesByKey := make(map[string][]byte, len(keys))
	var mu sync.Mutex
	var firstErr error

	sem := make(chan struct{}, readManyConcurrency)
	var wg sync.WaitGroup
	for _, key := range keys {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			data, err := s.Read(ctx, key)

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				bytesByKey[key] = data
			case errors.Is(err, objectstore.ErrObjectNotFound):
				// 缺失由"map 里没有这个键"表达，这里什么都不做。
			case firstErr == nil:
				firstErr = err
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return bytesByKey, nil
}

// SHA256 实现 objectstore.Store：交给数据万象算，**字节不过境**。
//
// 直传之后服务端看不到字节，而"核对上传方声明的摘要"要看见字节——把字节拉回来
// 算一遍正是直传想省掉的那一趟。数据万象的同步 filehash 接口在广州侧把这件事做
// 完，只回一个摘要，延迟与对象大小基本无关（实测 52 字节 676ms、8 MiB 658ms）。
//
// 这条能力**要单独开通**，因此"给不出"是一种常态而不是故障：没开通数据万象、
// 对象超过同步接口 128MB 的上限、服务端临时故障——一律报 ErrHashUnavailable，
// 由调用方回退到读回字节自己算。这里刻意**不按错误码枚举**：枚举清单写完就开始
// 过期，而调用方并不需要按码分支，它只需要知道"这条路走不通，换一条"。
//
// 唯一的例外是上下文已经结束：那时回退那条路同样走不动，把它报成"能力不可用"
// 会把一次取消表现成一次降级。
func (s *Store) SHA256(ctx context.Context, key string) (string, error) {
	res, _, err := s.client.CI.GetFileHash(ctx, key, &cos.GetFileHashOptions{
		CIProcess: "filehash",
		Type:      "sha256",
	})
	if err != nil {
		if cos.IsNotFoundError(err) {
			return "", objectstore.ErrObjectNotFound
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("%w: 计算内容摘要失败: %w", objectstore.ErrStoreUnavailable, err)
		}
		return "", fmt.Errorf("%w: %w", objectstore.ErrHashUnavailable, err)
	}
	if res == nil || res.FileHashCodeResult == nil || res.FileHashCodeResult.SHA256 == "" {
		return "", fmt.Errorf("%w: 存储侧没有给出摘要", objectstore.ErrHashUnavailable)
	}
	return strings.ToLower(res.FileHashCodeResult.SHA256), nil
}

// Put 实现 objectstore.Store：把一个对象写进私有区。
//
// 它**不设置任何 ACL**，因此写下去的对象仍是私有的——桶默认私有读写，而"哪些
// 对象是公开的"只由上架路径决定（见 PublicWriter.Put）。
func (s *Store) Put(ctx context.Context, key, contentType string, data []byte) error {
	_, err := s.client.Object.Put(ctx, key, bytes.NewReader(data), &cos.ObjectPutOptions{
		ObjectPutHeaderOptions: &cos.ObjectPutHeaderOptions{ContentType: contentType},
	})
	if err != nil {
		return fmt.Errorf("%w: 写入对象失败: %w", objectstore.ErrStoreUnavailable, err)
	}
	return nil
}

// PresignGet 实现 objectstore.Store。
//
// **它不发起任何请求**：预签名是对"地址 + 密钥 + 有效期"做一次签名，对象存不
// 存在要到真正取的时候才知道。因此它也不需要网络，这在测试里是可见的——地址
// 的正确性可以离线断言。
func (s *Store) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	signed, err := s.client.Object.GetPresignedURL2(ctx, http.MethodGet, key, ttl, nil)
	if err != nil {
		return "", fmt.Errorf("签发读取地址失败: %w", err)
	}
	return signed.String(), nil
}

// PresignDownload 实现 objectstore.Store：签发一条**强制下载**的短时地址。
//
// 固定的两个响应头通过**覆盖响应头**那两个查询参数交给对象存储（COS 的
// `response-content-type` / `response-content-disposition`），它们在请求被真正
// 取用时才生效，因此对象自己的元数据不参与——不管上传时声明过什么、也不管文件
// 名以什么结尾，浏览器都只会把它存下来。
//
// 它们**参与签名**：预签名是对整条请求（含查询串）算出来的，因此地址一旦签发，
// 那两个参数就改不动——改一个字节都会让签名对不上。这正是"响应头由签发策略
// 固定"这句话的落点，而不是一句约定。
func (s *Store) PresignDownload(ctx context.Context, key, filename string, ttl time.Duration) (string, error) {
	signed, err := s.client.Object.GetPresignedURL2(ctx, http.MethodGet, key, ttl, &cos.ObjectGetOptions{
		ResponseContentType:        objectstore.NeutralContentType,
		ResponseContentDisposition: objectstore.DownloadDisposition(filename),
	})
	if err != nil {
		return "", fmt.Errorf("签发下载地址失败: %w", err)
	}
	return signed.String(), nil
}

// PublicWriter 是**公开区**的写入口：把发布物引用的字节写进桶，并把那些对象设成
// 公开读。
//
// 它与 Store 分开**不是**因为它们写两个桶——两者写的是**同一个桶**（见
// docs/design/galaxy/asset-library.md）。分开是因为**写下去的东西会不会被所有人
// 读到**不同：Store 写的是私有对象，PublicWriter 写的是公开对象，好让这件事在
// 调用处一眼可见。
type PublicWriter struct {
	client *cos.Client
	// parts 用来拼复制源：公开区与私有区在同一个桶，源就是"桶名 + 私有区键"。
	parts bucketParts
}

// NewPublicWriter 构造公开区的写入口。它**复用私有区的桶地址与同一对密钥**：
// 公开与私有由对象权限决定，不由桶或凭证决定。
func NewPublicWriter(bucketURL, secretID, secretKey string) (*PublicWriter, error) {
	parts, err := parseBucketURL(bucketURL)
	if err != nil {
		return nil, err
	}
	client, err := newClient(bucketURL, secretID, secretKey)
	if err != nil {
		return nil, err
	}
	return &PublicWriter{client: client, parts: parts}, nil
}

// Exists 判定公开区里是否已有该键的对象。
func (w *PublicWriter) Exists(ctx context.Context, key string) (bool, error) {
	if _, err := w.client.Object.Head(ctx, key, nil); err != nil {
		if cos.IsNotFoundError(err) {
			return false, nil
		}
		return false, fmt.Errorf("查询公开区对象失败: %w", err)
	}
	return true, nil
}

// Copy 把一个**私有区**的对象复制进公开区。
//
// 它取代了早先的"把字节读回服务端再写上去"：公开区与私有区在**同一个桶**里，
// 因此这件事可以由对象存储自己在一次请求里做完——**字节不过境**。资产上架是
// 发布流程里最大的一块开销，而这正是它的来源（见 issue #33）。
//
// 随复制一起改的两个属性：
//
//   - **公开读在这里设置，而且只在这里设置。** 桶保持默认私有读写、没有桶级的
//     公开读策略，因此"哪些对象是公开的"完全由本方法的调用路径决定。由此得到
//     一条对本方法前提的约束：**复制过去的内容必须是发布校验已经放过的内容**
//     （见 docs/design/galaxy/publication.md）。
//   - 内容类型被**替换**成公开区该有的那个。私有区对象写下去时用的是中性的
//     application/octet-stream（见 objectstore.NeutralContentType），而公开区
//     对象由访问者的浏览器直连取用，类型应当是对象自己的属性，不该靠地址上的
//     参数去补。`Replaced` 是必需的：不带它，对象存储会原样沿用源的元数据。
//
// 复制源写的是**同一个桶里的键**：跨桶复制要先让源可公开读或给目标账号授权，
// 这里不需要，而这也正是"公开区与私有区共用一个桶"换来的便利。
func (w *PublicWriter) Copy(ctx context.Context, srcKey, dstKey, contentType string) error {
	_, _, err := w.client.Object.Copy(ctx, dstKey, w.parts.Bucket+"/"+srcKey, &cos.ObjectCopyOptions{
		ACLHeaderOptions: &cos.ACLHeaderOptions{XCosACL: cos.ACL.PublicRead},
		ObjectCopyHeaderOptions: &cos.ObjectCopyHeaderOptions{
			ContentType:           contentType,
			XCosMetadataDirective: "Replaced",
		},
	})
	if err != nil {
		return fmt.Errorf("复制到公开区失败: %w", err)
	}
	return nil
}

var _ objectstore.Store = (*Store)(nil)
