package galaxy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Problem 是正文里的一处问题。
//
// 消息里**带上位置（行号）与出问题的取值**：只说"有引用不合法"会让用户在一份
// 几百行的正文里自己找。
type Problem struct {
	Message string
}

// Report 是一段正文的校验结论。问题为空表示它可以发布。
type Report struct {
	Problems []Problem
}

// OK 表示这段正文可以发布。
func (r Report) OK() bool { return len(r.Problems) == 0 }

// Messages 返回全部问题消息，供把结论转成一句话的调用方使用。
func (r Report) Messages() []string {
	messages := make([]string, 0, len(r.Problems))
	for _, problem := range r.Problems {
		messages = append(messages, problem.Message)
	}
	return messages
}

// ValidateContent 校验一段正文能不能发布（唯一入口）。
//
// 编辑器需要即时提示时调用的是它，发布的前置阶段调用的也是它——**同一段代码、
// 同一份规则集合**。两端各写一份的表现有两种，都很难排查：编辑器说没问题而
// 发布说不行，用户被卡住，且没有任何提示告诉他哪一句是真的；或者反过来，用户
// 放弃一个其实能用的功能。
//
// 返回的是一个**问题清单**而不是单个错误：编辑器要的是"哪几处有问题"。存储
// 不可用这类故障仍然以 error 返回，与"正文有问题"分开——混在一起会让一次
// 数据库抖动表现成"你的正文写错了"。
func (s *Service) ValidateContent(ctx context.Context, subjectID, projectID string, content Document) (Report, error) {
	if _, err := OwnedProject(ctx, s.store, projectID, subjectID); err != nil {
		return Report{}, err
	}
	_, report, err := s.validateDocument(ctx, projectID, content)
	return report, err
}

// locatedProblem 是一处带位置的问题，用于把两趟扫描的结果按正文顺序合并。
type locatedProblem struct {
	offset  int
	message string
}

// validateDocument 是校验规则集合的实现，并在通过时给出改写好的产物。
//
// 规则全集（见 docs/design/galaxy/publication.md）：
//
//  1. 正文不超过体积上限；
//  2. 每一个 `asset://<资产标识>` 都指向本工程现存的一个资产；
//  3. 取资源的位置不得指向本工程资产库之外（用户写的导航链接不受此限）；
//  4. 改写后的产物不超过体积上限。
//
// 产物在这里算出来而不是在发布阶段另算一次：改写是纯计算，而"产物会不会超限"
// 是校验的一部分——把它留到落库前才发现，会让一次被拒的发布先产生上架副作用。
func (s *Service) validateDocument(ctx context.Context, projectID string, content Document) (Document, Report, error) {
	if err := CheckDocumentSize(content); err != nil {
		return "", Report{Problems: []Problem{{Message: err.Error()}}}, nil
	}

	var problems []locatedProblem

	// 占位符：每个都必须指向本工程的现存资产。顺带把资产取回来，改写时不再查第二遍。
	resolved := make(map[string]Asset)
	var lookupErr error
	eachPlaceholder(string(content), func(at int, assetID string) {
		if lookupErr != nil {
			return
		}
		if _, ok := resolved[assetID]; ok {
			return
		}
		asset, err := s.store.GetAsset(ctx, projectID, assetID)
		switch {
		case err == nil:
			resolved[assetID] = asset
		case errors.Is(err, ErrAssetNotFound):
			// 指向不存在的、别的工程的、或已删除的资产，落到同一条结论上。
			problems = append(problems, locatedProblem{
				offset:  at,
				message: fmt.Sprintf("第 %d 行引用的资产 %q 不在本工程的资产库里", lineOf(content, at), PlaceholderScheme+assetID),
			})
		default:
			// 存储故障不是"正文有问题"，照原样上抛。
			lookupErr = err
		}
	})
	if lookupErr != nil {
		return "", Report{}, lookupErr
	}

	// 取资源的位置：只允许本工程的占位符。
	for _, ref := range scanResourceReferences(string(content)) {
		if _, ok := placeholderID(ref.value); ok {
			continue
		}
		problems = append(problems, locatedProblem{
			offset: ref.offset,
			message: fmt.Sprintf("第 %d 行 %s 的资源引用 %q 必须指向本工程的资产（写成 %s<资产标识>）",
				lineOf(content, ref.offset), ref.where, ref.value, PlaceholderScheme),
		})
	}

	if len(problems) > 0 {
		return "", Report{Problems: sortedProblems(problems)}, nil
	}

	artifact, err := s.rewrite(content, resolved)
	if err != nil {
		return "", Report{}, err
	}
	if err := CheckArtifactSize(artifact); err != nil {
		return "", Report{Problems: []Problem{{Message: err.Error()}}}, nil
	}
	return artifact, Report{}, nil
}

// rewrite 把正文里的占位符换成公开区地址。
//
// 地址从 PublicOrigin 派生——与内容安全策略里的允许来源是同一个配置值的两处
// 用法（见 public_origin.go）。
func (s *Service) rewrite(content Document, resolved map[string]Asset) (Document, error) {
	return RewritePlaceholders(content, func(assetID string) (string, error) {
		asset, ok := resolved[assetID]
		if !ok {
			// 校验已经保证每个占位符都解析得到；走到这里说明有代码绕过校验
			// 直接调用了改写。
			return "", fmt.Errorf("%w: %s", ErrAssetNotFound, assetID)
		}
		return s.origin.AssetURL(asset.Digest), nil
	})
}

// sortedProblems 把问题按在正文里出现的先后排序，并去掉位置。
func sortedProblems(problems []locatedProblem) []Problem {
	sort.SliceStable(problems, func(i, j int) bool { return problems[i].offset < problems[j].offset })
	out := make([]Problem, 0, len(problems))
	for _, problem := range problems {
		out = append(out, Problem{Message: problem.message})
	}
	return out
}

// lineOf 返回一个偏移量所在的行号（从 1 开始）。
func lineOf(content Document, offset int) int {
	if offset > len(content) {
		offset = len(content)
	}
	return 1 + strings.Count(string(content[:offset]), "\n")
}

// placeholderID 判定一个取值是不是**恰好一个**占位符，是则返回资产标识。
//
// 前后空白容忍：`src=" asset://abc "` 与 `src="asset://abc"` 是同一个引用。
// 除此之外多一个字符都不算——这正是"未改写的 asset:// 必须显式失败"那条的
// 实现：`asset://abc/extra` 不是一个占位符，因此它会被当成一处外部资源引用报
// 出来，而不是被静默改写掉前半截。
func placeholderID(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, PlaceholderScheme) {
		return "", false
	}
	rest := trimmed[len(PlaceholderScheme):]
	if rest == "" {
		return "", false
	}
	for i := 0; i < len(rest); i++ {
		if !isAssetIDByte(rest[i]) {
			return "", false
		}
	}
	return rest, true
}

// resourceReference 是正文里一处"取资源"的位置。
type resourceReference struct {
	offset int
	// where 是位置描述（如 "img 的 src"），出现在给用户的错误里。
	where string
	value string
}

// scanResourceReferences 找出正文里所有取资源的位置。
//
// **这是尽力而为的扫描，不是安全边界。** 枚举 HTML 里"取资源"的写法不可能
// 穷尽——属性会新增、特性会演进，任何一份枚举清单都在它写完的那天开始过期。
// 把边界建在枚举上，等于承诺一件做不到的事。它存在的目的是**给用户一条可
// 操作的错误**："第 N 处引用指向了外部地址"。真正的边界是交付时附加的内容
// 安全策略响应头（见 csp.go）：扫描漏掉的写法仍然取不到东西。
//
// 两类写法必须分开（这是设计里最容易搞混的一处）：
//
//   - **取资源的引用**（`<img src>`、CSS `url()`、`srcset` …）：只能是本工程
//     资产的占位符（发布物不得从本工程资产库之外取任何一个字节）。
//   - **导航链接**（`<a href="https://example.com">`）：可以是任意地址。用户
//     链到外部网站是他的意图，不是资源引用。
func scanResourceReferences(content string) []resourceReference {
	var refs []resourceReference
	for i := 0; i < len(content); {
		lt := strings.IndexByte(content[i:], '<')
		if lt < 0 {
			break
		}
		start := i + lt
		switch {
		case strings.HasPrefix(content[start:], "<!--"):
			// 注释里的东西不会被浏览器取用。
			end := strings.Index(content[start+4:], "-->")
			if end < 0 {
				return refs
			}
			i = start + 4 + end + 3
			continue
		case strings.HasPrefix(content[start:], "<!"), strings.HasPrefix(content[start:], "<?"):
			end := strings.IndexByte(content[start:], '>')
			if end < 0 {
				return refs
			}
			i = start + end + 1
			continue
		}
		tagEnd := tagEnd(content, start)
		if tagEnd < 0 {
			return refs
		}
		nameEnd := start + 1
		closing := false
		if nameEnd < len(content) && content[nameEnd] == '/' {
			closing = true
			nameEnd++
		}
		nameStop := nameEnd
		for nameStop < len(content) && isTagNameByte(content[nameStop]) {
			nameStop++
		}
		tag := strings.ToLower(content[nameEnd:nameStop])
		if closing {
			i = tagEnd + 1
			continue
		}
		// 开始标签本身的属性是资源位置；而**体**要分开处理：
		//
		//   - 脚本体里可能出现任何文本，包括长得像标签的字符串，因此整段跳过；
		//     但 `<script src>` 是取资源的位置，所以属性照常解析。
		//   - 样式体是 CSS，由专门的扫描负责；跳过它，免得把 CSS 里的 `<`
		//     当成一个标签的开头。
		refs = append(refs, attributeReferences(content, tag, nameStop, tagEnd)...)
		switch tag {
		case "script":
			if body := intsIndex(content, tagEnd+1, "</script"); body >= 0 {
				i = body + len("</script>")
				continue
			}
		case "style":
			if body := intsIndex(content, tagEnd+1, "</style"); body >= 0 {
				refs = append(refs, scanCSS(content[tagEnd+1:body], tagEnd+1)...)
				i = body + len("</style>")
				continue
			}
		}
		i = tagEnd + 1
	}
	return refs
}

// attributeReferences 返回一个开始标签内部的资源引用。
func attributeReferences(content, tag string, attrsFrom, tagEnd int) []resourceReference {
	refs := make([]resourceReference, 0, 2)
	for _, attr := range parseAttributes(content[attrsFrom:tagEnd], attrsFrom) {
		name := strings.ToLower(attr.name)
		switch {
		case name == "style":
			refs = append(refs, scanCSS(attr.value, attr.valueOffset)...)
		case name == "srcset":
			if tag != "img" && tag != "source" {
				continue
			}
			for _, candidate := range splitSrcset(attr.value) {
				if candidate == "" {
					continue
				}
				// srcset 的候选是 `地址 描述符`，只取地址那一截。
				address := candidate
				if space := strings.IndexAny(candidate, " \t\n"); space >= 0 {
					address = candidate[:space]
				}
				refs = append(refs, resourceReference{
					offset: attr.valueOffset,
					where:  tag + " 的 srcset",
					value:  address,
				})
			}
		case isResourceAttribute(tag, name):
			if strings.TrimSpace(attr.value) == "" {
				// 空取值不取任何资源（浏览器把它当成"没有这个属性"或解析成
				// 页面自身，那一次请求被内容安全策略挡在 img-src 之外）。
				continue
			}
			refs = append(refs, resourceReference{
				offset: attr.valueOffset,
				where:  tag + " 的 " + name,
				value:  strings.TrimSpace(attr.value),
			})
		}
	}
	return refs
}

// isResourceAttribute 判定一个属性是不是"取资源"的位置。
//
// 与导航链接的分界就在这张表上：`href` 只有落在 <link> 上才是取资源，落在
// <a>、<area> 上是用户写的导航链接。
func isResourceAttribute(tag, attr string) bool {
	switch attr {
	case "src", "poster", "background":
		return true
	case "data":
		return tag == "object"
	case "href":
		return tag == "link"
	default:
		return false
	}
}

// splitSrcset 把 srcset 拆成候选。
//
// 按逗号拆是够用的近似：真实的 srcset 里逗号只能出现在地址（data: URL 里会
// 有），而 data: 形式的引用本来就在禁止之列——它会被当成一处外部引用报出来。
func splitSrcset(value string) []string {
	parts := strings.Split(value, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// htmlAttr 是一个已解析的属性，valueOffset 用于把问题定位回正文。
type htmlAttr struct {
	name        string
	value       string
	valueOffset int
}

// parseAttributes 解析一段标签内部的属性文本。
//
// 它不做完整的 HTML 解析：不处理实体、不校验属性名。它要回答的问题只有一个
// ——"这段文本里有没有取资源的属性，取值是什么、在哪"。
func parseAttributes(text string, base int) []htmlAttr {
	var attrs []htmlAttr
	i := 0
	for i < len(text) {
		for i < len(text) && isSpaceByte(text[i]) {
			i++
		}
		if i >= len(text) {
			break
		}
		nameStart := i
		for i < len(text) && !isSpaceByte(text[i]) && text[i] != '=' && text[i] != '/' {
			i++
		}
		name := text[nameStart:i]
		for i < len(text) && isSpaceByte(text[i]) {
			i++
		}
		if i >= len(text) || text[i] != '=' {
			if name != "" {
				attrs = append(attrs, htmlAttr{name: name})
			}
			continue
		}
		i++ // '='
		for i < len(text) && isSpaceByte(text[i]) {
			i++
		}
		if i >= len(text) {
			break
		}
		var value string
		var valueOffset int
		if text[i] == '"' || text[i] == '\'' {
			quote := text[i]
			i++
			valueOffset = base + i
			valueStart := i
			for i < len(text) && text[i] != quote {
				i++
			}
			value = text[valueStart:i]
			if i < len(text) {
				i++
			}
		} else {
			valueOffset = base + i
			valueStart := i
			for i < len(text) && !isSpaceByte(text[i]) {
				i++
			}
			value = text[valueStart:i]
		}
		attrs = append(attrs, htmlAttr{name: name, value: value, valueOffset: valueOffset})
	}
	return attrs
}

// scanCSS 找出一段 CSS 里所有取资源的位置：url(...) 与 @import 后面的字符串。
//
// 覆盖内联样式属性与 <style> 块两处：两者的写法是同一种语言，因此用同一段
// 代码扫，不各写一份。
func scanCSS(css string, base int) []resourceReference {
	var refs []resourceReference
	lower := strings.ToLower(css)
	for i := 0; i < len(css); {
		switch {
		case strings.HasPrefix(lower[i:], "url("):
			open := i + len("url(")
			close := strings.IndexByte(css[open:], ')')
			if close < 0 {
				return refs
			}
			raw := css[open : open+close]
			refs = appendRef(refs, raw, base+open, "样式里的 url()")
			i = open + close + 1
		case strings.HasPrefix(lower[i:], "@import"):
			j := i + len("@import")
			for j < len(css) && isSpaceByte(css[j]) {
				j++
			}
			if j < len(css) && (css[j] == '"' || css[j] == '\'') {
				quote := css[j]
				valueStart := j + 1
				close := strings.IndexByte(css[valueStart:], quote)
				if close < 0 {
					return refs
				}
				refs = appendRef(refs, css[valueStart:valueStart+close], base+valueStart, "@import")
				i = valueStart + close + 1
				continue
			}
			// @import url(...) 会落在上面那个分支里。
			i = j
		default:
			i++
		}
	}
	return refs
}

func appendRef(refs []resourceReference, raw string, offset int, where string) []resourceReference {
	value := strings.TrimSpace(raw)
	value = strings.Trim(value, `"'`)
	value = strings.TrimSpace(value)
	if value == "" {
		return refs
	}
	return append(refs, resourceReference{offset: offset, where: where, value: value})
}

// tagEnd 返回一个标签结束的 '>' 位置，跳过引号内的 '>'。
func tagEnd(content string, start int) int {
	quote := byte(0)
	for i := start + 1; i < len(content); i++ {
		b := content[i]
		if quote != 0 {
			if b == quote {
				quote = 0
			}
			continue
		}
		switch b {
		case '"', '\'':
			quote = b
		case '>':
			return i
		}
	}
	return -1
}

// intsIndex 是大小写不敏感的 strings.Index。
func intsIndex(haystack string, from int, needle string) int {
	idx := strings.Index(strings.ToLower(haystack[from:]), strings.ToLower(needle))
	if idx < 0 {
		return -1
	}
	return from + idx
}

func isTagNameByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '-' || b == ':' || b == '_':
		return true
	default:
		return false
	}
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f'
}
