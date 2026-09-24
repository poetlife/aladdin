package interceptor

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/poetlife/aladdin/internal/rbac"
)

// HeaderScope 是作用域在请求头中的名字。
//
// 小写是刻意的：gRPC 的 metadata 键必须全小写，而 http.Header.Get
// 会做大小写规范化，因此同一个常量对 Connect 与 gRPC 两条路径都成立。
const HeaderScope = "aladdin-scope"

// HeaderAuthorization 是凭证在请求头中的名字。理由同上。
const HeaderAuthorization = "authorization"

// 认证失败的两类原因。它们都映射到 Unauthenticated，
// 但在日志中必须可区分：一类是调用方没带凭证，另一类是带了但无效。
var (
	// ErrNoCredential 表示请求未携带任何凭证。
	ErrNoCredential = errors.New("请求未携带凭证")
	// ErrInvalidCredential 表示凭证存在但无法解析或已失效。
	ErrInvalidCredential = errors.New("凭证无效或已失效")
)

// Authenticator 从请求头中确认主体。
//
// 实现只负责认证（你是谁）：解析凭证、校验签名与有效期。
// 它**不得**做任何权限判断——那是 Engine 的职责。
//
// 它由 HTTP 中间件调用，而不是由 Connect 拦截器调用：
// Connect 的拦截器在请求体读取并解压之后才执行，把认证放在那里意味着
// 未认证的请求也会先被完整读一遍（见 docs/design/rbac/server-permissions.md）。
type Authenticator interface {
	Authenticate(ctx context.Context, header http.Header) (rbac.Subject, error)
}

// TokenAuthenticator 是按不透明 token 查表认证的实现。
//
// 这是骨架自带的开发用实现：它把 token 到主体的映射放在内存里。
// 生产环境应替换为签名凭证或外部身份服务，替换点只有这一个类型。
type TokenAuthenticator struct {
	// Tokens 是 token 到主体的映射。
	Tokens map[string]rbac.Subject
}

// NewTokenAuthenticator 构造一个空的 token 认证器。
func NewTokenAuthenticator() *TokenAuthenticator {
	return &TokenAuthenticator{Tokens: map[string]rbac.Subject{}}
}

// Add 登记一个 token 及其对应主体。
func (a *TokenAuthenticator) Add(token string, subject rbac.Subject) {
	a.Tokens[token] = subject
}

// Authenticate 实现 Authenticator。
func (a *TokenAuthenticator) Authenticate(_ context.Context, header http.Header) (rbac.Subject, error) {
	token, err := BearerToken(header)
	if err != nil {
		return rbac.Subject{}, err
	}
	subject, ok := a.Tokens[token]
	if !ok {
		return rbac.Subject{}, ErrInvalidCredential
	}
	return subject, nil
}

// BearerToken 从请求头中取出 Bearer token。
func BearerToken(header http.Header) (string, error) {
	raw := strings.TrimSpace(header.Get(HeaderAuthorization))
	if raw == "" {
		return "", ErrNoCredential
	}
	// 允许直接传 token，也允许标准的 "Bearer <token>"。
	if after, ok := strings.CutPrefix(raw, "Bearer "); ok {
		raw = strings.TrimSpace(after)
	}
	if raw == "" {
		return "", ErrInvalidCredential
	}
	return raw, nil
}

// ScopeFromHeader 从请求头读取调用方声明的作用域。
func ScopeFromHeader(header http.Header) (rbac.Scope, bool) {
	values := header.Values(HeaderScope)
	if len(values) == 0 || values[0] == "" {
		return "", false
	}
	return rbac.Scope(values[0]), true
}

// subjectCtxKey 是主体在 context 中的键。
type subjectCtxKey struct{}

var subjectKey = subjectCtxKey{}

// WithSubject 把已确认的主体放入 context。
//
// 由 HTTP 中间件调用一次；鉴权拦截器只读取，不写入。
func WithSubject(ctx context.Context, subject rbac.Subject) context.Context {
	return context.WithValue(ctx, subjectKey, subject)
}

// SubjectFromContext 取回当前请求的主体。
func SubjectFromContext(ctx context.Context) (rbac.Subject, bool) {
	s, ok := ctx.Value(subjectKey).(rbac.Subject)
	return s, ok
}
