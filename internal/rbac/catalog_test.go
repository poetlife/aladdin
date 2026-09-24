// 本文件位于外部测试包 rbac_test，因为它需要同时 import
// rbac（权限目录）与 server/interceptor（注解解析）——
// 放在 rbac 包内会构成 import cycle。
package rbac_test

import (
	"os/exec"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	// 触发 proto 文件描述符的注册。
	_ "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/internal/server/interceptor"
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
		rule, err := interceptor.Resolve(fullMethod)
		if err != nil {
			t.Errorf("方法 %s 解析注解失败: %v", fullMethod, err)
			return
		}
		if rule.Kind == interceptor.KindDenied {
			t.Errorf("方法 %s 未被分类：%s", fullMethod, rule.Reason)
		}
	})
}

// TestPublicMethodsAreAllowlisted 收敛公开方法。
//
// 公开方法数量只减不增；新增必须在评审中说明理由，因此这里把它固定下来。
func TestPublicMethodsAreAllowlisted(t *testing.T) {
	want := map[string]bool{
		"/aladdin.identity.v1.IdentityService/Login":   true,
		"/aladdin.identity.v1.IdentityService/Refresh": true,
	}
	got := map[string]bool{}
	eachMethod(t, func(fullMethod string, _ *descriptorpb.MethodOptions) {
		rule, err := interceptor.Resolve(fullMethod)
		if err == nil && rule.Kind == interceptor.KindPublic {
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
