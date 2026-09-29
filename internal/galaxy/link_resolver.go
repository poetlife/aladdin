package galaxy

import (
	"errors"
	"fmt"
)

// 两个 LinkResolver 实现：发布态给站点绝对地址，预览态给文内锚点与短时地址。
//
// 它们共用**同一处"引用落在文件组的哪一条条目上"的判断**（resolveEntryPath +
// Manifest.HasPath），差别只在最后的地址长什么样。把判断写两遍的表现是"预览
// 说这处没问题、发布说不行"。

// SiteLinker 把引用解析成**发布态**的站点内绝对地址。
//
// 它不产出公开区地址（那按内容摘要寻址、只在重定向那一刻给出），也不产出绝对
// URL——产物里写的是站点内的路径，因此构建产物一个字节都不用改，工程标识也
// 只出现在发布根一处。
type SiteLinker struct {
	// Form 是工程形态，决定 markdown 源被渲染成 `.html`。
	Form SiteForm
	// Manifest 是该版本的文件组。
	Manifest Manifest
	// SiteRoot 是发布根路径，形如 `/g/<工程标识>/`。
	SiteRoot string
}

// Resolve 实现 LinkResolver。
func (l SiteLinker) Resolve(from, dest string, isResource bool) (string, error) {
	entry, err := l.entryFor(from, dest, isResource)
	if errors.Is(err, errNavigationLink) {
		return dest, nil
	}
	if err != nil {
		return "", err
	}
	return l.SiteRoot + ArtifactPath(l.Form, entry.Path), nil
}

// entryFor 把一处引用解析成文件组里的条目（唯一入口）。
//
// 导航链接（外部地址）是一个特例：它**不落在任何条目上**，因此返回
// errNavigationLink，由调用方原样保留这处地址。
func (l SiteLinker) entryFor(from, dest string, isResource bool) (Entry, error) {
	// 记号：必须是本文件组里的一条**资产条目**。
	if assetID, ok := placeholderID(dest); ok {
		entry, found := assetEntryByID(l.Manifest, assetID)
		if !found {
			return Entry{}, fmt.Errorf("引用的资产 %q 不在本文件组里（%s）",
				PlaceholderScheme+assetID, from)
		}
		return entry, nil
	}
	if isExternalDestination(dest) {
		if isResource {
			return Entry{}, errExternalResource(from, dest)
		}
		// 导航链接可以是任意地址：用户链到外部网站是他的意图，不是资源引用。
		return Entry{}, errNavigationLink
	}
	entryPath, ok := resolveEntryPath(l.SiteRoot, from, dest)
	if !ok {
		return Entry{}, fmt.Errorf("%s 里的引用 %q 不是一处可用的站点内位置", from, dest)
	}
	entry, found := l.Manifest.Find(entryPath)
	if !found {
		return Entry{}, fmt.Errorf("%s 引用了文件组里不存在的位置 %q", from, entryPath)
	}
	return entry, nil
}

// errNavigationLink 表示"这处引用不是一处站点内位置，但它是合法的导航链接"。
//
// 它是一条**控制流信号，不是错误**：调用方据此原样保留这处地址。用一个哨兵
// 而不是第二个返回值，是因为解析入口只有一个返回形状（见 LinkResolver）。
var errNavigationLink = fmt.Errorf("导航链接")

// PreviewLinker 把引用解析成**预览态**的地址。
//
// 预览落在不透明源的沙箱 iframe 上，站内引用解析不到（沙箱文档没有自己的源）。
// 因此 docs 形态把文档间链接变成**文内锚点**（整站被拼成一份），而其余条目
// 指向编辑态的短时地址。
//
// **预览不做审查、不做裁剪**：解析不出来的引用原样留着（显示成坏的），而"这处
// 有问题"由校验入口单独给出。把两者混起来会让用户以为预览看起来对就等于发布
// 能成功。
type PreviewLinker struct {
	Form     SiteForm
	Manifest Manifest
	// Docs 是已渲染的文档页，用来把文档间链接变成锚点。
	Docs []Doc
	// URLOf 给出一个条目在编辑态的短时地址。
	URLOf func(Entry) (string, error)
}

// Resolve 实现 LinkResolver。**它不返回错误**：预览容忍坏引用。
func (l PreviewLinker) Resolve(from, dest string, isResource bool) (string, error) {
	if assetID, ok := placeholderID(dest); ok {
		entry, found := assetEntryByID(l.Manifest, assetID)
		if !found {
			return dest, nil
		}
		return l.urlOrDest(entry, dest), nil
	}
	if isExternalDestination(dest) {
		return dest, nil
	}
	entryPath, ok := resolveEntryPath("", from, dest)
	if !ok {
		return dest, nil
	}
	entry, found := l.Manifest.Find(entryPath)
	if !found {
		return dest, nil
	}
	if l.Form == SiteFormDocs && IsMarkdownPath(entryPath) {
		if anchor, ok := PreviewAnchorFor(l.Docs, entryPath); ok {
			return anchor, nil
		}
	}
	return l.urlOrDest(entry, dest), nil
}

// urlOrDest 取一个条目的短时地址，取不到时保留原引用。
func (l PreviewLinker) urlOrDest(entry Entry, dest string) string {
	if l.URLOf == nil {
		return dest
	}
	url, err := l.URLOf(entry)
	if err != nil || url == "" {
		return dest
	}
	return url
}
