//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/internal/rbac"
)

// 本文件端到端验证**命令行**这条创作路径：真实的二进制、真实的 cobra 命令树、
// 真实的传输，打到与服务端用例同一台装配上。
//
// 与 cmd/aladdin 单元测试的分工：那边固定参数校验、危险注解、目录读写这类
// 不依赖服务端的结论；这里回答的是"命令确实接到了服务上"——权限声明没写错、
// 凭证与作用域确实被带上、凭据文件与配置分层在真实子进程里没有意外。
//
// **直传的 PUT 不在这里。** 装配里的对象存储是内存假实现，凭证指向真实的
// 存储主机，没有真桶可写（见 docs/design/galaxy/cli.md 的可验证性一节）。因此
// 这里只走到"服务端签发了凭证"为止，字节由后面的用例直接写进假存储。

// buildCLI 构建被测的命令行二进制。
//
// 走**进程外**而不是把命令树拉进来执行：被测的正是"一个只装了二进制的用户
// 能不能走通"，而配置分层、凭证解析、退出码这些恰好在进程内会被测试夹具有意
// 无意地短路掉。
func buildCLI(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "aladdin")
	build := exec.Command("go", "build", "-o", binary, "github.com/poetlife/aladdin/cmd/aladdin")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("构建命令行失败: %v\n%s", err, out)
	}
	return binary
}

// cliEnv 去掉本机可能存在的 ALADDIN_* 变量。
//
// 目标地址与凭证完全由参数给出（参数优先级最高），但环境里残留的值会让同一条
// 用例在本机与 CI 上跑出两种结果。
func cliEnv() []string {
	env := os.Environ()
	kept := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, "ALADDIN_") {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

// runCLI 执行一次命令行，返回它的输出与退出码。
func runCLI(t *testing.T, binary string, h harness, args ...string) (string, string, int) {
	t.Helper()
	full := append([]string{
		"--address", h.address,
		"--token", testToken,
		"--output", "json",
		"--yes",
	}, args...)

	cmd := exec.Command(binary, full...)
	cmd.Env = cliEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	code := 0
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("运行命令行失败: %v\nstderr:\n%s", err, stderr.String())
		}
		code = exitErr.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

// mustRunCLI 执行一次命令行，非零退出即判定用例失败。
func mustRunCLI(t *testing.T, binary string, h harness, args ...string) string {
	t.Helper()
	stdout, stderr, code := runCLI(t, binary, h, args...)
	if code != 0 {
		t.Fatalf("命令行 %v 退出码 = %d，期望 0\nstderr:\n%s", args, code, stderr)
	}
	return stdout
}

// jsonField 从命令行的 JSON 输出里取出一段取值。
func jsonField(t *testing.T, payload string, path ...string) string {
	t.Helper()
	var current any
	if err := json.Unmarshal([]byte(payload), &current); err != nil {
		t.Fatalf("命令行的输出不是 JSON: %v\n%s", err, payload)
	}
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("沿 %v 取值时 %q 不是对象\n%s", path, key, payload)
		}
		current, ok = object[key]
		if !ok {
			t.Fatalf("输出的 JSON 里没有 %q（路径 %v）\n%s", key, path, payload)
		}
	}
	text, ok := current.(string)
	if !ok {
		t.Fatalf("路径 %v 的取值不是字符串\n%s", path, payload)
	}
	return text
}

// writeSite 在本地写出一组文件，返回目录。
//
// 它只给**不经过直传**的那些用例用：命令行侧的 push 会把字节送到桶，而装配里
// 没有真桶（见文件头的说明），因此只能用目录形状不对这类"在发起上传之前就结束"
// 的情形。
func writeSite(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for entryPath, content := range files {
		target := filepath.Join(dir, filepath.FromSlash(entryPath))
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			t.Fatalf("建目录失败: %v", err)
		}
		if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
			t.Fatalf("写文件失败: %v", err)
		}
	}
	return dir
}

// 命令行走完一遍创作：建工程 → 存版本 → 发布 → 匿名取到每一页 → 撤回 →
// 地址不可达。
//
// 这条链路是命令行接入的全部意义所在：一个只装了二进制的人，不打开浏览器就能
// 把一份本地产物发布出去。
//
// **草稿由 RPC 而不是 `draft push` 建。** push 会把每一份文件直传到桶里，而装配
// 里的对象存储是内存假实现、凭证指向真实的存储主机——没有真桶可写。这条边界与
// cli.md 记的是同一处：**不会为了测试给生产代码加一个"换地址"的开关**。因此这里
// 用 RPC 把字节放进假存储（扮演客户端直传），命令行的部分从"清单已经在那儿"开始。
func TestGalaxyCLIAuthoringRoundTrip(t *testing.T) {
	h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
	binary := buildCLI(t)
	client := connectGalaxy(t, h, testToken)

	// 能力下发是"只需认证"的命令：未登录的凭证连它都调不动，因此它同时
	// 验证了凭证确实被带上了。
	capabilities := mustRunCLI(t, binary, h, "galaxy", "capabilities")
	var caps struct {
		Capabilities struct {
			PublishEnabled     bool `json:"publish_enabled"`
			AssetUploadEnabled bool `json:"asset_upload_enabled"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal([]byte(capabilities), &caps); err != nil {
		t.Fatalf("capabilities 的输出不是 JSON: %v\n%s", err, capabilities)
	}
	if !caps.Capabilities.PublishEnabled {
		t.Fatalf("这个装配配了发布域，publish_enabled 应当为真\n%s", capabilities)
	}

	const description = "命令行建的"
	created := mustRunCLI(t, binary, h,
		"galaxy", "project", "create", "--name", "命令行建的站点", "--description", description, "--slot", "site")
	projectID := jsonField(t, created, "project", "id")
	if projectID == "" {
		t.Fatal("建工程没有返回标识")
	}
	// 内容槽是 proto 枚举，JSON 线格式下发的是数值：1 就是 CONTENT_SLOT_SITE。
	var createdForm struct {
		Project struct {
			Slots []struct {
				Slot int `json:"slot"`
			} `json:"slots"`
		} `json:"project"`
	}
	if err := json.Unmarshal([]byte(created), &createdForm); err != nil {
		t.Fatalf("解析建工程的输出失败: %v\n%s", err, created)
	}
	if len(createdForm.Project.Slots) != 1 || createdForm.Project.Slots[0].Slot != 1 {
		t.Fatalf("内容槽 = %+v，期望恰好一个 site（枚举值 1）", createdForm.Project.Slots)
	}

	// 改名只改给出来的那一项：更新请求表达的是完整状态，命令行为此先读当前值，
	// 因此没给的标志必须保持原样，而不是被空串抹掉。
	updated := mustRunCLI(t, binary, h,
		"galaxy", "project", "update", projectID, "--name", "改过的名字")
	if got := jsonField(t, updated, "project", "name"); got != "改过的名字" {
		t.Fatalf("更新后的名称 = %q", got)
	}
	if got := jsonField(t, updated, "project", "description"); got != description {
		t.Fatalf("只改名称不该动简介，实际 %q", got)
	}

	// 发布根由服务端给出，构建命令据此注入 --base。
	base := strings.TrimSpace(mustRunCLI(t, binary, h, "galaxy", "project", "base", projectID, "--output", "text"))
	if !strings.HasPrefix(base, "/g/") || !strings.HasSuffix(base, projectID+"/") {
		t.Fatalf("发布根 = %q，期望形如 /g/<工程标识>/", base)
	}

	// 一份站点：入口、一个子目录里的页、一份样式表。**构建产物里的路径原样保留**。
	const style = "body{margin:0}"
	indexContent := `<link rel="stylesheet" href="/g/` + projectID + `/style.css"><p>命令行写的</p>`
	pushDraft(t, client, projectID,
		pushContentOverRPC(t, h, client, projectID, "index.html", indexContent),
		pushContentOverRPC(t, h, client, projectID, "guide/one.html", "<p>一</p>"),
		pushContentOverRPC(t, h, client, projectID, "style.css", style),
	)

	// 清单列出了每一条条目与它的类别。
	listed := mustRunCLI(t, binary, h, "galaxy", "draft", "list", projectID)
	if !strings.Contains(listed, "index.html") || !strings.Contains(listed, "style.css") {
		t.Errorf("draft list 没有列出全部路径:\n%s", listed)
	}

	// 校验的是**已保存的草稿**：推送是唯一的写入路径。
	if _, stderr, code := runCLI(t, binary, h, "galaxy", "validate", projectID); code != 0 {
		t.Fatalf("一份干净的站点没通过校验（退出码 %d）\nstderr:\n%s", code, stderr)
	}

	saved := mustRunCLI(t, binary, h, "galaxy", "version", "save", projectID)
	versionID := jsonField(t, saved, "version", "id")

	versionList := mustRunCLI(t, binary, h, "galaxy", "version", "list", projectID)
	if !strings.Contains(versionList, versionID) {
		t.Errorf("version list 里没有刚存的版本:\n%s", versionList)
	}

	published := mustRunCLI(t, binary, h, "galaxy", "publish", projectID, versionID)
	address := jsonField(t, published, "publication", "url")
	// 命令行回显的是**分享地址**，它落在主站上：发布域是一处裸沙箱，对外由主站壳
	// 包一层跨源沙箱 iframe（见 docs/design/galaxy/publication.md 的"主站壳"）。
	// `project base` 给的仍是发布域上的发布根——两者不是同一条。
	if !strings.HasPrefix(address, h.appBase) {
		t.Fatalf("分享地址 = %q，期望落在主站下", address)
	}
	// 内容落在发布域上：主站壳要问的那一跳。
	content := resolveSharedPage(t, h, address)
	if !strings.HasPrefix(content, h.publishBase) {
		t.Fatalf("内容地址 = %q，期望落在发布域下", content)
	}

	// **不带任何凭证**取一次产物，且**每一个地址都在**。
	status, body, _ := fetchPublished(t, h, content, "index.html", nil)
	if status != http.StatusOK {
		t.Fatalf("匿名请求发布地址 = %d，期望 200", status)
	}
	if !strings.Contains(body, "命令行写的") {
		t.Error("产物里没有命令行写进去的正文")
	}
	if !strings.Contains(body, "/g/"+projectID+"/style.css") {
		t.Error("构建产物里的绝对路径被改写了")
	}
	if status, body, _ := fetchPublished(t, h, content, "guide/one.html", nil); status != http.StatusOK || !strings.Contains(body, "<p>一</p>") {
		t.Errorf("guide/one.html = %d / %q", status, body)
	}
	if status, css, _ := fetchPublished(t, h, content, "style.css", nil); status != http.StatusOK || css != style {
		t.Errorf("style.css = %d / %q", status, css)
	}

	mustRunCLI(t, binary, h, "galaxy", "unpublish", projectID)
	if status, _, _ := fetchPublished(t, h, content, "index.html", nil); status != http.StatusNotFound {
		t.Fatalf("撤回之后再取发布地址 = %d，期望 404", status)
	}

	// 一份引用了不存在资产的清单：validate 以非零状态退出，好让
	// "validate && publish" 这类脚本成立。
	pushDraft(t, client, projectID,
		pushContentOverRPC(t, h, client, projectID, "index.html", indexContent),
		&galaxyv1.FileEntry{Path: "a.png", Source: &galaxyv1.FileEntry_AssetId{AssetId: "ast_missing"}},
	)
	if _, _, code := runCLI(t, binary, h, "galaxy", "validate", projectID); code != 1 {
		t.Fatalf("引用不存在的资产应当以非零状态退出（未分类失败），实际退出码 = %d", code)
	}

	mustRunCLI(t, binary, h, "galaxy", "project", "delete", projectID)
	if _, _, code := runCLI(t, binary, h, "galaxy", "project", "get", projectID); code == 0 {
		t.Fatal("删除之后这个工程还读得到")
	}
}

// 目录里出现白名单外的扩展名时，命令行**在发起任何上传之前**就以用法错误结束。
//
// 这是"目录是整组的输入单位"那条规则在真实二进制上的落点：判定只做一次，而它
// 发生在客户端，因此一次注定失败的推送不会先把一半文件传上去。
func TestGalaxyCLIPushRejectsUnknownFilesLocally(t *testing.T) {
	h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
	binary := buildCLI(t)

	created := mustRunCLI(t, binary, h,
		"galaxy", "project", "create", "--name", "本地校验", "--slot", "site")
	projectID := jsonField(t, created, "project", "id")

	dir := writeSite(t, map[string]string{
		"index.html": "<p>x</p>",
		"notes.xyz":  "?",
	})
	_, stderr, code := runCLI(t, binary, h, "galaxy", "draft", "push", projectID, dir)
	if code != 2 {
		t.Fatalf("目录里有白名单外的文件时退出码 = %d，期望 2（用法错误）\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "notes.xyz") {
		t.Errorf("提示 %q 没有指出那一份文件", stderr)
	}
	// 一份字节都没有上传：拒绝发生在编排开始之前。
	if h.objects.Count() != 0 {
		t.Errorf("被拒的推送往桶里写了 %d 个对象", h.objects.Count())
	}
}

// 没有权限时命令行拿到的是"权限不足"，而不是"未登录"：两者的退出码不同，
// 脚本据此决定是重新登录还是不要重试（见 docs/design/rbac/cli-permissions.md）。
func TestGalaxyCLIWithoutPermissionIsDenied(t *testing.T) {
	// 这个角色没有任何 galaxy 权限码。
	h := startServer(t, rbac.RoleViewer, testScope)
	binary := buildCLI(t)

	_, stderr, code := runCLI(t, binary, h, "galaxy", "project", "list")
	if code != 4 {
		t.Fatalf("无权限时退出码 = %d，期望 4（权限不足）\nstderr:\n%s", code, stderr)
	}
}
