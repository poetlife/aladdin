package server

import (
	"context"
	"time"

	"connectrpc.com/connect"

	opsv1 "github.com/poetlife/aladdin/api/gen/aladdin/ops/v1"
	"github.com/poetlife/aladdin/internal/buildinfo"
)

// OpsService 是**部署实例自述**的 RPC 实现。
//
// 它没有存储、没有业务判断，只是一层把 internal/buildinfo 读出来的薄壳。这样做是
// 有意的：版本信息的唯一来源是那个包（构建期往它注入），这里再算一遍就等于有了
// 第二个版本号，而两个版本号不一致时无从判断该信哪个（见
// docs/design/deployment/README.md）。
//
// 权限与作用域由方法注解声明（见 ops.proto），拦截器统一执行，这里不重复判定：
// 多一道判定就是多一处会与注解漂移的地方。
type OpsService struct {
	// read 取这一次读取的构建与运行信息。
	//
	// 它是可注入的函数而不是直接调 buildinfo.Read：测试要断言的是"读到的信息
	// 怎么落成协议字段"，而不是"本机碰巧是哪次构建"——后者取决于谁跑测试、
	// 有没有带 -ldflags，断言不了。
	read func() buildinfo.Info
}

// NewOpsService 构造部署信息服务。
func NewOpsService() *OpsService {
	return &OpsService{read: buildinfo.Read}
}

// GetDeploymentInfo 实现 OpsService。
//
// 请求里的 scope 只用于鉴权（见 ops.proto），不过滤结果，因此这里不读它。
func (s *OpsService) GetDeploymentInfo(_ context.Context, _ *connect.Request[opsv1.GetDeploymentInfoRequest]) (*connect.Response[opsv1.GetDeploymentInfoResponse], error) {
	info := s.read()
	return connect.NewResponse(&opsv1.GetDeploymentInfoResponse{
		Version:   info.Version,
		Commit:    info.Commit,
		BuiltAt:   formatMoment(info.BuildTime),
		Released:  info.Released,
		GoVersion: info.GoVersion,
		Os:        info.OS,
		Arch:      info.Arch,
		Instance:  info.Instance,
		StartedAt: formatMoment(info.StartedAt),
	}), nil
}

// formatMoment 把时刻落成 RFC3339（UTC），零值落成空串。
//
// 空串而不是"零值时间的时间戳"（0001-01-01T00:00:00Z）：后者是一个**看起来像
// 真的**取值，界面只能靠模式匹配去猜它其实表示"没有"——而那正是"构建时间显示成
// 公元 1 年"这类页面的来路。空表示没有，且只有这一个含义。
//
// UTC 与仓库既有的时间字段一致（见 galaxy 各接口）。
func formatMoment(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
