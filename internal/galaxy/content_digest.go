package galaxy

import (
	"crypto/sha256"
	"encoding/hex"
)

// ContentDigest 返回一段字节的内容摘要（唯一入口）。
//
// 摘要是"同一份字节"的标识：私有区按它确认去重（同一份字节在本工程里只存一份），
// 公开区把它当作地址的第二段。由此得到两条直接结果：
//
//   - 同一份字节在**同一个工程**的任何版本、任何次发布里都落在**同一个**公开
//     地址上，因此"这份资产在这个工程里是否已上架"是一个只看地址就能回答的
//     问题；
//   - 摘要在**不同的工程之间不合并**：公开区的地址里有一段工程标识（见
//     promote.go 的 ReleaseObjectKey），两个工程用到相同字节是两份对象。
//
// 用 SHA-256 而不是更短的摘要：这里没有"摘要碰撞被利用"的对抗场景需要额外
// 论证，用标准库最长的那个算法就不必解释为什么够长。
func ContentDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// contentDigestLength 是内容摘要的长度：SHA-256 的十六进制。
const contentDigestLength = 64

// IsContentDigest 判定一个**声明的**摘要形状是否合法。
//
// 直传之后服务端算不了摘要，它由上传方声明（见
// docs/design/objectstore/README.md）。而摘要会成为**公开区的对象键**，因此
// 它的形状必须在被使用之前校验：一个含 `/`、`..` 或控制字符的取值会把"按内容
// 寻址"变成"按调用方给的路径写"。
//
// 它只校验形状，不校验正确性——正确性在上架时读回字节核对（见 promote.go）。
func IsContentDigest(declared string) bool {
	if len(declared) != contentDigestLength {
		return false
	}
	for i := 0; i < len(declared); i++ {
		c := declared[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}
