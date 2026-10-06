package skill

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/poetlife/aladdin/internal/relpath"
)

// 本文件是**来源的形状**：一个仓库地址怎么被拆成"哪个仓库的哪一个引用、哪一段
// 子路径"。
//
// 它是本模块唯一一处由调用方输入决定出站请求目标的地方，因此允许的取值必须窄到
// 可以逐条列举（见 docs/design/skill/onboarding.md 的"来源的形状"）。两条硬性
// 约束：
//
//   - **只认 github.com 的仓库根形状**。别的任何主机都拒绝，包括形如
//     `github.com.evil.example` 的取值——那是按后缀匹配会漏掉的一类。
//   - **取字节的地址由服务端自己拼**。解析的产物是（仓库、引用、子路径）三项，
//     不是"去哪里取"。这一条一旦松动，上面整张白名单就白写了：校验的是调用方给
//     的地址，请求的是另一个，而那正是 SSRF 的经典形态。

const (
	// githubHost 是唯一允许的远端主机。
	githubHost = "github.com"
	// githubURLPrefix 是唯一允许的地址前缀（协议固定 https）。
	githubURLPrefix = "https://" + githubHost + "/"
	// maxRepoSegmentBytes 是仓库地址里 owner / repo 两段各自的长度上限。
	maxRepoSegmentBytes = 100
)

// ParseRepositoryURL 把一个仓库地址解析成仓库（唯一入口）。
//
// 成功时返回的只有两段名字。**调用方给的整串地址此后不再出现**在任何出站请求里
// ——远端实现据这两段自己拼地址。
func ParseRepositoryURL(raw string) (Repository, error) {
	value := trimSpace(raw)
	if value == "" {
		return Repository{}, fmt.Errorf("%w: 地址为空", ErrRepositoryInvalid)
	}
	// 大小写不敏感地比较协议与主机：写成 HTTPS://GitHub.com 的人没有做错什么，
	// 而大小写不同的主机名在网络上是同一个主机。
	lowered := strings.ToLower(value)
	if !strings.HasPrefix(lowered, githubURLPrefix) {
		return Repository{}, fmt.Errorf("%w: 只接受 %s<owner>/<repo> 的地址，收到 %q",
			ErrRepositoryInvalid, githubURLPrefix, raw)
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return Repository{}, fmt.Errorf("%w: %v", ErrRepositoryInvalid, err)
	}
	if parsed.User != nil || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Repository{}, fmt.Errorf("%w: 地址不得带用户信息、端口、查询串或片段", ErrRepositoryInvalid)
	}

	segments := splitRepoSegments(parsed.Path)
	if len(segments) != 2 {
		if len(segments) > 2 {
			// 网页上看来的地址（带 `/tree/<ref>/<path>`）单列一句：它看着合法，
			// 直接放行只会得到一句"仓库不存在"，而真正要改的是把它拆成引用与
			// 子路径两段。
			if hasTreeSegment(parsed.Path) {
				return Repository{}, fmt.Errorf(
					"%w: 这是网页地址（/tree/… 那一段），请改用 --ref 给出引用、--path 给出子路径",
					ErrRepositoryInvalid)
			}
			return Repository{}, fmt.Errorf(
				"%w: 只接受仓库根地址，请用 --path 给出子路径", ErrRepositoryInvalid)
		}
		return Repository{}, fmt.Errorf("%w: 地址里缺少仓库名", ErrRepositoryInvalid)
	}
	for _, segment := range segments {
		if err := validateRepoSegment(segment); err != nil {
			return Repository{}, err
		}
	}
	return Repository{Owner: segments[0], Name: segments[1]}, nil
}

// splitRepoSegments 切出地址路径里的两段，容忍结尾的 `/` 与 `.git`。
//
// 三种写法（`…/repo`、`…/repo/`、`…/repo.git`）是同一个仓库，因此它们必须归一到
// 同一个结果——否则同一个来源能被纳管成两个技能，而"这个技能从哪来"就有两个
// 看起来都对的答案。
func splitRepoSegments(pathValue string) []string {
	trimmed := strings.Trim(trimSpace(pathValue), "/")
	if trimmed == "" {
		return nil
	}
	segments := strings.Split(trimmed, "/")
	last := len(segments) - 1
	segments[last] = strings.TrimSuffix(segments[last], ".git")
	if segments[last] == "" {
		return segments[:last]
	}
	return segments
}

// validateRepoSegment 校验一段 owner / repo。
//
// 字符集比 GitHub 实际允许的略宽（它不收下划线开头的组织名之类），这是刻意的：
// 宽一点点只是让一个无效的仓库走到"远端说它不存在"那一步，而窄一点点会让一个
// 真实存在的仓库在这里就被拒——两种错的代价不一样。
func validateRepoSegment(segment string) error {
	if segment == "" || len(segment) > maxRepoSegmentBytes {
		return fmt.Errorf("%w: 仓库地址的一段为空或过长", ErrRepositoryInvalid)
	}
	if segment == "." || segment == ".." {
		return fmt.Errorf("%w: 仓库地址的一段不能是 %q", ErrRepositoryInvalid, segment)
	}
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-' || c == '_' || c == '.':
		default:
			return fmt.Errorf("%w: 仓库地址的一段含非法字符", ErrRepositoryInvalid)
		}
	}
	return nil
}

// hasTreeSegment 判定一个路径里有没有 `tree` / `blob` 这一段。
func hasTreeSegment(pathValue string) bool {
	for _, segment := range strings.Split(pathValue, "/") {
		if segment == "tree" || segment == "blob" {
			return true
		}
	}
	return false
}

// ParseSubPath 校验来源里的子路径（唯一入口）。
//
// 空表示仓库根。非空时走的是**包内路径同一条**形状校验：子路径不可能解析出包根
// 之外的位置，因此"取回时只保留这一段之下"这条收缩不需要第二套规则。
func ParseSubPath(raw string) (string, error) {
	trimmed := trimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	// 结尾的斜杠去掉（它是粘贴带来的），**开头的不去**：一个以 `/` 起的取值看着是
	// 绝对路径，把它悄悄当成相对路径正是"猜"——而猜错的表现是这个技能纳进来的是
	// 另一段内容，没有任何地方会报错。
	if strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("%w: 子路径 %q 不以斜杠开头", ErrRepositoryInvalid, raw)
	}
	value := strings.TrimSuffix(trimmed, "/")
	if value == "" {
		return "", nil
	}
	if !relpath.Valid(value) {
		return "", fmt.Errorf("%w: 子路径 %q 形状不合法", ErrRepositoryInvalid, raw)
	}
	return value, nil
}

// ParseRef 校验来源里的引用（唯一入口）。
//
// 空表示仓库的默认分支。取值会被放进请求地址的一段里，因此字符集收在分支名实际
// 会用到的那几个字符上；`..` 与空段被拒，理由与路径相同——它们让"这一段"不再是
// 一个名字。
func ParseRef(raw string) (string, error) {
	value := trimSpace(raw)
	if value == "" {
		return "", nil
	}
	if len([]rune(value)) > MaxRefRunes {
		return "", fmt.Errorf("%w: 引用超过 %d 个字符", ErrRepositoryInvalid, MaxRefRunes)
	}
	if strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") {
		return "", fmt.Errorf("%w: 引用 %q 不以斜杠起止", ErrRepositoryInvalid, raw)
	}
	segments := strings.Split(value, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("%w: 引用 %q 含空段或点段", ErrRepositoryInvalid, raw)
		}
		for i := 0; i < len(segment); i++ {
			c := segment[i]
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			case c == '-' || c == '_' || c == '.' || c == '+':
			default:
				return "", fmt.Errorf("%w: 引用 %q 含非法字符", ErrRepositoryInvalid, raw)
			}
		}
	}
	return value, nil
}

// trimSpace 去掉首尾空白。
//
// 收在一处是为了让"什么算空白"在三个解析入口上是同一个答案。
func trimSpace(value string) string { return strings.TrimSpace(value) }
