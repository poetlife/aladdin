package skill

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// 对**真实 GitHub** 的一条冒烟。
//
// **默认跳过**（见 docs/design/skill/onboarding.md 的可验证性表）：它是本仓库唯一的
// 联网测试，其余测试一律不访问网络。跑法：
//
//	ALADDIN_GITHUB_SMOKE=1 go test ./internal/skill/ -run TestSkillImportFromGitHub -v
//
// **强烈建议同时给一个凭据**：取回是"1 + 文件数"次请求，未认证的额度是每小时 60 次，
// 一个几十个文件的仓库一次就吃掉大半（见 onboarding.md 的"远端凭据"）。
//
//	ALADDIN_GITHUB_SMOKE=1 ALADDIN_GITHUB_TOKEN=… go test ./internal/skill/ -run TestSkillImportFromGitHub -v
//
// 它守的是假实现守不住的那一层：**真实的响应长什么样**。写下它之前踩过的两个坑都
// 属于这一层——tarball 端点对某些 Accept 回 415、blob 端点在默认媒体类型下回的是
// JSON 而不是字节——而它们各自表现为一句指向别处的错误（"远端不可达"、"这个文件
// 不是文本"）。
func TestSkillImportFromGitHub(t *testing.T) {
	if os.Getenv("ALADDIN_GITHUB_SMOKE") != "1" {
		t.Skip("跳过：真实 GitHub 冒烟需要显式开启（ALADDIN_GITHUB_SMOKE=1）")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	remote := NewGithubRemote(os.Getenv("ALADDIN_GITHUB_TOKEN"))
	repo, err := ParseRepositoryURL("https://github.com/yanliudesign/mono-color-skill")
	if err != nil {
		t.Fatalf("解析地址: %v", err)
	}

	// 目录树、取字节、包契约连成一条：任何一环与真实响应对不上，这里就炸在那一环。
	commit, err := remote.ResolveCommit(ctx, repo, "")
	if err != nil {
		t.Fatalf("解析引用: %v", err)
	}
	if len(commit) != 40 {
		t.Fatalf("提交标识 = %q，看着不像一个 sha", commit)
	}
	tree, err := remote.FetchTree(ctx, repo, commit, "")
	if err != nil {
		t.Fatalf("取回: %v", err)
	}

	files := make(map[string]string, len(tree.Files))
	for _, file := range tree.Files {
		files[file.Path] = string(file.Data)
	}
	// **这一条最要紧**：blob 端点回的是字节而不是 JSON 包装。
	if !strings.HasPrefix(files[ManifestPath], "---\nname:") {
		t.Errorf("%s 的开头不是 frontmatter，取回的内容可能是错的（前 40 字节：%q）",
			ManifestPath, truncate(files[ManifestPath], 40))
	}
	// 真实仓库里的示例图被跳过并点名——**平台只收文本**，而这条路径要能被走到。
	if !hasSkippedImage(tree.Skipped) {
		t.Errorf("没有跳过任何示例图，跳过的条目是 %v", tree.Skipped)
	}
	// 假实现守不住的那一层过了之后，包契约这一层照常成立。
	if _, err := BuildPackage(tree.Files); err != nil {
		t.Fatalf("真实仓库的包没通过契约: %v", err)
	}
}

func hasSkippedImage(skipped []string) bool {
	for _, path := range skipped {
		if strings.HasSuffix(path, ".png") || strings.HasSuffix(path, ".jpg") {
			return true
		}
	}
	return false
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
