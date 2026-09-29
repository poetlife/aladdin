package galaxy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/poetlife/aladdin/internal/objectstore"
)

// Problem 是校验发现的一处问题。
//
// 消息里**带上出问题的文件与位置**：只说"有引用不合法"会让用户在一组文件里
// 自己找。
type Problem struct {
	Message string
	// Path 是出问题的文件在文件组里的路径。与具体文件无关时为空。
	Path string
	// Line 是行号（从 1 开始）。0 表示与行无关。
	Line int
}

// Report 是一份内容能不能发布的结论。问题为空表示它可以发布。
type Report struct {
	Problems []Problem
}

// OK 表示这份内容可以发布。
func (r Report) OK() bool { return len(r.Problems) == 0 }

// Messages 返回全部问题消息，供把结论转成一句话的调用方使用。
func (r Report) Messages() []string {
	messages := make([]string, 0, len(r.Problems))
	for _, problem := range r.Problems {
		messages = append(messages, problem.Describe())
	}
	return messages
}

// Describe 把一处问题写成一句可读的话。
func (p Problem) Describe() string {
	switch {
	case p.Path != "" && p.Line > 0:
		return fmt.Sprintf("%s:%d %s", p.Path, p.Line, p.Message)
	case p.Path != "":
		return fmt.Sprintf("%s %s", p.Path, p.Message)
	default:
		return p.Message
	}
}

// ValidateDraft 校验当前草稿能不能发布（唯一入口）。
//
// 界面需要即时提示时调用的是它，发布的前置阶段调用的也是它——**同一段代码、
// 同一份规则集合**。两端各写一份的表现有两种，都很难排查：提示说没问题而发布
// 说不行，用户被卡住，且没有任何提示告诉他哪一句是真的；或者反过来，用户放弃
// 一个其实能用的功能。
//
// 返回的是一个**问题清单**而不是单个错误：界面要的是"哪几处有问题"。存储
// 不可用这类故障仍然以 error 返回，与"内容有问题"分开——混在一起会让一次
// 数据库抖动表现成"你的内容写错了"。
func (s *Service) ValidateDraft(ctx context.Context, subjectID, projectID string, slot ContentSlot) (Report, error) {
	project, err := s.ownedProjectSlot(ctx, subjectID, projectID, slot)
	if err != nil {
		return Report{}, err
	}
	draft, err := s.store.GetDraft(ctx, projectID, slot)
	if errors.Is(err, ErrDraftNotFound) {
		draft = Draft{ProjectID: projectID, Slot: slot}
	} else if err != nil {
		return Report{}, err
	}
	_, report, err := s.buildArtifacts(ctx, project, slot, draft.Manifest, false)
	return report, err
}

// buildArtifacts 是校验规则集合的实现，并在通过时给出**改写好的产物**。
//
// 规则全集（见 docs/design/galaxy/publication.md）：
//
//  1. 入口文件存在；
//  2. 每一条资产条目都指向本工程现存的一个资产；
//  3. 每一处取字节的引用都落在本文件组的**某一条条目**上（文本或资产），且
//     不接受任何指向文件组之外的资源引用（导航链接不受此限）；
//  4. `docs` 槽下每一处文档间链接都落在文件组里；
//  5. 单份文本、整组文本与文件数都不超上限。
//
// 产物在这里算出来而不是在发布阶段另算一次：改写与渲染是纯计算，而"产物会不会
// 超限"是校验的一部分——把它留到落库前才发现，会让一次被拒的发布先产生上架
// 副作用。
//
// 返回的产物是「产物路径 → 字节」。**它就是发布要写进内容对象的东西**，因此
// 校验通过之后发布不必再算一遍。
//
// tolerant 为真时是**预览要的那一档**：产物照给，坏引用原样留着（见
// buildPreviewArtifacts）。两档共用这一处实现，是因为"一处引用落在哪一条条目上"
// 只有一个判断入口——两处各写一份的表现是"预览说这处没问题、发布说不行"。
func (s *Service) buildArtifacts(ctx context.Context, project Project, slot ContentSlot, manifest Manifest, tolerant bool) (map[string][]byte, Report, error) {
	var problems []Problem

	if _, ok := manifest.Find(slot.EntryPath()); !ok {
		problems = append(problems, Problem{
			Message: fmt.Sprintf("缺少入口文件 %s", slot.EntryPath()),
		})
	}

	// 资产条目：每一条都要指向本工程现存的一个资产。"不属于本工程"与"不存在"
	// 落到同一条结论上（见 asset.go 的 assetOfProject）。
	for _, entry := range manifest.Assets() {
		if _, err := s.assetOfProject(ctx, project.ID, entry.AssetID); err != nil {
			if errors.Is(err, ErrAssetNotFound) {
				problems = append(problems, Problem{
					Path:    entry.Path,
					Message: fmt.Sprintf("引用的资产 %s 不在本工程的资产库里", entry.AssetID),
				})
				continue
			}
			return nil, Report{}, err
		}
	}

	textBytes, sizeProblems, err := s.loadTextEntries(ctx, project.ID, manifest)
	if err != nil {
		return nil, Report{}, err
	}
	problems = append(problems, sizeProblems...)

	siteRoot := s.origin.SiteRoot(project.ID, slot)
	artifacts := make(map[string][]byte, len(manifest))

	// 引用解析器按模式二选一：发布与校验要"坏引用即失败"，预览要"坏引用原样
	// 留着"。两者共用同一处"引用落在哪一条条目上"的判断（见 link_resolver.go），
	// 地址也同形——预览根那次替换在交付那一步逐字完成。
	var linker LinkResolver = SiteLinker{Slot: slot, Manifest: manifest, SiteRoot: siteRoot}
	if tolerant {
		linker = PreviewLinker{Site: SiteLinker{Slot: slot, Manifest: manifest, SiteRoot: siteRoot}}
	}

	// markdown 需要先把整组渲染出来才能谈导航与文档间链接，因此分两趟：
	// 第一趟只渲染 markdown（逐份，好把问题定位到具体文件），第二趟拼产物。
	var docs []Doc
	if slot == SlotDocs {
		for _, entry := range manifest {
			if !IsMarkdownPath(entry.Path) {
				continue
			}
			source, ok := textBytes[entry.Path]
			if !ok {
				continue // 读不到字节已由 loadTextEntries 报过
			}
			doc, err := RenderDoc(DocSource{Path: entry.Path, Body: source}, linker)
			if err != nil {
				problems = append(problems, Problem{Path: entry.Path, Message: err.Error()})
				continue
			}
			docs = append(docs, doc)
		}
	}

	if !tolerant && len(problems) > 0 {
		return nil, Report{Problems: sortedProblems(problems)}, nil
	}

	// 第二趟：拼产物。
	if slot == SlotDocs && len(docs) > 0 {
		for artifactPath, data := range RenderDocsArtifacts(docs, siteRoot) {
			artifacts[artifactPath] = data
		}
	}
	for _, entry := range manifest {
		if slot == SlotDocs && IsMarkdownPath(entry.Path) {
			continue // 已由渲染产出
		}
		source, ok := textBytes[entry.Path]
		if !ok {
			continue
		}
		artifactPath := ArtifactPath(slot, entry.Path)

		// 记号替换对**每一份文本**生效（HTML、CSS、站点文件一视同仁）。
		substituted, err := SubstituteAssetMarkers(source, func(assetID string) (string, error) {
			asset, found := assetEntryByID(manifest, assetID)
			if !found {
				return "", fmt.Errorf("引用的资产 %q 不在本文件组里", PlaceholderScheme+assetID)
			}
			return siteRoot + asset.Path, nil
		})
		if err != nil {
			problems = append(problems, Problem{Path: entry.Path, Message: err.Error()})
			continue
		}

		// 取资源的引用：每一处都必须落在本文件组的一条条目上。
		problems = append(problems, checkResourceReferences(siteRoot, manifest, artifactPath, substituted)...)
		artifacts[artifactPath] = substituted
	}

	if !tolerant && len(problems) > 0 {
		return nil, Report{Problems: sortedProblems(problems)}, nil
	}

	// 产物的总量与文件数上限。**它是发布与校验的责任，不是渲染的责任**：预览
	// 照给产物——超限这件事由校验入口单独指出（见 authoring.md 的"预览不做审查"）。
	if !tolerant {
		var total int
		for _, data := range artifacts {
			total += len(data)
		}
		if total > MaxFileSetBytes {
			return nil, Report{Problems: []Problem{{
				Message: fmt.Sprintf("产物共 %d 字节，超过上限 %d 字节", total, MaxFileSetBytes),
			}}}, nil
		}
		if len(artifacts) > MaxFiles {
			return nil, Report{Problems: []Problem{{
				Message: fmt.Sprintf("产物共 %d 个文件，超过上限 %d 个", len(artifacts), MaxFiles),
			}}}, nil
		}
	}
	return artifacts, Report{Problems: sortedProblems(problems)}, nil
}

// buildPreviewArtifacts 是预览要的那一档产物：**有问题也给产物**，坏引用原样留着，
// 文档间链接的解析也不因坏引用而失败。
//
// 它与发布/校验共用同一段渲染、同一处"引用落在哪一条条目上"的判断，差别只有一条：
// 坏引用要不要中止。这一条差别是刻意的——预览是"看看现在长什么样"，把它做成一个
// 会因为一处坏引用而整体拒绝的东西，用户就看不到修复它之后剩下的部分（见
// docs/design/galaxy/authoring.md）。
func (s *Service) buildPreviewArtifacts(ctx context.Context, project Project, slot ContentSlot, manifest Manifest) (map[string][]byte, error) {
	artifacts, _, err := s.buildArtifacts(ctx, project, slot, manifest, true)
	return artifacts, err
}

// loadTextEntries 读出清单里每一条文本条目的字节，并核对体积上限。
//
// **它是"这份内容的文本字节"的唯一读取入口**：校验与发布都从它拿字节，因此
// "读不到对象"这件事在两处的表现一致——它是一处内容问题（对象不存在），不是
// 一次存储故障，因此以问题清单返回而不是 error。
func (s *Service) loadTextEntries(ctx context.Context, projectID string, manifest Manifest) (map[string][]byte, []Problem, error) {
	if s.assets == nil {
		return nil, nil, ErrAssetUnavailable
	}
	bytesByPath := make(map[string][]byte, len(manifest))
	var problems []Problem
	var total int
	for _, entry := range manifest {
		if entry.Kind != EntryKindText {
			continue
		}
		data, err := s.assets.Read(ctx, ContentObjectKey(projectID, entry.Digest))
		if err != nil {
			if errors.Is(err, objectstore.ErrObjectNotFound) {
				problems = append(problems, Problem{
					Path:    entry.Path,
					Message: "这一条的内容对象不存在，内容可能没有推送完整",
				})
				continue
			}
			return nil, nil, err
		}
		if len(data) > MaxTextBytes {
			problems = append(problems, Problem{
				Path:    entry.Path,
				Message: fmt.Sprintf("%d 字节，超过单份上限 %d 字节", len(data), MaxTextBytes),
			})
			continue
		}
		total += len(data)
		bytesByPath[entry.Path] = data
	}
	if total > MaxFileSetBytes {
		problems = append(problems, Problem{
			Message: fmt.Sprintf("整组内容共 %d 字节，超过上限 %d 字节", total, MaxFileSetBytes),
		})
	}
	return bytesByPath, problems, nil
}

// checkResourceReferences 扫一遍一段非 markdown 文本里的取资源引用，并把不落在
// 本文件组里的那些报成问题。
//
// **这是尽力而为的扫描，不是安全边界。** 枚举"取资源"的写法不可能穷尽——属性
// 会新增、特性会演进，任何一份枚举清单都在它写完的那天开始过期。把边界建在枚举
// 上，等于承诺一件做不到的事。它存在的目的是**给用户一条可操作的错误**；真正的
// 边界是交付时附加的内容安全策略响应头（见 csp.go）：扫描漏掉的写法仍然取不到
// 东西。
//
// 两类写法必须分开（这是设计里最容易搞混的一处）：取资源的引用不得指向本文件
// 组之外；`<a href>` 一类导航链接可以是任意地址。
func checkResourceReferences(siteRoot string, manifest Manifest, entryPath string, content []byte) []Problem {
	refs := scanResourceReferences(string(content))
	if strings.EqualFold(pathExt(entryPath), ".css") {
		refs = append(refs, scanCSS(string(content), 0)...)
	}
	var problems []Problem
	for _, ref := range refs {
		if assetID, ok := placeholderID(ref.value); ok {
			// 记号必须落在一条资产条目上（SubstituteAssetMarkers 已经替它
			// 报过一次错，这里是为了让引用完整性的判断自成一体）。
			if _, found := assetEntryByID(manifest, assetID); !found {
				problems = append(problems, Problem{
					Path:    entryPath,
					Line:    lineOf(content, ref.offset),
					Message: fmt.Sprintf("%s 引用的资产 %s 不在本文件组里", ref.where, PlaceholderScheme+assetID),
				})
			}
			continue
		}
		if isExternalDestination(ref.value) {
			problems = append(problems, Problem{
				Path:    entryPath,
				Line:    lineOf(content, ref.offset),
				Message: fmt.Sprintf("%s 的资源引用 %q 指向了本文件组之外；发布物不得从别处取任何字节", ref.where, ref.value),
			})
			continue
		}
		resolved, ok := resolveEntryPath(siteRoot, entryPath, ref.value)
		if !ok || !manifest.HasPath(resolved) {
			problems = append(problems, Problem{
				Path:    entryPath,
				Line:    lineOf(content, ref.offset),
				Message: fmt.Sprintf("%s 的资源引用 %q 不在本文件组里", ref.where, ref.value),
			})
		}
	}
	return problems
}

// sortedProblems 按（路径，行号，消息）排序，让同样的输入得到同样的结论。
func sortedProblems(problems []Problem) []Problem {
	sort.SliceStable(problems, func(i, j int) bool {
		if problems[i].Path != problems[j].Path {
			return problems[i].Path < problems[j].Path
		}
		if problems[i].Line != problems[j].Line {
			return problems[i].Line < problems[j].Line
		}
		return problems[i].Message < problems[j].Message
	})
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
// **这是尽力而为的扫描，不是安全边界**（理由见 checkResourceReferences）。
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
