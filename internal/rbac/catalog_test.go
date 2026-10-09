// 本文件是 rbac 的外部测试包：校验 proto 注解、权限目录与判定语义三者一致。
// 走外部包是为了只依赖 rbac 的公开 API，不触达包内实现。
package rbac_test

import (
	"os/exec"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	// 触发 proto 文件描述符的注册。**本文件检查哪些方法的注解，就必须导入哪些
	// 生成包**：eachMethod 遍历的是全局描述符注册表，没导入的文件里的方法
	// 根本不在其中，而"什么都没检查到"只会表现为这些用例静默通过。
	_ "github.com/poetlife/aladdin/api/gen/aladdin/events/v1"
	_ "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	_ "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	_ "github.com/poetlife/aladdin/api/gen/aladdin/ops/v1"
	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	_ "github.com/poetlife/aladdin/api/gen/aladdin/skill/v1"
	_ "github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1"
	"github.com/poetlife/aladdin/internal/rbac"
)

// protoPrefix 限定只检查本仓库自己的 proto，不检查 google/protobuf 等依赖。
const protoPrefix = "aladdin/"

// TestGeneratedFilesInSync 校验生成产物与 api/permissions/catalog.yaml 同步。
//
// 这是"唯一信源"的强制手段：手工修改生成文件后，本测试会失败。
func TestGeneratedFilesInSync(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过：需要执行 go run")
	}
	// 在仓库根目录执行：go run 的包路径与 -root 都相对它解析。
	cmd := exec.Command("go", "run", "./internal/tools/permissiongen", "-check", "-root", ".")
	cmd.Dir = "../.."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("生成产物与 catalog.yaml 不同步，请运行 make gen\n%s", out)
	}
}

// TestCatalogCodesAreWellFormed 校验目录中每个权限码形状合法且无重复。
func TestCatalogCodesAreWellFormed(t *testing.T) {
	if len(rbac.AllPermissionCodes) == 0 {
		t.Fatal("权限目录为空")
	}
	seen := map[rbac.PermissionCode]bool{}
	for _, code := range rbac.AllPermissionCodes {
		if !code.Valid() {
			t.Errorf("权限码 %q 形状非法", code)
		}
		if seen[code] {
			t.Errorf("权限码 %q 重复", code)
		}
		seen[code] = true
	}
}

// TestBuiltinRolesReferenceKnownPermissions 校验内置角色只引用目录中的权限码。
func TestBuiltinRolesReferenceKnownPermissions(t *testing.T) {
	known := map[rbac.PermissionCode]bool{}
	for _, code := range rbac.AllPermissionCodes {
		known[code] = true
	}
	roleIDs := map[string]bool{}
	for _, role := range rbac.BuiltinRoles {
		if role.ID == "" {
			t.Error("内置角色缺少 ID")
		}
		if roleIDs[role.ID] {
			t.Errorf("内置角色 %q 重复", role.ID)
		}
		roleIDs[role.ID] = true
		for _, p := range role.Permissions {
			if !known[p] {
				t.Errorf("角色 %q 引用了目录外的权限码 %q", role.ID, p)
			}
		}
	}
}

// TestProtoAnnotationsReferenceKnownPermissions 校验 proto 方法注解引用的权限码
// 全部存在于权限目录中。
//
// 这是"权限码字面量只允许出现在 proto 与权限目录"这条约定的强制手段：
// proto 里写错一个码，CI 立刻失败，而不是等到线上拒绝一次请求才发现。
func TestProtoAnnotationsReferenceKnownPermissions(t *testing.T) {
	known := map[rbac.PermissionCode]bool{}
	for _, code := range rbac.AllPermissionCodes {
		known[code] = true
	}

	checked := 0
	eachMethod(t, func(fullMethod string, opts *descriptorpb.MethodOptions) {
		checked++
		raw := proto.GetExtension(opts, rbacv1.E_RequiredPermission).(string)
		if raw == "" {
			return
		}
		code := rbac.PermissionCode(raw)
		if !known[code] {
			t.Errorf("方法 %s 声明的权限码 %q 未登记在 api/permissions/catalog.yaml", fullMethod, raw)
		}
	})
	if checked == 0 {
		t.Fatal("未检查到任何方法：proto 描述符可能未注册")
	}
}

// TestEveryMethodIsClassified 校验每个方法都能被拦截器分成三类之一。
//
// 三分法没有"默认放行"这一档：漏写注解的方法会被 KindDenied 捕获并在
// 运行时拒绝，本测试把它提前到构建期。
func TestEveryMethodIsClassified(t *testing.T) {
	eachMethod(t, func(fullMethod string, _ *descriptorpb.MethodOptions) {
		rule, err := rbac.Resolve(fullMethod)
		if err != nil {
			t.Errorf("方法 %s 解析注解失败: %v", fullMethod, err)
			return
		}
		if rule.Kind == rbac.KindDenied {
			t.Errorf("方法 %s 未被分类：%s", fullMethod, rule.Reason)
		}
	})
}

// TestPublicMethodsAreAllowlisted 收敛公开方法。
//
// 公开方法数量只减不增；新增必须在评审中说明理由，因此这里把它固定下来。
//
// 七个成员各自为什么必须公开，见 docs/design/rbac/server-permissions.md。
// 其中"查询可用的登录方式"是这条规则的边界用例：它的返回内容本来就会
// 出现在浏览器里，不公开不保护任何东西。命令行登录的两个方法则是与"登录"
// 同一条循环依赖：调用方正是那个还没登录的终端。
//
// 遥测上报是第六个：登录页上的失败、命令行未登录就退出都发生在拿到会话之前，
// 要求先认证才能上报是同一条循环依赖。它由服务端按动作白名单与限流兜住
// （见 internal/telemetry），而不是靠"要求认证"。
//
// 第七个（解析已发布页面的内容地址）是**访客**打开一条分享地址时主站壳要问的
// 下一跳：它与发布域那条匿名 HTTP 入口回答同一件事，只凭工程标识作答、只返回
// 公开内容、否定结论也与发布态完全一致。要求先认证才能问，等于让"分享给没登录
// 的人"这条唯一用途办不成。
func TestPublicMethodsAreAllowlisted(t *testing.T) {
	want := map[string]bool{
		"/aladdin.identity.v1.IdentityService/Login":            true,
		"/aladdin.identity.v1.IdentityService/Refresh":          true,
		"/aladdin.identity.v1.IdentityService/GetAuthMethods":   true,
		"/aladdin.identity.v1.IdentityService/StartDeviceLogin": true,
		"/aladdin.identity.v1.IdentityService/PollDeviceLogin":  true,
		"/aladdin.telemetry.v1.TelemetryService/ReportEvents":   true,
		"/aladdin.galaxy.v1.GalaxyService/ResolveSharedPage":    true,
	}
	got := map[string]bool{}
	eachMethod(t, func(fullMethod string, _ *descriptorpb.MethodOptions) {
		rule, err := rbac.Resolve(fullMethod)
		if err == nil && rule.Kind == rbac.KindPublic {
			got[fullMethod] = true
		}
	})

	for method := range got {
		if !want[method] {
			t.Errorf("新增了公开方法 %s：公开方法应尽可能少，确需新增请同步更新本测试与文档", method)
		}
	}
	for method := range want {
		if !got[method] {
			t.Errorf("公开方法 %s 不再被标记为 public", method)
		}
	}
}

// TestStreamingMethodsDoNotTakeScopeFromRequestField 收敛流式方法的作用域来源。
//
// 流式拦截器**拿不到请求消息**：框架在被包装的那个函数之后才去读流上那一条消息
// （先读走它，handler 就读不到了，见 internal/server/interceptor/authz.go）。
// 因此"作用域从请求字段取"在流式方法上不可表达——声明了它的表现是**开流即被
// 拒**，而那要等到线上真去开一条流才发现。这条测试把它提前到构建期。
func TestStreamingMethodsDoNotTakeScopeFromRequestField(t *testing.T) {
	streamed := 0
	eachMethod(t, func(fullMethod string, _ *descriptorpb.MethodOptions) {
		desc, err := rbac.MethodDescriptor(fullMethod)
		if err != nil {
			t.Errorf("方法 %s 取描述符失败: %v", fullMethod, err)
			return
		}
		if !desc.IsStreamingClient() && !desc.IsStreamingServer() {
			return
		}
		streamed++
		rule, err := rbac.Resolve(fullMethod)
		if err != nil {
			return // 分类本身的问题由 TestEveryMethodIsClassified 报出
		}
		if rule.ScopeFrom == rbacv1.ScopeSource_SCOPE_SOURCE_REQUEST_FIELD {
			t.Errorf("流式方法 %s 把作用域声明为从请求字段取：流式拦截器读不到那条请求消息，"+
				"流式方法只能从凭证或请求头取作用域（见 docs/design/events/README.md）", fullMethod)
		}
	})
	// 一个流式方法都没有时，这条规则无从检验——而那通常说明 proto 描述符没被
	// 注册，用例已经失效。报出来，别让它静默通过。
	if streamed == 0 {
		t.Fatal("未检查到任何流式方法：proto 描述符可能未注册")
	}
}

// eachMethod 遍历本仓库 proto 中定义的所有 gRPC 方法。
func eachMethod(t *testing.T, fn func(fullMethod string, opts *descriptorpb.MethodOptions)) {
	t.Helper()
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if !strings.HasPrefix(fd.Path(), protoPrefix) {
			return true
		}
		services := fd.Services()
		for i := range services.Len() {
			service := services.Get(i)
			methods := service.Methods()
			for j := range methods.Len() {
				method := methods.Get(j)
				fullMethod := "/" + string(service.FullName()) + "/" + string(method.Name())
				opts, _ := method.Options().(*descriptorpb.MethodOptions)
				if opts == nil {
					opts = &descriptorpb.MethodOptions{}
				}
				fn(fullMethod, opts)
			}
		}
		return true
	})
}
