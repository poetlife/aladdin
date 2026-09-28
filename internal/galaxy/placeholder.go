package galaxy

import "strings"

// PlaceholderScheme 是资产占位符的协议形状。
//
// 正文里引用素材写的是 `asset://<资产标识>`，例如
// `<img src="asset://a1b2c3">` 或 `<style>body{background:url(asset://a1b2c3)}</style>`。
//
// **用 `asset://` 这个协议形状，而不是某种模板标记**：它不是一个浏览器能解析
// 的协议，所以**任何一次漏掉改写的场合都是显式失败**（图片裂开、链接点不动），
// 而不是静默指向某个看起来合法的地方。静默失败的内容发布是最糟的失败方式
// ——它不会被人发现，直到有人看懂了页面上的错。
const PlaceholderScheme = "asset://"

// isAssetIDByte 判定一个字节能不能出现在资产标识里。
//
// 标识由 base64url 的字母表构成，因此这段字符集同时是**占位符的边界**：
// 扫描到第一个不属于它的字节（引号、括号、空格、逗号）就结束。这比"用正则
// 匹配一个标识的形状"更稳：标识的字母表只有一处定义，就是分配它的那一处。
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

// eachPlaceholder 按出现顺序遍历正文里的每一个占位符。
//
// 它是**唯一一次扫描实现**：识别（ReferencedAssetIDs）与改写
// （RewritePlaceholders）共用它，因此"哪些文本算一个占位符"不会有两份答案。
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

// ReferencedAssetIDs 返回正文里引用到的资产标识，去重且保持首次出现的顺序。
//
// **这是"一段正文引用了哪些资产"的唯一识别入口。** 由此得到的两条结论见
// project-versioning.md：一个版本引用了哪些资产可以从正文单独确定（不需要
// 查任何别的记录），而"资产是否仍被引用"也是同一个调用。
//
// 返回**有序**而不是无序集合：发布时按这个顺序上架与留痕，同样的输入得到
// 同样的顺序，日志与错误信息才是可比较的。
func ReferencedAssetIDs(content Document) []string {
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

// RewritePlaceholders 把正文里每个占位符换成 resolve 给出的地址。
//
// **逐字替换，不解析 HTML，这是有意的。** 占位符出现在元素属性里、在
// `<style>` 里的 CSS `url()` 里、在 `srcset` 里，都只是把这段文本换掉。依赖
// "枚举哪些属性算引用"的做法必然会漏（`srcset`、内联 CSS、将来新增的属性），
// 而逐字替换不会漏。
//
// resolve 报错时整个改写失败：**未改写的 `asset://` 不得出现在产物里**。
// 发布路径上这会先经过 validate.go 的校验，因此这里的错误是一条"不该发生"
// 的兜底，而不是用户会看到的提示。
func RewritePlaceholders(content Document, resolve func(assetID string) (string, error)) (Document, error) {
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
		return "", failure
	}
	out.WriteString(text[cursor:])
	return Document(out.String()), nil
}
