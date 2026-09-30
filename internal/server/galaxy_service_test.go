package server

import (
	"strings"
	"testing"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/internal/galaxy"
	"github.com/poetlife/aladdin/internal/identity"
	"github.com/poetlife/aladdin/internal/rbac"
)

// 创作接口面上**没有"指定拥有者"这个形状**。
//
// 归属由凭证决定，不由请求里的字段决定。这条不只是设计取向：它意味着接口面上不
// 存在一个可以被伪造的拥有者参数，也不存在"用别人的工程标识试试看"这个动作的
// 入口（见 docs/design/galaxy/project-versioning.md）。
//
// 它扫的是生成出来的 method descriptor，因此新加一个带 owner 字段的请求会**直接
// 让构建失败**，而不是等到有人读 proto 时才发现。
func TestGalaxyInterfaceHasNoOwnerTarget(t *testing.T) {
	forbidden := map[string]bool{
		"owner":            true,
		"owner_subject":    true,
		"owner_subject_id": true,
		"subject_id":       true,
		"subject":          true,
		"target_subject":   true,
	}

	services := galaxyv1.File_aladdin_galaxy_v1_galaxy_proto.Services()
	if services.Len() != 1 {
		t.Fatalf("galaxy proto 里有 %d 个服务，期望 1 个", services.Len())
	}
	methods := services.Get(0).Methods()
	if methods.Len() == 0 {
		t.Fatal("创作服务没有任何方法")
	}
	for i := 0; i < methods.Len(); i++ {
		method := methods.Get(i)
		fields := method.Input().Fields()
		for j := 0; j < fields.Len(); j++ {
			name := string(fields.Get(j).Name())
			if forbidden[name] {
				t.Errorf("%s 的请求里有字段 %q：创作方法不得以指定拥有者为目标", method.Name(), name)
			}
		}
	}
}

// 接口面上**没有枚举入口**：没有"列出所有工程"、"按名称查工程"、"列出已发布
// 页面"这类方法。
//
// 工程标识不可猜是发布态匿名可读的唯一防线，而任何枚举入口都会把它降级成
// "打开就能逛"。
func TestGalaxyInterfaceHasNoEnumerationEntry(t *testing.T) {
	// 这些子串出现在方法名里就说明多了一个枚举入口。
	forbidden := []string{
		"All",         // ListAllProjects 之类
		"ByName",      // 名称不是查找键
		"Name",        // GetProjectByName
		"Search",      // 搜索即枚举
		"Publication", // 发布记录不出现在接口面上：对外只剩一个地址
		"Published",   // ListPublishedProjects
	}

	methods := galaxyv1.File_aladdin_galaxy_v1_galaxy_proto.Services().Get(0).Methods()
	for i := 0; i < methods.Len(); i++ {
		name := string(methods.Get(i).Name())
		for _, bad := range forbidden {
			if strings.Contains(name, bad) {
				t.Errorf("方法 %s 看起来是一个枚举入口（含 %q）", name, bad)
			}
		}
	}
}

// 每个创作方法都要**能被归入三类之一**，且只有一个公开方法。
//
// 创作面的内容不属于"未认证也能拿到"的东西——唯一匿名可达的是发布地址那条 HTTP
// 入口，它根本不是 RPC 方法。**唯一的例外是主站壳的解析调用**
// （ResolveSharedPage）：访客打开一条分享地址时没有会话，而它回答的事实与那条
// HTTP 入口完全一样，因此与它同一档（见 docs/design/rbac/server-permissions.md
// 的公开方法白名单）。除它之外任何方法落进这一档都是缺陷。
func TestGalaxyMethodsAreClassified(t *testing.T) {
	serviceName := "aladdin.galaxy.v1.GalaxyService"
	methods := galaxyv1.File_aladdin_galaxy_v1_galaxy_proto.Services().Get(0).Methods()
	if methods.Len() == 0 {
		t.Fatal("创作服务没有任何方法")
	}
	publicAllowed := map[string]bool{"ResolveSharedPage": true}
	seenPublic := map[string]bool{}
	for i := 0; i < methods.Len(); i++ {
		method := methods.Get(i)
		procedure := "/" + serviceName + "/" + string(method.Name())

		rule, err := rbac.Resolve(procedure)
		if err != nil {
			t.Errorf("%s 的注解读不出来: %v", procedure, err)
			continue
		}
		if rule.Kind == rbac.KindPublic {
			if !publicAllowed[string(method.Name())] {
				t.Errorf("%s 是公开方法——创作面不该有不需要任何凭证的 RPC", procedure)
				continue
			}
			seenPublic[string(method.Name())] = true
		}
		if rule.Kind == rbac.KindDenied {
			t.Errorf("%s 没有被归入三类之一: %s", procedure, rule.Reason)
		}
	}
	// 白名单里的那个必须**确实**是公开的：注解被误删时上面那个循环不会报错，
	// 而主站壳会静默变成"未认证调用被拒"。
	for name := range publicAllowed {
		if !seenPublic[name] {
			t.Errorf("%s 不再被标记为 public：主站壳的匿名解析会因此被拒", "/"+serviceName+"/"+name)
		}
	}

	// 发布与撤回共用同一个权限码：撤回是发布的反向操作，单独给一码不会带来任何
	// 安全收益，只会让"发布得出去、收不回来"变成一个可以只授一半的组合。
	for _, method := range []string{"Publish", "Unpublish"} {
		rule, err := rbac.Resolve("/" + serviceName + "/" + method)
		if err != nil {
			t.Fatalf("读取 %s 的注解失败: %v", method, err)
		}
		if rule.Permission != rbac.PermissionGalaxyProjectPublish {
			t.Errorf("%s 需要的权限码 = %q，期望 %q", method, rule.Permission, rbac.PermissionGalaxyProjectPublish)
		}
	}
	// 上传、删除与改元数据资产共用 galaxy.asset.write；读取与发布各有自己的码。
	cases := map[string]rbac.PermissionCode{
		"BeginAssetUpload":    rbac.PermissionGalaxyAssetWrite,
		"CommitAssetUpload":   rbac.PermissionGalaxyAssetWrite,
		"DeleteAsset":         rbac.PermissionGalaxyAssetWrite,
		"UpdateAsset":         rbac.PermissionGalaxyAssetWrite,
		"ListAssets":          rbac.PermissionGalaxyAssetRead,
		"ListProjects":        rbac.PermissionGalaxyProjectRead,
		"PushDraft":           rbac.PermissionGalaxyProjectWrite,
		"BeginContentUpload":  rbac.PermissionGalaxyProjectWrite,
		"CommitContentUpload": rbac.PermissionGalaxyProjectWrite,
		"ValidateDraft":       rbac.PermissionGalaxyProjectRead,
		"PreviewDraft":        rbac.PermissionGalaxyProjectRead,
	}
	for method, want := range cases {
		rule, err := rbac.Resolve("/" + serviceName + "/" + method)
		if err != nil {
			t.Fatalf("读取 %s 的注解失败: %v", method, err)
		}
		if rule.Permission != want {
			t.Errorf("%s 需要的权限码 = %q，期望 %q", method, rule.Permission, want)
		}
	}
}

// 发布地址是一条**浏览器直连的非 RPC 入口**：它必须在放行清单里，而不是被当成
// 一个没有注解的方法拦下。
//
// 两件事一起断言：它不是一个可解析的 RPC 过程名（因此必须列进清单），而清单确实
// 放行它。这正是"清单里不得出现 RPC 过程名"那条边界没有被破坏的证据。
func TestPublishAddressIsABrowserEntry(t *testing.T) {
	address := galaxy.PublicPathPrefix + "prj_abc"

	if _, err := rbac.Resolve(address); err == nil {
		t.Error("发布地址被当成了一个 RPC 过程名——它不可能是")
	}
	if !isBrowserEntry(address) {
		t.Error("发布地址没有登记在浏览器直连清单里")
	}
	// 清单里的精确路径仍然照常放行，而前缀不吃前缀之外的路径。
	if !isBrowserEntry(identity.GithubStartPath) {
		t.Error("清单里的精确路径不再放行")
	}
	if isBrowserEntry("/galaxy") {
		t.Error("前缀之外的路径不该被放行")
	}
}

// 发布地址的**指标属性必须有界**。
//
// 它的最后一段是不可猜的工程标识，直接拿它当指标属性会让时序数量随被访问的页面
// 数增长——那正是"指标属性必须受控"要防的事。
func TestPublishAddressIsNormalizedForMetrics(t *testing.T) {
	middleware := &telemetryMiddleware{
		registeredPaths: []string{galaxy.PublicPathPrefix},
	}
	for _, projectID := range []string{"prj_a", "prj_b", "prj_c"} {
		path := galaxy.PublicPathPrefix + projectID
		if got := middleware.procedureOf(path); got != galaxy.PublicPathPrefix {
			t.Errorf("%s 归一成了 %q，期望前缀 %q（否则每个页面各占一个时序）", path, got, galaxy.PublicPathPrefix)
		}
	}
}
