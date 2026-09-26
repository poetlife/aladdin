package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/poetlife/aladdin/internal/identity"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/internal/server/interceptor"
)

// credentialAuthenticator 是请求凭证的认证入口，覆盖系统里的两类凭证。
//
// 两类凭证的结论对调用方是同一件事（"已确认的主体"或"未认证"），
// 区别只在"能否撤销"与"能否过期"上（见 docs/design/identity/session-token.md）。
// 因此它们的次序与错误类别由这一处决定，而不是散在两个实现里：
//
//  1. **先形态判断**：机器凭证是运维配置的**有界集合**，一次内存查找就能
//     确定；不命中才轮得到会话路径去查存储。反过来先查库的话，每一个
//     机器凭证请求都会白付一次数据库往返。
//  2. **两类失败必须可分**："凭证不成立"与"存储不可用"是不同结论。
//     后者若被压成前者，一次数据库抖动就会变成一次全站重新登录
//     （见 interceptor.RejectAuthFailure）。
type credentialAuthenticator struct {
	// machine 是机器凭证与开发凭证。它可以是空的——一个只让人登录的
	// 部署不需要任何机器凭证。
	machine *interceptor.TokenAuthenticator
	// sessions 是服务端签发的会话凭证。为 nil 时表示这个部署没有装配
	// 会话路径，此时只有机器凭证可用。
	sessions *identity.Sessions
}

// Authenticate 实现 interceptor.Authenticator。
func (a *credentialAuthenticator) Authenticate(ctx context.Context, header http.Header) (rbac.Subject, error) {
	token, err := interceptor.BearerToken(header)
	if err != nil {
		return rbac.Subject{}, err
	}

	if subject, ok := a.machine.Lookup(token); ok {
		return subject, nil
	}
	if a.sessions == nil {
		return rbac.Subject{}, interceptor.ErrInvalidCredential
	}

	subject, err := a.sessions.Verify(ctx, token)
	switch {
	case err == nil:
		return subject, nil
	case errors.Is(err, identity.ErrSessionNotFound):
		// 不存在、已撤销、已过期三者对调用方是同一件事。区分它们等于
		// 告诉探测者"这个字符串曾经有效"。
		return rbac.Subject{}, interceptor.ErrInvalidCredential
	case errors.Is(err, identity.ErrStoreUnavailable):
		return rbac.Subject{}, fmt.Errorf("%w: %w", interceptor.ErrStoreUnavailable, err)
	default:
		// 认不出的错误不改写：把它冒充成"凭证无效"会把一次设施故障
		// 表现成一次登录失败。
		return rbac.Subject{}, err
	}
}

var _ interceptor.Authenticator = (*credentialAuthenticator)(nil)
