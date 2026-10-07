package galaxy

import (
	"fmt"
	"strings"
)

// 产物复核：引用完整性的判据落在**产物**上（唯一入口）。
//
// 源回答"准备发布什么"，产物回答"实际发布出去什么"：`site` 槽两者逐字相同，
// `docs` 槽的产物是渲染出来的那一份——记号的解开、文档间链接的改写都发生在那一趟
// 里。因此判据只能是"产物里的每一处取资源引用都落在产物清单上、且产物里不残留
// 记号"：源里那些记号本来就该是记号，而产物里再出现一个就说明有一处替换被漏掉了
// （见 docs/design/galaxy/publication.md 的"校验规则"）。
//
// **这是尽力而为的扫描，不是安全边界。** 枚举"取资源"的写法不可能穷尽——属性
// 会新增、特性会演进，任何一份枚举清单都在它写完的那天开始过期。把边界建在枚举
// 上，等于承诺一件做不到的事。它存在的目的是**给用户一条可操作的错误**；真正的
// 边界是交付时附加的内容安全策略响应头（见 csp.go）：扫描漏掉的写法仍然取不到
// 东西。
//
// 两类写法必须分开（这是设计里最容易搞混的一处）：取资源的引用不得指向本文件组
// 之外；`<a href>` 一类导航链接可以是任意地址。

// AuditArtifacts 复核一组产物里的取资源引用（唯一入口）。
//
// validPaths 是这批产物在发布态里**可以命中的路径集合**（产物清单里的全部路径：
// 渲染出来的每一页、原样带上的站点文件与资产条目）。artifacts 是「产物路径 → 字节」。
//
// 它同时是发布前置校验与**回读发布态之后**的复核入口：两处判的是同一件事，只是
// 一份产物是刚算出来的、另一份是从发布态取回来的（见 cmd/aladdin 的
// `galaxy publication verify`）。
func AuditArtifacts(siteRoot string, validPaths map[string]bool, artifacts map[string][]byte) Report {
	var problems []Problem
	for artifactPath, content := range artifacts {
		problems = append(problems, auditArtifact(siteRoot, validPaths, artifactPath, content)...)
	}
	return Report{Problems: sortedProblems(problems)}
}

// auditArtifact 复核一份产物：每一处取资源引用都要落在 validPaths 上，且不得
// 残留没解开的记号。
func auditArtifact(siteRoot string, validPaths map[string]bool, artifactPath string, content []byte) []Problem {
	refs := scanResourceReferences(string(content))
	if strings.EqualFold(pathExt(artifactPath), ".css") {
		refs = append(refs, scanCSS(string(content), 0)...)
	}
	var problems []Problem
	for _, ref := range refs {
		line := lineOf(content, ref.offset)
		if _, isMarker := placeholderID(ref.value); isMarker {
			// **产物里的记号本该已经被解开。** 走到这里说明有一处替换被漏掉了
			// ——那是"漏掉解析必须显式失败"这条设计的落点，它该在校验里被拒绝，
			// 而不是留到读者的浏览器控制台里。
			problems = append(problems, Problem{
				Path: artifactPath,
				Line: line,
				Message: fmt.Sprintf("%s 的记号 %q 没有被解开；发布物里不得出现未解析的记号",
					ref.where, ref.value),
			})
			continue
		}
		if isExternalDestination(ref.value) {
			problems = append(problems, Problem{
				Path: artifactPath,
				Line: line,
				Message: fmt.Sprintf("%s 的资源引用 %q 指向了本文件组之外；发布物不得从别处取任何字节",
					ref.where, ref.value),
			})
			continue
		}
		resolved, ok := resolveEntryPath(siteRoot, artifactPath, ref.value)
		if !ok || !validPaths[resolved] {
			problems = append(problems, Problem{
				Path:    artifactPath,
				Line:    line,
				Message: fmt.Sprintf("%s 的资源引用 %q 不在本文件组里", ref.where, ref.value),
			})
		}
	}
	return problems
}

// lineOf 返回一个偏移量所在的行号（从 1 开始）。
func lineOf(content []byte, offset int) int {
	if offset > len(content) {
		offset = len(content)
	}
	if offset < 0 {
		offset = 0
	}
	return 1 + strings.Count(string(content[:offset]), "\n")
}

// pathExt 返回路径的扩展名（小写）。
func pathExt(entryPath string) string {
	if dot := strings.LastIndexByte(entryPath, '.'); dot >= 0 && dot > strings.LastIndexByte(entryPath, '/') {
		return strings.ToLower(entryPath[dot:])
	}
	return ""
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
// **这是尽力而为的扫描，不是安全边界**（理由见本文件开头的说明）。
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
				continue
			}
			// 走到这里说明这个字节既不构成属性名、也不是 `=`——自闭合标签末尾
			// 那个 `/` 就是这种。**必须推进**：名字为空时上面那个 `continue`
			// 不消耗任何字节，一个 `<meta ... />` 就足以让属性解析原地打转，
			// 进而让校验永远转不完（服务端一个核被打满）。
			i++
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
