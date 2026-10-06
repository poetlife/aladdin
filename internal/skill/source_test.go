package skill

import (
	"strings"
	"testing"
)

// 来源的形状是本模块的**安全边界**：出站请求的目标由它决定，因此这里逐条钉死
// （见 docs/design/skill/onboarding.md 的"来源的形状"）。

func TestParseRepositoryURLAccepts(t *testing.T) {
	cases := map[string]Repository{
		"https://github.com/owner/repo":             {Owner: "owner", Name: "repo"},
		"https://github.com/owner/repo/":            {Owner: "owner", Name: "repo"},
		"https://github.com/owner/repo.git":         {Owner: "owner", Name: "repo"},
		"https://github.com/owner/repo.git/":        {Owner: "owner", Name: "repo"},
		"https://GitHub.com/Owner/Repo":             {Owner: "Owner", Name: "Repo"},
		"  https://github.com/yanliudesign/x-skill": {Owner: "yanliudesign", Name: "x-skill"},
		"https://github.com/a/b_c.d-e":              {Owner: "a", Name: "b_c.d-e"},
	}
	for raw, want := range cases {
		got, err := ParseRepositoryURL(raw)
		if err != nil {
			t.Errorf("解析 %q 失败: %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("解析 %q = %+v，期望 %+v", raw, got, want)
		}
	}
}

// 三种写法（`…/repo`、`…/repo/`、`…/repo.git`）必须归一到同一个结果：否则同一个
// 来源能被纳管成两个技能，而"这个技能从哪来"就有两个看起来都对的答案。
func TestParseRepositoryURLNormalizesToSameRepository(t *testing.T) {
	first, err := ParseRepositoryURL("https://github.com/owner/repo.git/")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	second, err := ParseRepositoryURL("https://github.com/owner/repo")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if first != second {
		t.Errorf("同一仓库的两种写法解析成 %+v 与 %+v", first, second)
	}
}

func TestParseRepositoryURLRejects(t *testing.T) {
	cases := map[string]string{
		"空":         "",
		"只是主机":      "https://github.com",
		"只有一段":      "https://github.com/owner",
		"三段":        "https://github.com/owner/repo/extra",
		"http":      "http://github.com/owner/repo",
		"别的协议":      "git://github.com/owner/repo",
		"别的站点":      "https://gitlab.com/owner/repo",
		"后缀伪装的主机":   "https://github.com.evil.example/owner/repo",
		"前缀伪装的主机":   "https://notgithub.com/owner/repo",
		"带端口":       "https://github.com:8443/owner/repo",
		"带用户信息":     "https://user:pass@github.com/owner/repo",
		"带查询串":      "https://github.com/owner/repo?ref=main",
		"带片段":       "https://github.com/owner/repo#readme",
		"owner 是点段": "https://github.com/../repo",
		"段含空格":      "https://github.com/ow ner/repo",
		"段超长":       "https://github.com/" + strings.Repeat("a", 101) + "/repo",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseRepositoryURL(raw); err == nil {
				t.Errorf("地址 %q 被接受了", raw)
			} else if !strings.Contains(err.Error(), "仓库地址") {
				t.Errorf("地址 %q 的错误没有点名是地址问题: %v", raw, err)
			}
		})
	}
}

// 网页上看来的地址（带 /tree/…）单列一句提示：它看着合法，直接放行只会得到一句
// "仓库不存在"，而真正要改的是把它拆成引用与子路径。
func TestParseRepositoryURLPointsAtWebAddresses(t *testing.T) {
	_, err := ParseRepositoryURL("https://github.com/owner/repo/tree/main/skills/foo")
	if err == nil {
		t.Fatal("网页地址被接受了")
	}
	for _, want := range []string{"--ref", "--path"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息里没有提到 %s: %v", want, err)
		}
	}
}

func TestParseRef(t *testing.T) {
	accepts := map[string]string{
		"":                       "",
		"main":                   "main",
		"v1.2.3":                 "v1.2.3",
		"feature/skill-catalog":  "feature/skill-catalog",
		"  main  ":               "main",
		strings.Repeat("a", 256): strings.Repeat("a", 256),
	}
	for raw, want := range accepts {
		got, err := ParseRef(raw)
		if err != nil {
			t.Errorf("解析引用 %q 失败: %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("解析引用 %q = %q，期望 %q", raw, got, want)
		}
	}

	rejects := map[string]string{
		"超长":     strings.Repeat("a", 257),
		"空段":     "feature//x",
		"点段":     "feature/../x",
		"只有点段":   "..",
		"以斜杠开头":  "/main",
		"以斜杠结尾":  "main/",
		"含空格":    "ma in",
		"含问号":    "main?x",
		"含控制字符":  "ma\nin",
		"含反斜杠":   `ma\in`,
		"含百分号编码": "%2E%2E",
	}
	for name, raw := range rejects {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseRef(raw); err == nil {
				t.Errorf("引用 %q 被接受了", raw)
			}
		})
	}
}

// 子路径走的是**包内路径同一条**形状校验：它不可能解析出包根之外的位置。
func TestParseSubPath(t *testing.T) {
	accepts := map[string]string{
		"":            "",
		"skills/foo":  "skills/foo",
		"skills/foo/": "skills/foo",
		"a/b/c":       "a/b/c",
		ManifestPath:  ManifestPath,
	}
	for raw, want := range accepts {
		got, err := ParseSubPath(raw)
		if err != nil {
			t.Errorf("解析子路径 %q 失败: %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("解析子路径 %q = %q，期望 %q", raw, got, want)
		}
	}

	for _, raw := range []string{
		"..", "a/../../b", "a/./b", "a//b", "a b", "参考/配色",
		// 以斜杠开头的取值看着是绝对路径，**不悄悄当成相对路径**。
		"/etc", "/skills/foo", "/",
	} {
		if _, err := ParseSubPath(raw); err == nil {
			t.Errorf("子路径 %q 被接受了", raw)
		}
	}
}

// URL 由两段现拼，与调用方给的那串形状无关：界面上不会因为有人多写了一个 `.git`
// 就出现两种"看起来不同的来源"。
func TestSourceURLIsCanonical(t *testing.T) {
	source := Source{Owner: "owner", Name: "repo", Ref: "main", SubPath: "skills/x"}
	if got := source.URL(); got != "https://github.com/owner/repo" {
		t.Errorf("来源地址 = %q", got)
	}
	if got := (Source{}).URL(); got != "" {
		t.Errorf("空来源给出的地址是 %q，期望空串", got)
	}
}
