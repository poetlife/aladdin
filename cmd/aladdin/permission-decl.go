package main

import (
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	"github.com/poetlife/aladdin/internal/rbac"
)

// annotationPermission 是命令所需权限码的注解键。
const annotationPermission = "aladdin.permission"

// annotationDangerous 标记该命令属于危险操作，需要二次确认。
const annotationDangerous = "aladdin.dangerous"

// annotationAuthenticatedOnly 标记该命令只需认证、不需特定权限。
//
// 与 proto 的 authenticated_only 注解同构：命令的鉴权分类必须三选一，
// 没有"漏声明即放行"这一档。
const annotationAuthenticatedOnly = "aladdin.authenticated_only"

// requirePermission 为一个命令声明它所需的权限码。
//
// 声明是**静态**的：它不用于运行时的本地放行判断——CLI 不允许缓存并复用
// 权限判定结果来决定是否发起调用（见 docs/design/rbac/cli-permissions.md）。
// 它的用途有两个：
//  1. 在 --help 中展示该命令需要什么权限；
//  2. 供 checkCommands 做构建期静态检查，防止新增命令漏声明。
func requirePermission(cmd *cobra.Command, permission rbac.PermissionCode) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[annotationPermission] = permission.String()
	return cmd
}

// markAuthenticatedOnly 声明该命令只需认证。
//
// 适用于查询自身信息的命令（如 whoami、permissions）：
// 它们对任何已登录主体都可用，因此没有对应的权限码。
func markAuthenticatedOnly(cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[annotationAuthenticatedOnly] = "true"
	return cmd
}

// authenticatedOnly 报告命令是否被声明为只需认证。
func authenticatedOnly(cmd *cobra.Command) bool {
	return cmd.Annotations[annotationAuthenticatedOnly] == "true"
}

// markDangerous 标记命令属于危险操作。
func markDangerous(cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[annotationDangerous] = "true"
	return cmd
}

// requiredPermission 返回命令声明的权限码。未声明时返回空串。
func requiredPermission(cmd *cobra.Command) rbac.PermissionCode {
	if cmd.Annotations == nil {
		return ""
	}
	return rbac.PermissionCode(cmd.Annotations[annotationPermission])
}

// isDangerous 报告命令是否为危险操作。
func isDangerous(cmd *cobra.Command) bool {
	return cmd.Annotations[annotationDangerous] == "true"
}

// publicCommands 是免鉴权命令的白名单，按命令全路径精确匹配。
//
// 与 gRPC 侧同源同理：白名单必须显式维护且尽可能短。
// 新增成员需要在评审中说明理由。
var publicCommands = map[string]bool{
	"aladdin version": true,
	// 升级只作用于调用者自己的那个文件，不碰服务端，也不需要凭证。
	"aladdin update": true,
	"aladdin login":  true,
}

// publicCommandPrefixes 是免鉴权命令的**子树**前缀。
//
// 用于 cobra 自动生成的命令族：`completion` 会派生出
// `completion bash`、`completion zsh` 等，逐个列举既啰嗦又容易漏。
// 这些命令只输出本地 shell 补全脚本，不发起任何请求。
var publicCommandPrefixes = []string{
	"aladdin completion",
	"aladdin help",
}

// isPublicCommand 报告命令是否命中免鉴权白名单。
func isPublicCommand(path string) bool {
	if publicCommands[path] {
		return true
	}
	for _, prefix := range publicCommandPrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+" ") {
			return true
		}
	}
	return false
}

// checkCommands 校验每个可执行命令要么声明了权限码，要么在公开白名单中。
//
// 它是"命令覆盖"核查项的实现，在每次执行前运行——代价可忽略，
// 但能在开发阶段立刻发现漏声明的新命令。
func checkCommands(root *cobra.Command) error {
	var walk func(cmd *cobra.Command) error
	walk = func(cmd *cobra.Command) error {
		for _, child := range cmd.Commands() {
			if err := walk(child); err != nil {
				return err
			}
		}
		if !cmd.Runnable() || cmd.Hidden {
			return nil
		}
		path := cmd.CommandPath()
		switch {
		case isPublicCommand(path):
		case requiredPermission(cmd) != "":
		case authenticatedOnly(cmd):
		default:
			return fmt.Errorf("命令 %q 必须三选一：声明所需权限、标记为只需认证、或进入公开白名单", path)
		}
		return nil
	}
	return walk(root)
}

// describeDenial 把服务端返回的拒绝原因转成对用户可读的提示。
//
// 它读取的是错误详情里的结构化 DenialDetail，而不是匹配错误文本——
// 文本会变，枚举取值是契约（见 docs/ssot-registry.md）。
//
// 详情随协议走：CLI 与浏览器同走 Connect，服务端把 DenialDetail 放进错误
// 响应的详情里，connect-go 的 Error.Details() 直接解出强类型消息，因此这里
// 不需要处理任何线格式。
func describeDenial(err error) string {
	ce, ok := connectError(err)
	if !ok {
		return ""
	}

	detail, found := denialDetail(ce)
	if !found {
		return ""
	}

	switch detail.GetReason() {
	case rbac.ReasonScopeMismatch:
		return "当前作用域下权限不足，请使用更高层级的 --scope 或联系管理员授权"
	case rbac.ReasonNoMatchingGrant:
		return "权限不足。若认为这是误判，请执行 `aladdin permissions` 查看当前生效的权限码"
	case rbac.ReasonSessionExpired:
		return "凭证已失效，请重新登录"
	case rbac.ReasonSubjectNotFound:
		return "主体不存在或已停用，请联系管理员"
	case rbac.ReasonStoreUnavailable:
		return "权限服务暂时不可用，请稍后重试"
	case rbac.ReasonAnnotationMissing:
		// 服务端配置缺陷，不是用户的权限问题。把方法名直接报出来，
		// 排障的人一眼知道该找谁，而不是去查自己的角色配置。
		return fmt.Sprintf("服务端方法 %q 未声明鉴权注解，请联系服务端开发",
			detail.GetMetadata()["method"])
	default:
		return ce.Message()
	}
}

// connectError 取出错误里的 Connect 错误。
//
// 不是 Connect 错误（网络故障、配置错误等）时返回 false：那些错误没有拒绝
// 详情可读，由一个统一的错误信息出口去讲。
func connectError(err error) (*connect.Error, bool) {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce, true
	}
	return nil, false
}

// denialDetail 从 Connect 错误里取出结构化的拒绝详情。
func denialDetail(ce *connect.Error) (*rbacv1.DenialDetail, bool) {
	for _, d := range ce.Details() {
		msg, err := d.Value()
		if err != nil {
			continue
		}
		if detail, ok := msg.(*rbacv1.DenialDetail); ok {
			return detail, true
		}
	}
	return nil, false
}
