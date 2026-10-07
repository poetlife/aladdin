package galaxy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
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
//  3. **产物**里每一处取字节的引用都落在产物清单的**某一条条目**上（文本或
//     资产），不接受任何指向文件组之外的资源引用（导航链接不受此限），也不接受
//     没解开的记号（见 artifact_audit.go）；
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

	// `docs` 槽下每一处 `#锚点` 都要落在**目标那一页的标题**上。判据就是刚刚
	// 渲染出来的那份标题标识（目录用的同一份），因此这一趟不需要第二次解析。
	if !tolerant && slot == SlotDocs && len(docs) > 0 {
		problems = append(problems, checkAnchorLinks(docs, siteRoot)...)
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
		artifacts[artifactPath] = substituted
	}

	if !tolerant && len(problems) > 0 {
		return nil, Report{Problems: sortedProblems(problems)}, nil
	}

	// 引用完整性复核在**产物**上做：源回答"准备发布什么"，产物回答"实际发布出去
	// 什么"（见 artifact_audit.go）。两档的差别只有"跑不跑"——预览不做审查。
	if !tolerant {
		problems = append(problems, AuditArtifacts(siteRoot, artifactPaths(artifacts, manifest), artifacts).Problems...)
		if len(problems) > 0 {
			return nil, Report{Problems: sortedProblems(problems)}, nil
		}
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

// artifactPaths 返回产物清单里**可以命中的全部路径**：渲染或改写出来的每一份
// 文本，加上原样进产物的资产条目。
//
// 它就是"发布出去的那一份集合"，与 writeArtifacts 落库的那条发布记录同一个形状
// ——一份是刚算出来的，一份在发布记录里。
func artifactPaths(artifacts map[string][]byte, manifest Manifest) map[string]bool {
	paths := make(map[string]bool, len(artifacts)+len(manifest))
	for artifactPath := range artifacts {
		paths[artifactPath] = true
	}
	for _, entry := range manifest.Assets() {
		paths[entry.Path] = true
	}
	return paths
}

// checkAnchorLinks 校验文档里每一处 `#锚点` 都落在目标那一页的标题上（唯一入口）。
//
// 判据是渲染这一趟自己算出来的标题标识（目录用的同一份），因此这里不需要再走
// 一遍语法树、也不需要回读渲染出来的 HTML——两处因此不可能漂移。**指不到即拒绝**：
// 锚点是同组、同一份文档内的引用，不涉及网络，与"引用的文件在不在文件组里"是同
// 一类判断（见 docs/design/galaxy/site-model.md 的"引用完整性"）。
//
// 指向**非 markdown 条目**的后缀不在此列：那里没有标题可对，无从校验。
func checkAnchorLinks(docs []Doc, siteRoot string) []Problem {
	anchorsBySource := make(map[string]map[string]bool, len(docs))
	for _, doc := range docs {
		anchors := make(map[string]bool, len(doc.Anchors))
		for _, anchor := range doc.Anchors {
			anchors[anchor] = true
		}
		anchorsBySource[doc.SourcePath] = anchors
	}

	var problems []Problem
	for _, doc := range docs {
		for _, link := range doc.Links {
			anchor, target, ok := anchorTarget(doc.SourcePath, link.Dest, siteRoot)
			if !ok {
				continue
			}
			anchors, known := anchorsBySource[target]
			if !known || anchors[anchor] {
				continue
			}
			problems = append(problems, Problem{
				Path:    doc.SourcePath,
				Line:    link.Line,
				Message: fmt.Sprintf("链接 %q 指向了 %s 里不存在的锚点 %q", link.Dest, target, anchor),
			})
		}
	}
	return problems
}

// anchorTarget 判定一处链接是不是"带锚点的站内导航"，是则给出锚点与目标页的源路径。
//
// 三种情形不算：没有 `#` 后缀的、锚点为空的（只有一个 `#`）、目标是外部地址的
// ——后者由那个站点自己回答，不是本文件组能判的事。
func anchorTarget(from, dest, siteRoot string) (anchor, target string, ok bool) {
	location, suffix := splitDestination(dest)
	// 后缀里可能先有查询串再有锚点（`doc.md?q=1#小节`），因此找的是 `#`，不是首字节。
	hash := strings.IndexByte(suffix, '#')
	if hash < 0 {
		return "", "", false
	}
	if anchor = suffix[hash+1:]; anchor == "" {
		return "", "", false
	}
	if location == "" {
		// `#小节`：**本页自己**。
		return anchor, from, true
	}
	if isExternalDestination(location) {
		return "", "", false
	}
	resolved, resolvedOK := resolveEntryPath(siteRoot, from, location)
	if !resolvedOK {
		return "", "", false
	}
	return anchor, resolved, true
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
//
// **它一次把键交下去读**（见 objectstore.Store.ReadMany）：逐条读在跨境链路上
// 是逐个往返，而这一步每次校验与预览都要走。两条条目指向同一份字节时是同一个
// 键，只读一次。
func (s *Service) loadTextEntries(ctx context.Context, projectID string, manifest Manifest) (map[string][]byte, []Problem, error) {
	if s.assets == nil {
		return nil, nil, ErrAssetUnavailable
	}
	keys := make([]string, 0, len(manifest))
	for _, entry := range manifest {
		if entry.Kind != EntryKindText {
			continue
		}
		keys = append(keys, ContentObjectKey(projectID, entry.Digest))
	}
	read, err := s.assets.ReadMany(ctx, uniqueStrings(keys))
	if err != nil {
		return nil, nil, err
	}

	// **按清单顺序回填**：问题清单的顺序因此只取决于清单本身，与读的先后无关
	// ——错在哪一份文件要能被稳定地复现。
	bytesByPath := make(map[string][]byte, len(manifest))
	var problems []Problem
	var total int
	for _, entry := range manifest {
		if entry.Kind != EntryKindText {
			continue
		}
		data, ok := read[ContentObjectKey(projectID, entry.Digest)]
		if !ok {
			problems = append(problems, Problem{
				Path:    entry.Path,
				Message: "这一条的内容对象不存在，内容可能没有推送完整",
			})
			continue
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

// uniqueStrings 按首次出现的顺序去重。
//
// 它只为请求数服务：同一个内容摘要被多条条目引用时（同一份字节出现在两个路径
// 上），逐条去读会把同一份字节来回传好几遍。
func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
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
