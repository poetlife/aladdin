// Package cosstore 是 profile.AvatarStore 的腾讯云 COS 实现。
//
// 它只做三件事：把一段字节写成一个对象、删掉一个对象、为一个对象签发一个
// 短时读取地址。类型判定、大小上限、对象键规则都在 internal/profile ——
// 那些是**领域规则**，不是某个存储的特性；放在这里，就只有在用 COS 时才成立。
//
// 桶必须是**私有读写**：本实现依赖"没有签名取不到东西"这一前提，而签发地址
// 这个动作的意义也正在于此（见 docs/design/profile/avatar-storage.md）。
package cosstore

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/tencentyun/cos-go-sdk-v5"

	"github.com/poetlife/aladdin/internal/profile"
)

// httpTimeout 是写入与删除的超时。
//
// 有超时是必须的：默认的 http.Client 会一直等下去，而一个卡住的头像上传会
// 一直占着一次请求的 goroutine 与一个连接。签发地址不发起请求，不受它影响。
const httpTimeout = 15 * time.Second

// Config 是构造 COS 头像存储所需的取值。
type Config struct {
	// BucketURL 是完整的桶主机名，形如
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

// Store 是 profile.AvatarStore 的 COS 实现。
type Store struct {
	client *cos.Client
}

// New 构造 COS 头像存储。
func New(cfg Config) (*Store, error) {
	bucket, err := url.Parse(cfg.BucketURL)
	if err != nil {
		return nil, fmt.Errorf("解析桶地址失败: %w", err)
	}
	// 只接受 https。用明文把密钥与图片送出去，与"桶私有"这个前提直接冲突——
	// 配置校验也会拒绝它，这里再挡一次是因为本构造函数是唯一入口，绕过它
	// 就等于绕过这条约束。
	if bucket.Scheme != "https" || bucket.Host == "" {
		return nil, fmt.Errorf("桶地址必须是带主机名的 https 地址，当前是 %q", cfg.BucketURL)
	}

	client := cos.NewClient(
		&cos.BaseURL{BucketURL: bucket},
		&http.Client{
			Timeout: httpTimeout,
			Transport: &cos.AuthorizationTransport{
				SecretID:  cfg.SecretID,
				SecretKey: cfg.SecretKey,
			},
		},
	)
	return &Store{client: client}, nil
}

// Put 实现 profile.AvatarStore。
//
// 一个主体一个对象键，因此这里是**覆盖写**：替换头像不产生孤儿对象。
//
// 内容类型随对象一起写入，而不是靠取图地址上的参数去补：那种写法把类型绑在
// 地址上，换一个不带参数的地址就又变回 octet-stream，而"这个对象是什么类型"
// 应当是对象自己的属性。
func (s *Store) Put(ctx context.Context, subjectID, contentType string, data []byte) error {
	_, err := s.client.Object.Put(
		ctx,
		profile.AvatarKey(subjectID),
		bytes.NewReader(data),
		&cos.ObjectPutOptions{
			ObjectPutHeaderOptions: &cos.ObjectPutHeaderOptions{ContentType: contentType},
		},
	)
	if err != nil {
		return fmt.Errorf("写入头像对象失败: %w", err)
	}
	return nil
}

// Delete 实现 profile.AvatarStore。对象不存在时也成功（幂等）。
func (s *Store) Delete(ctx context.Context, subjectID string) error {
	_, err := s.client.Object.Delete(ctx, profile.AvatarKey(subjectID))
	if err != nil {
		return fmt.Errorf("删除头像对象失败: %w", err)
	}
	return nil
}

// PresignGet 实现 profile.AvatarStore。
//
// **它不发起任何请求**：预签名是对"地址 + 密钥 + 有效期"做一次签名，对象存不
// 存在要到真正取的时候才知道。因此它也不需要网络，这在测试里是可见的——
// 地址的正确性可以离线断言。
func (s *Store) PresignGet(ctx context.Context, subjectID string, ttl time.Duration) (string, error) {
	signed, err := s.client.Object.GetPresignedURL2(
		ctx,
		http.MethodGet,
		profile.AvatarKey(subjectID),
		ttl,
		nil,
	)
	if err != nil {
		return "", fmt.Errorf("签发头像读取地址失败: %w", err)
	}
	return signed.String(), nil
}

var _ profile.AvatarStore = (*Store)(nil)
