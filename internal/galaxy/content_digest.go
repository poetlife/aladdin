package galaxy

import "github.com/poetlife/aladdin/internal/objectstore"

// ContentDigest 返回一段字节的内容摘要。
//
// **实现只有一处**：`objectstore.ContentDigest`。skill 的内容对象走的是同一个
// 摘要（见 docs/ssot-registry.md 与 docs/design/skill/onboarding.md），两处
// 用不同算法或不同编码的表现是"同一份字节在两处是两个键"。
//
// 这里保留一个名字，是为了让调用方读到的仍然是"galaxy 的内容对象"——而
// "摘要是同一份字节的标识"这条语义，见 objectstore 包。
func ContentDigest(data []byte) string { return objectstore.ContentDigest(data) }

// IsContentDigest 判定一个**声明的**摘要形状是否合法。
//
// 实现同样只有一处（见 ContentDigest）。它只校验形状，不校验正确性——正确性在
// 提交时核对（见 asset.go 的 verifyAssetDigest）。
func IsContentDigest(declared string) bool { return objectstore.IsContentDigest(declared) }
