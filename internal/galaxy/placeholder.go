package galaxy

import (
	"fmt"
	"strings"
)

// PlaceholderScheme 是资产记号（源侧）的协议形状。
//
// 源里引用素材写的是 `asset://<资产标识>`，例如 `<img src="asset://a1b2c3">`、
// `![图](asset://a1b2c3)` 或 `<style>body{background:url(asset://a1b2c3)}</style>`。
//
// 三条理由，缺一条这个设计就不成立（见 docs/design/galaxy/authoring.md）：
//
//  1. **记号是一个可被逐字替换的文本。** 因此替换不需要解析 HTML——出现在元素
//     属性里、在 CSS `url()` 里、在 `srcset` 里，都只是把这段文本换掉。依赖
//     "枚举哪些属性算引用"的做法必然会漏，而逐字替换不会漏。
//  2. **编辑态与发布态的地址本来就不同**（短时预签名 vs 稳定公开地址）。把真实
//     地址写进源，等于让源里存着一个会过期或与发布态不一致的字符串。
//  3. **"这个引用是不是本工程的资产"成了一个可判定的问题。** 写成真实地址时，
//     判断它属不属于本工程要靠比对地址前缀——一个可以被相似域名、被重定向、
//     被大小写绕过的问题。
//
// 用协议形状而不是某种模板标记：它不是一个浏览器能解析的协议，因此**任何一次
// 漏掉解析的场合都是显式失败**（图片裂开、链接点不动），而不是静默指向某个看
// 起来合法的地方。
const PlaceholderScheme = "asset://"

// assetMarkerPathPrefix 是记号在文件组里的**默认路径前缀**。
//
// 手写内容里的 `asset://<资产标识>` 在进入文件组时被解析成一条资产条目，路径
// 默认取 `assets/<资产标识>`——它只影响地址长什么样，不影响任何判定。
const assetMarkerPathPrefix = "assets/"

// isAssetIDByte 判定一个字节能不能出现在资产标识里。
//
// 标识由 base64url 的字母表构成，因此这段字符集同时是**记号的边界**：扫描到
// 第一个不属于它的字节（引号、括号、空格、逗号）就结束。这比"用正则匹配一个
// 标识的形状"更稳：标识的字母表只有一处定义，就是分配它的那一处。
func isAssetIDByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z':
		return true
	case b >= 'A' && b <= 'Z':
		return true
	case b >= '0' && b <= '9':
		return true
	case b == '-' || b == '_':
		return true
	default:
		return false
	}
}

// eachPlaceholder 按出现顺序遍历一段文本里的每一个记号。
//
// 它是**唯一一次扫描实现**：识别（AssetMarkerIDs）与替换
// （SubstituteAssetMarkers）共用它，因此"哪些文本算一个记号"不会有两份答案。
func eachPlaceholder(content string, visit func(at int, assetID string)) {
	search := content
	base := 0
	for {
		idx := strings.Index(search, PlaceholderScheme)
		if idx < 0 {
			return
		}
		at := base + idx
		start := at + len(PlaceholderScheme)
		end := start
		for end < len(content) && isAssetIDByte(content[end]) {
			end++
		}
		if id := content[start:end]; id != "" {
			visit(at, id)
		}
		if end <= at {
			end = start
		}
		search = content[end:]
		base = end
	}
}

// AssetMarkerIDs 返回一段源里出现的资产标识，去重且保持首次出现的顺序。
//
// **它是"这段源里写了哪些记号"的唯一识别入口**，供命令行在把目录送上文件组时
// 把记号解析成资产条目。"一个版本引用了哪些资产"这个问题**不经过它**——那个
// 结论由清单里的资产条目直接读出（见 content_set.go 的 Manifest.Assets）。
func AssetMarkerIDs(content []byte) []string {
	var ids []string
	seen := make(map[string]bool)
	eachPlaceholder(string(content), func(_ int, assetID string) {
		if seen[assetID] {
			return
		}
		seen[assetID] = true
		ids = append(ids, assetID)
	})
	return ids
}

// AssetMarkerPath 返回一个记号在文件组里的默认路径。
func AssetMarkerPath(assetID string) string { return assetMarkerPathPrefix + assetID }

// SubstituteAssetMarkers 把源里每个记号换成 resolve 给出的地址。
//
// **逐字替换，不解析 HTML，这是有意的**（理由见 PlaceholderScheme）。
//
// resolve 报错时整段替换失败：**未解析的 `asset://` 不得出现在产物里**。发布
// 路径上这会先经过校验，因此这里的错误是一条"不该发生"的兜底。
func SubstituteAssetMarkers(content []byte, resolve func(assetID string) (string, error)) ([]byte, error) {
	var out strings.Builder
	text := string(content)
	cursor := 0
	var failure error
	eachPlaceholder(text, func(at int, assetID string) {
		if failure != nil {
			return
		}
		address, err := resolve(assetID)
		if err != nil {
			failure = err
			return
		}
		out.WriteString(text[cursor:at])
		out.WriteString(address)
		cursor = at + len(PlaceholderScheme) + len(assetID)
	})
	if failure != nil {
		return nil, failure
	}
	out.WriteString(text[cursor:])
	return []byte(out.String()), nil
}

// placeholderID 判定一个取值是不是**恰好一个**记号，是则返回资产标识。
//
// 前后空白容忍：`src=" asset://abc "` 与 `src="asset://abc"` 是同一个引用。
// 除此之外多一个字符都不算——这正是"未解析的 asset:// 必须显式失败"那条的
// 实现：`asset://abc/extra` 不是一个记号，因此它会被当成一处指向文件组之外
// 的引用报出来，而不是被静默替换掉前半截。
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

// assetEntryByID 按资产标识在清单里找那条资产条目（唯一入口）。
//
// 一个资产在一个文件组里可能出现在多个路径下，取路径序最小的那一个：解析结果
// 因此是确定的，同样的输入得到同样的产物。
func assetEntryByID(manifest Manifest, assetID string) (Entry, bool) {
	for _, entry := range manifest.Assets() {
		if entry.AssetID == assetID {
			return entry, true
		}
	}
	return Entry{}, false
}

// errExternalResource 是一处指向文件组之外的取资源引用。
func errExternalResource(where, value string) error {
	return fmt.Errorf("%s 的资源引用 %q 指向了本文件组之外；发布物不得从别处取任何字节", where, value)
}
