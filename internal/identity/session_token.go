package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/poetlife/aladdin/internal/rbac"
)

// DefaultSessionTTL 是会话的默认有效期。
//
// 取"天"量级：太短会把用户按在登录页上反复登入，太长则一次泄露的暴露窗口
// 被拉长——虽然撤销是立即生效的，但那要求有人先发现泄露。
// 具体取值是待定决策（见 docs/design/identity/session-token.md）。
const DefaultSessionTTL = 7 * 24 * time.Hour

// tokenEntropyBytes 是凭证的随机字节数。
//
// 32 字节（256 位）：猜中一份有效凭证的概率是 2^-256 量级，穷举不可行。
// 这个值由"猜中概率可以忽略"确定，不由"看起来够长"确定。
const tokenEntropyBytes = 32

// Sessions 是会话凭证的签发、校验、撤销入口，**是这条链路上唯一的实现**。
//
// 有效期与时钟由构造时注入，不在这里直接取 time.Now：
// 契约测试要能推进时间而不 sleep，生产要能有一个统一的时钟来源。
type Sessions struct {
	store SessionStore
	ttl   time.Duration
	now   func() time.Time
}

// NewSessions 用默认有效期构造会话入口。
func NewSessions(store SessionStore) *Sessions {
	return &Sessions{store: store, ttl: DefaultSessionTTL, now: time.Now}
}

// storeFailure 把存储返回的错误归一成"存储不可用"。
//
// 存储实现本来就该自己包好（见 gormstore 的 unavailable），这里再归一一次，
// 是为了让"库出问题了"这个结论不依赖每个实现都记得包：漏掉一个实现的后果，
// 是一次数据库故障被表现成"所有人的登录都失效了"，运维于是去查认证配置。
//
// ErrSessionNotFound **原样放行**：那是一个合格的否定结论（这份凭证不成立），
// 不是故障。两者一旦混掉，客户端会把一次权限/凭证问题放大成一次登录风暴。
func storeFailure(action string, err error) error {
	if err == nil || errors.Is(err, ErrSessionNotFound) {
		return err
	}
	return fmt.Errorf("%w: %s: %w", ErrStoreUnavailable, action, err)
}

// Issued 是一次签发的产物。
//
// Token 是**凭证原文**，只在这一刻存在：它由调用方交付给客户端之后，
// 服务端再也拿不回来，库里只有它的摘要。因此它不得进入日志、错误信息
// 与 --debug 输出（见 docs/design/identity/session-token.md）。
type Issued struct {
	Token   string
	Session Session
}

// Issue 为已确认的主体签发一份新凭证。
//
// 调用方必须先完成身份确认：本方法不做任何身份校验，它只记录"谁"与"到什么时候"。
func (s *Sessions) Issue(ctx context.Context, subject rbac.Subject) (Issued, error) {
	token, err := newToken()
	if err != nil {
		return Issued{}, err
	}
	now := s.now().UTC()
	session := Session{
		SubjectID:    subject.ID,
		SubjectType:  subject.Type,
		DefaultScope: subject.DefaultScope,
		IssuedAt:     now,
		ExpiresAt:    now.Add(s.ttl),
	}
	if err := s.store.Put(ctx, HashToken(token), session); err != nil {
		return Issued{}, storeFailure("签发会话", err)
	}
	return Issued{Token: token, Session: session}, nil
}

// Verify 校验一份凭证，返回它代表的主体。
//
// 找不到、已过期、库用不了是三种结果：前两者都返回 ErrSessionNotFound
// （对调用方是同一件事），后者返回 ErrStoreUnavailable——**它必须能被区分**，
// 否则一次数据库故障会被表现成"所有人都登录失效了"。
func (s *Sessions) Verify(ctx context.Context, token string) (rbac.Subject, error) {
	if token == "" {
		return rbac.Subject{}, ErrSessionNotFound
	}
	session, err := s.store.Get(ctx, HashToken(token))
	if err != nil {
		return rbac.Subject{}, storeFailure("读取会话", err)
	}
	if session.Expired(s.now()) {
		return rbac.Subject{}, ErrSessionNotFound
	}
	return session.Subject(), nil
}

// Revoke 撤销一份凭证，立即生效。
//
// 撤销是删除而不是标记：既然校验的唯一依据就是存储里的那一行，删掉它
// 就是最彻底的失效——不需要一个"已作废"状态位，也就不存在"忘了检查那个位"。
//
// 撤销不存在的凭证不是错误：重复登出、登出一个早已过期的会话，结果一致。
func (s *Sessions) Revoke(ctx context.Context, token string) error {
	return storeFailure("撤销会话", s.store.Delete(ctx, HashToken(token)))
}

// Refresh 用一份仍然有效的凭证换一份新的，**旧凭证同时失效**。
//
// 两步的顺序是有意的：先签发、再撤销。反过来的话，签发失败会把用户
// 直接登出（旧凭证已经没了，新凭证又没拿到），而先签发遇到问题时
// 客户端手里那份旧的仍然能用，重试即可。
//
// 撤销失败时返回错误、不把新凭证交出去：只发新不作旧，等于每次刷新都让
// 一份旧凭证继续有效到自然过期，凭证的暴露面随刷新次数单调增长。
func (s *Sessions) Refresh(ctx context.Context, token string) (Issued, error) {
	subject, err := s.Verify(ctx, token)
	if err != nil {
		return Issued{}, err
	}
	issued, err := s.Issue(ctx, subject)
	if err != nil {
		return Issued{}, err
	}
	if err := s.Revoke(ctx, token); err != nil {
		return Issued{}, err
	}
	return issued, nil
}

// Cleanup 回收已经过期的会话行，返回回收条数。
//
// 它只被启动路径调用一次（见 docs/design/identity/session-token.md）：
// 正确性不依赖它——已过期的凭证本来就校验不过——它存在的唯一目的是
// 不让会话表无限增长。因此调用方应当记录失败但**不因此拒绝启动**。
func (s *Sessions) Cleanup(ctx context.Context) (int64, error) {
	removed, err := s.store.DeleteExpired(ctx, s.now())
	return removed, storeFailure("回收会话", err)
}

// newToken 生成一份密码学随机的凭证。
//
// 编码用 URL 安全的无填充 base64：凭证要放进 Authorization 头，
// 标准 base64 的 '+' '/' '=' 会在各种中转环节被转义或截断。
func newToken() (string, error) {
	buf := make([]byte, tokenEntropyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成会话凭证失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashToken 算出一份凭证在会话存储中的查找键。
//
// **这是凭证与"库里的那一行"之间唯一的换算入口。**
//
// 用 SHA-256 而不是口令哈希那类刻意放慢的算法：凭证是 256 位密码学随机数，
// 不存在被猜中的风险，放慢只会让**每一个请求**都白付一次昂贵的代价——
// 而这个换算发生在每次认证上。这与口令的处理方式刻意不同，两者不可合并。
//
// 输出定长十六进制，正好对上会话表里那一列的长度。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
