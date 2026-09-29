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
	// Slot 是内容槽，决定 markdown 源被渲染成 `.html`。
	Slot ContentSlot
	// Manifest 是该版本的文件组。
	Manifest Manifest
	// SiteRoot 是**该槽的**发布根路径，形如 `/g/<工程标识>/`（docs 槽到
	// `docs/`）。
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
	return l.SiteRoot + ArtifactPath(l.Slot, entry.Path), nil
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

// PreviewLinker 把引用解析成**预览态**的站点内绝对地址。
//
// 它与 SiteLinker **同形**（都给出 `<站点根>/<产物路径>`），因为预览的页面也在
// 一个真实的地址空间里——预览通道把草稿整组按发布的路径形状给出，于是页内引用
// 由浏览器自己解析（见 docs/design/galaxy/site-model.md 的"预览"）。两处只在
// 最后一步分道：交付那一步把**发布根逐字换成预览根**，因此这里算出来的仍然是
// 发布根下的地址。
//
// 与 SiteLinker 的差别只有一条：**它不因坏引用而失败**。预览不做审查、不做裁剪，
// 解析不出来的引用原样留着（显示成坏的），"这处有问题"由校验入口单独给出。把两者
// 混起来会让用户以为预览看起来对就等于发布能成功。
type PreviewLinker struct {
	// Site 是发布态那套解析：预览与发布共用**同一处**"引用落在哪一条条目上"的
	// 判断，差别只有"坏引用要不要中止"这一条。因此这里持有一个 SiteLinker，而
	// 不是把它的字段再抄一遍。
	Site SiteLinker
}

// Resolve 实现 LinkResolver。**它不返回错误**：预览容忍坏引用。
func (l PreviewLinker) Resolve(from, dest string, isResource bool) (string, error) {
	resolved, err := l.Site.Resolve(from, dest, isResource)
	if err != nil {
		// 导航链接本来就走这条路（SiteLinker 把外部地址原样返回），坏引用也一样：
		// 留着作者写的那个地址，让预览显示成坏的。
		return dest, nil
	}
	return resolved, nil
}
