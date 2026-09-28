// Package cosupload 是 objectstore.Store 的腾讯云 COS 实现。
//
// 它做四件事：用长期密钥向临时凭证服务换取一份被策略限定的写入凭证、读取对象
// 的元数据、读出对象字节、删除对象，以及签发短时读取地址。**策略怎么构造**在
// policy.go —— 那是这套机制的安全核心，与"用哪个 SDK 调哪个接口"分开。
//
// 桶必须是**私有读写**：本实现依赖"没有签名取不到东西"这一前提，而签发读取
// 地址这个动作的意义也正在于此。
package cosupload

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
func (s *Store) IssueUpload(ctx context.Context, key string, rules []objectstore.TypeRule) (objectstore.Credential, error) {
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
		Bucket:       s.parts.Bucket,
		Region:       s.parts.Region,
		Key:          key,
		SecretID:     secretID,
		SecretKey:    secretKey,
		SessionToken: token,
		ExpiresAt:    time.Now().Add(credentialTTL),
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

// PublicWriter 是**公开区**的写入口：按内容摘要上架与查询已存在的对象。
//
// 它与 Store 分开是因为它们写的是两个桶、承担两套语义：Store 管"用户的字节怎么
// 进来"，PublicWriter 管"发布物引用的字节怎么被公开"。公开桶是公开读的，而私有
// 桶必须私有——分成两个类型，好让"这一次写的是哪个桶"在调用处一眼可见。
type PublicWriter struct {
	client *cos.Client
}

// NewPublicWriter 构造公开区的写入口。
func NewPublicWriter(publicBucketURL, secretID, secretKey string) (*PublicWriter, error) {
	client, err := newClient(publicBucketURL, secretID, secretKey)
	if err != nil {
		return nil, err
	}
	return &PublicWriter{client: client}, nil
}

// Exists 判定公开区里是否已有该摘要的对象。
func (w *PublicWriter) Exists(ctx context.Context, digest string) (bool, error) {
	if _, err := w.client.Object.Head(ctx, digest, nil); err != nil {
		if cos.IsNotFoundError(err) {
			return false, nil
		}
		return false, fmt.Errorf("查询公开区对象失败: %w", err)
	}
	return true, nil
}

// Put 把一个对象按给定的摘要键写进公开区。
//
// 内容类型随对象一起写入：公开区对象由访问者的浏览器直连取用，而"这个对象是
// 什么类型"应当是对象自己的属性，不该靠地址上的参数去补。
func (w *PublicWriter) Put(ctx context.Context, digest, contentType string, data []byte) error {
	_, err := w.client.Object.Put(ctx, digest, bytes.NewReader(data), &cos.ObjectPutOptions{
		ObjectPutHeaderOptions: &cos.ObjectPutHeaderOptions{ContentType: contentType},
	})
	if err != nil {
		return fmt.Errorf("写入公开区对象失败: %w", err)
	}
	return nil
}

var _ objectstore.Store = (*Store)(nil)
