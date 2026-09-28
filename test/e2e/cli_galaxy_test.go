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

	"github.com/poetlife/aladdin/internal/rbac"
)

// 本文件端到端验证**命令行**这条创作路径：真实的二进制、真实的 cobra 命令树、
// 真实的传输，打到与服务端用例同一台装配上。
//
// 与 cmd/aladdin 单元测试的分工：那边固定参数校验、危险注解、类型推断这类
// 不依赖服务端的结论；这里回答的是"命令确实接到了服务上"——权限声明没写错、
// 凭证与作用域确实被带上、凭据文件与配置分层在真实子进程里没有意外。
//
// **资产上传的 PUT 不在这里。** 装配里的对象存储是内存假实现，凭证指向真实的
// 存储主机，没有真桶可写（见 docs/design/galaxy/cli.md 的可验证性一节）。

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

// 命令行走完一遍创作：建工程 → 从本地文件存草稿 → 存版本 → 发布 → 匿名取到
// 产物 → 撤回 → 地址不可达。
//
// 这条链路是命令行接入的全部意义所在：一个只装了二进制的人，不打开浏览器就能
// 把一份本地 HTML 发布出去。
func TestGalaxyCLIAuthoringRoundTrip(t *testing.T) {
	h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
	binary := buildCLI(t)

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
		"galaxy", "project", "create", "--name", "命令行建的页面", "--description", description)
	projectID := jsonField(t, created, "project", "id")
	if projectID == "" {
		t.Fatal("建工程没有返回标识")
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

	content := "<!doctype html><html><body><p>命令行写的</p></body></html>"
	pagePath := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(pagePath, []byte(content), 0o600); err != nil {
		t.Fatalf("准备正文文件失败: %v", err)
	}

	mustRunCLI(t, binary, h, "galaxy", "draft", "save", projectID, "--file", pagePath)

	// 草稿读回来的**只有正文**，可以直接重定向成文件。
	drafted := mustRunCLI(t, binary, h, "galaxy", "draft", "get", projectID, "--output", "text")
	if drafted != content {
		t.Fatalf("draft get 的输出不是正文本身:\n期望 %q\n实际 %q", content, drafted)
	}

	// 引用了资产库里没有的东西：校验必须拦下，并以非零状态退出，好让
	// "validate && publish" 这类脚本成立。
	brokenPath := filepath.Join(t.TempDir(), "broken.html")
	broken := "<!doctype html><img src=\"asset://ast_missing\">"
	if err := os.WriteFile(brokenPath, []byte(broken), 0o600); err != nil {
		t.Fatalf("准备正文文件失败: %v", err)
	}
	if _, stderr, code := runCLI(t, binary, h,
		"galaxy", "validate", projectID, "--file", brokenPath); code != 1 {
		t.Fatalf("引用不存在的资产应当以非零状态退出（未分类失败），实际退出码 = %d\nstderr:\n%s",
			code, stderr)
	}
	mustRunCLI(t, binary, h, "galaxy", "validate", projectID, "--file", pagePath)

	saved := mustRunCLI(t, binary, h, "galaxy", "version", "save", projectID)
	versionID := jsonField(t, saved, "version", "id")

	// 版本正文读回来与存进去时逐字相同，并且同样只输出正文本身。
	savedContent := mustRunCLI(t, binary, h,
		"galaxy", "version", "get", projectID, versionID, "--output", "text")
	if savedContent != content {
		t.Fatalf("version get 的输出不是正文本身:\n期望 %q\n实际 %q", content, savedContent)
	}

	published := mustRunCLI(t, binary, h, "galaxy", "publish", projectID, versionID)
	address := jsonField(t, published, "project", "published_url")
	if !strings.HasPrefix(address, h.publishBase) {
		t.Fatalf("发布地址 = %q，期望落在发布域下", address)
	}

	// **不带任何凭证**取一次产物。
	status, body, _ := fetchPublishAddress(t, h, address)
	if status != http.StatusOK {
		t.Fatalf("匿名请求发布地址 = %d，期望 200", status)
	}
	if !strings.Contains(body, "命令行写的") {
		t.Error("产物里没有命令行写进去的正文")
	}

	mustRunCLI(t, binary, h, "galaxy", "unpublish", projectID)
	if status, _, _ := fetchPublishAddress(t, h, address); status != http.StatusNotFound {
		t.Fatalf("撤回之后再取发布地址 = %d，期望 404", status)
	}

	mustRunCLI(t, binary, h, "galaxy", "project", "delete", projectID)
	if _, _, code := runCLI(t, binary, h, "galaxy", "project", "get", projectID); code == 0 {
		t.Fatal("删除之后这个工程还读得到")
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
