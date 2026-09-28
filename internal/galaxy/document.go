package galaxy

import (
	"errors"
	"fmt"
)

const (
	// MaxDocumentBytes 是正文（草稿与版本）的字节上限。
	//
	// 它约束的是**一份完整的 HTML 文档**：发布物恒为一个文档，样式与脚本
	// 都内联其中，因此这个上限就是"一个页面能有多大"的上限。
	MaxDocumentBytes = 1 << 20

	// MaxArtifactBytes 是发布产物的字节上限。
	//
	// 它比正文上限宽：改写会把每个 asset:// 占位符换成一条完整的公开地址，
	// 一页引用了很多张图时产物会明显长于正文。这个差额是刻意留的，而不是
	// "产物不该比正文大"的反面。
	MaxArtifactBytes = 2 << 20
)

var (
	// ErrDocumentTooLarge 表示正文超过字节上限。
	ErrDocumentTooLarge = errors.New("正文超过大小上限")

	// ErrArtifactTooLarge 表示发布产物超过字节上限。
	ErrArtifactTooLarge = errors.New("发布产物超过大小上限")
)

// Document 是正文：一份**完整的** HTML 文档（`<!doctype html>` 到 `</html>`）。
//
// 它不被包裹、不被补全、不被注入：
//
//   - **不包裹**：不替用户加 <html>、<head>、<body>，也不注入任何标签、属性
//     或脚本。
//   - **不补全**：漏了闭合标签就漏着，浏览器怎么容错是浏览器的事。
//   - **不注入**：发布时除了替换资产占位符（见 placeholder.go）与在**交付层**
//     附加安全响应头（见 csp.go）之外，正文的每一个字节都来自用户。
//
// 这条的理由很直接：**用户以为发布的是自己写的东西，那就必须是。** 任何一层
// 看不见的包裹都会让"我写的这行为什么没生效"变成一个需要读 aladdin 源码才能
// 回答的问题。安全头走的是 HTTP 响应头而不是正文，因此不违反这一条。
//
// 语法高亮、自动补全、格式化属于编辑器实现细节，不在本模块内。
type Document string

// CheckDocumentSize 判定正文是否在体积上限内（唯一入口）。
func CheckDocumentSize(content Document) error {
	if len(content) > MaxDocumentBytes {
		return fmt.Errorf("%w: 当前 %d 字节，上限 %d 字节", ErrDocumentTooLarge, len(content), MaxDocumentBytes)
	}
	return nil
}

// CheckArtifactSize 判定发布产物是否在体积上限内（唯一入口）。
func CheckArtifactSize(artifact Document) error {
	if len(artifact) > MaxArtifactBytes {
		return fmt.Errorf("%w: 当前 %d 字节，上限 %d 字节", ErrArtifactTooLarge, len(artifact), MaxArtifactBytes)
	}
	return nil
}
