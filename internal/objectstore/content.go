package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// 本文件是**内容对象**的公共规则：一份按内容摘要寻址的字节，怎么算它的摘要、
// 怎么判一个声明的摘要合不合法、以及怎么把它写下去。
//
// 消费方有两处，它们的内容来源完全不同——galaxy 的内容对象由服务端在发布时生成
// （渲染与记号替换的结果，见 docs/design/galaxy/publication.md），skill 的内容
// 由服务端从远端仓库取回（见 docs/design/skill/onboarding.md）——但"按内容寻址"
// 这件事的规则是同一套：键只回答"这份字节是什么"，因此同一份字节在任何版本、
// 任何次写入里都落在同一个键上，而**写入必须是幂等的**。
//
// 这两处各写一份的表现不是多几行代码，而是一处防住了覆盖、另一处没防住，表现
// 为"某个版本的内容在别人写入之后变了"。

// ErrDigestMismatch 表示一段字节与它声明的摘要不符。
//
// 它与"存储不可用"必须分开：前者是**一次内容问题**（调用方算错了、或者拿错了
// 字节），后者是一次故障。两者的应对完全不同。
var ErrDigestMismatch = errors.New("字节与声明的摘要不符")

// ContentDigest 返回一段字节的内容摘要（唯一入口）。
//
// 摘要是"同一份字节"的标识。用 SHA-256 而不是更短的摘要：这里没有"摘要碰撞被
// 利用"的对抗场景需要额外论证，用标准库最长的那个算法就不必解释为什么够长。
func ContentDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// contentDigestLength 是内容摘要的长度：SHA-256 的十六进制。
const contentDigestLength = 64

// IsContentDigest 判定一个**声明的**摘要形状是否合法。
//
// 直传之后服务端算不了摘要，它由上传方声明（见
// docs/design/objectstore/README.md）。而摘要会成为**对象键**，因此它的形状必须
// 在被使用之前校验：一个含 `/`、`..` 或控制字符的取值会把"按内容寻址"变成
// "按调用方给的路径写"。
//
// 它只校验形状，不校验正确性——正确性在写入前核对（见 PutContentObject）。
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

// PutContentObject 把一个内容对象写进**私有区**，**仅当该键上还没有对象**
// （唯一入口）。
//
// 第二个返回值表示这次是否真的写了。"这一份字节是不是新的"因此是一个只看键
// 就能回答的问题——重复写入同一份内容不产生任何新字节，也不覆盖任何东西。
//
// **"仅当不存在时写入"是内容寻址的硬性要求，不是优化。** 否则一个算错或伪造的
// 摘要会落到另一个版本已经在用的键上，把那个对象改写掉——而那是"版本不可变"的
// 反面。因此：
//
//   - 先 Head：对象已在则直接返回，**不做任何写入**；
//   - 写入前核对 digest 与 data 相符，不一致即拒（调用方自己算错了摘要时，
//     拒绝比写下去好：写下去会把一个错误的键指向一份正确的字节，而那个键此后
//     永远对不上）。
//
// 对象用**中性类型**存放：下发时的内容类型由请求的路径扩展名决定，对象本身不
// 承担类型语义——顺带消掉"预签名地址被直接打开时按 HTML 渲染"这条隐患。
func PutContentObject(ctx context.Context, store Store, key, digest string, data []byte) (bool, error) {
	if actual := ContentDigest(data); actual != digest {
		return false, fmt.Errorf("%w: 声明 %s，实际 %s", ErrDigestMismatch, digest, actual)
	}
	if _, err := store.Head(ctx, key); err == nil {
		return false, nil
	} else if !errors.Is(err, ErrObjectNotFound) {
		return false, err
	}
	if err := store.Put(ctx, key, NeutralContentType, data); err != nil {
		return false, err
	}
	return true, nil
}
