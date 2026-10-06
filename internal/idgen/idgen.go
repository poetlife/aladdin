// Package idgen 是**不可猜标识**的分配入口。
//
// 平台里有一类标识既是主键、又是地址的一部分，且必须猜不出来——工程标识
// （发布地址的一部分，见 docs/design/galaxy/README.md）、资产标识、版本标识，
// 以及技能标识（见 docs/design/skill/README.md）。它们都由这一个入口分配：
// **分配即冻结、不可猜、永不复用**——与主体标识同一条约定。
//
// 它被两处消费（galaxy 与 skill），而"从密码学随机源取多少熵、怎么编码"必须是
// 同一个答案：两处各写一份的表现是"有一类标识比另一类短一截"，那是一个只有在
// 有人去数长度时才会被发现的问题。
package idgen

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// EntropyBytes 是标识里随机部分的字节数。
//
// 128 位不可猜：工程标识是发布地址的一部分，而发布态是公开匿名的——"地址即
// 凭据"的全部强度都落在这几个字节上。被分配它的每一类标识都按这个强度走，不
// 因为"这一类看起来没那么敏感"而分档：分档等于让"哪一类标识可以猜"变成一个
// 需要有人记得住的规则。
const EntropyBytes = 16

// New 从密码学随机源取一段熵并编码，前缀由调用方给。
//
// 用 base64url 而不是十六进制：同样的字节数下它更短，而这些标识会出现在地址与
// 命令行参数里，短一点对所有人可读。
func New(prefix string) (string, error) {
	buf := make([]byte, EntropyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("分配标识失败: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(buf), nil
}
