package rbac

import (
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
)

// 本文件是方法级鉴权注解的唯一读取入口。
//
// 受控方法所需的权限码与作用域来源**声明在 proto 方法上**
// （api/proto/aladdin/rbac/v1/annotations.proto），这里从方法描述符读回，
// 因此新增受控接口只需要改 proto，不需要记得同步修改任何 Go 代码。
//
// 它住在 rbac 而不是拦截器包里，因为"这个方法属于哪一类"是权限语义，
// 不是传输层的事。至少有三类调用方要问同一个问题：Connect 拦截器、
// HTTP 中间件，以及文档生成器——答案只有一份，谁都不得另写一份。

// Kind 是一个受控方法在鉴权维度上的分类。
//
// 三分法是刻意的：一个方法必须恰好落进某一类，没有任何一类可被省略，
// 否则"漏写注解的业务方法"会被默认放行。
type Kind int

const (
	// KindDenied 表示方法没有任何有效注解，默认拒绝。
	KindDenied Kind = iota
	// KindPublic 表示方法免认证免鉴权。
	KindPublic
	// KindAuthenticatedOnly 表示只需认证，任何已确认主体可调用。
	KindAuthenticatedOnly
	// KindRequires 表示需要认证且需要指定权限。
	KindRequires
)

// String 返回该分类的稳定标识符。
//
// 它是对外契约（文档扩展、日志），取值不要随文案调整而变。
func (k Kind) String() string {
	switch k {
	case KindDenied:
		return "denied"
	case KindPublic:
		return "public"
	case KindAuthenticatedOnly:
		return "authenticated_only"
	case KindRequires:
		return "requires"
	default:
		return "unknown"
	}
}

// MethodRule 是解析一个 RPC 方法注解后的结果。
type MethodRule struct {
	Kind       Kind
	Permission PermissionCode
	ScopeFrom  rbacv1.ScopeSource
	// Reason 在 Kind 为 KindDenied 时说明拒因，用于排查注解遗漏。
	Reason string
}

// ErrUnknownProcedure 表示这个过程名根本不对应任何已注册的 RPC 方法：路径形状
// 不对（`/`、`/nope` 这类），或者指向一个不存在的服务 / 方法。
//
// **它与"方法存在、但没写鉴权注解"是两回事。** 后者（KindDenied）是服务端漏写
// 注解，属于服务端缺陷；前者是调用方把地址打错了。调用方据此给出**不同的错误码**
// ——混成同一个，会让一次地址打错在面板上呈现为服务端故障，也会让排障的人去找
// 一个并不存在的漏写注解（见 docs/design/rbac/server-permissions.md 的"拒绝语义"）。
var ErrUnknownProcedure = errors.New("未知的 RPC 过程名")

// Resolve 从 RPC 过程名解析出鉴权规则。
//
// procedure 形如 "/aladdin.rbac.v1.RBACService/GetRole"。这个格式对三种
// 调用方是同一个：gRPC 的 info.FullMethod、Connect 的 req.Spec().Procedure，
// 以及 HTTP 中间件手上的 r.URL.Path（Connect 的路径格式与 procedure 相同）。
// 因此本函数只写一份。
//
// 注解定义在 api/proto/aladdin/rbac/v1/annotations.proto，
// 这里是它唯一的读取入口——任何调用方都不得硬编码方法到权限码的映射。
func Resolve(procedure string) (MethodRule, error) {
	opts, err := methodOptions(procedure)
	if err != nil {
		return MethodRule{}, err
	}

	if proto.GetExtension(opts, rbacv1.E_Public).(bool) {
		return MethodRule{Kind: KindPublic}, nil
	}

	permission := PermissionCode(proto.GetExtension(opts, rbacv1.E_RequiredPermission).(string))
	authenticatedOnly := proto.GetExtension(opts, rbacv1.E_AuthenticatedOnly).(bool)
	scopeFrom := proto.GetExtension(opts, rbacv1.E_ScopeSource).(rbacv1.ScopeSource)

	switch {
	case permission != "":
		if !permission.Valid() {
			return MethodRule{}, fmt.Errorf("方法 %s 声明的权限码 %q 形状非法", procedure, permission)
		}
		return MethodRule{Kind: KindRequires, Permission: permission, ScopeFrom: scopeFrom}, nil
	case authenticatedOnly:
		return MethodRule{Kind: KindAuthenticatedOnly, ScopeFrom: scopeFrom}, nil
	default:
		return MethodRule{
			Kind:   KindDenied,
			Reason: "方法未声明 required_permission，也未标记 public 或 authenticated_only",
		}, nil
	}
}

// MethodDescriptor 通过过程名取回方法描述符。
//
// 它是"过程名 → 描述符"这**唯一一处**查找：判定路径与文档生成都要问同一个
// 问题，各写一份必然漂移（见 docs/ssot-registry.md）。**怎么解读选项由调用方
// 决定**——鉴权语义在 Resolve，传输语义（如 idempotency_level）由需要它的
// 调用方自己读，本包不替它们下结论。
func MethodDescriptor(procedure string) (protoreflect.MethodDescriptor, error) {
	service, method, err := splitProcedure(procedure)
	if err != nil {
		return nil, err
	}

	desc, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(service))
	if err != nil {
		return nil, fmt.Errorf("%w：未找到服务描述符 %s（%w）", ErrUnknownProcedure, service, err)
	}
	serviceDesc, ok := desc.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, fmt.Errorf("%w：%s 不是服务描述符", ErrUnknownProcedure, service)
	}
	methodDesc := serviceDesc.Methods().ByName(protoreflect.Name(method))
	if methodDesc == nil {
		return nil, fmt.Errorf("%w：服务 %s 上不存在方法 %s", ErrUnknownProcedure, service, method)
	}
	return methodDesc, nil
}

// methodOptions 取回方法上的注解。
//
// 走描述符而非硬编码映射，是为了让"新增受控接口"只需要改 proto，
// 不需要记得同步修改任何 Go 代码。
func methodOptions(procedure string) (*descriptorpb.MethodOptions, error) {
	methodDesc, err := MethodDescriptor(procedure)
	if err != nil {
		return nil, err
	}
	opts, ok := methodDesc.Options().(*descriptorpb.MethodOptions)
	if !ok || opts == nil {
		return &descriptorpb.MethodOptions{}, nil
	}
	return opts, nil
}

// splitProcedure 把 "/pkg.Service/Method" 拆成服务全名与方法名。
func splitProcedure(procedure string) (string, string, error) {
	trimmed := strings.TrimPrefix(procedure, "/")
	service, method, ok := strings.Cut(trimmed, "/")
	if !ok || service == "" || method == "" {
		return "", "", fmt.Errorf("%w %q", ErrUnknownProcedure, procedure)
	}
	return service, method, nil
}
