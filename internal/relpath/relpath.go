// Package relpath 判定**一条相对路径合不合形状**。
//
// 它被两处消费，两处的判据是同一个：galaxy 文件组里的一条条目路径（见
// docs/design/galaxy/site-model.md），以及 skill 一个包里的路径与来源里的子路径
// （见 docs/design/skill/onboarding.md）。
//
// 规则（相对路径、以 `/` 分隔、受限于 URL 安全字符集、不含 `..`、不以 `/` 开头
// 或结尾、不含空段、有长度上限）**字符集不是风格偏好，是地址的约束**：在 galaxy
// 那边这条路径会成为公开地址的一部分，而在 skill 那边它会出现在命令行参数与
// 界面上。把字符集收在 URL 的非保留字符里，等于顺带消掉"同一个路径有两种写法"
// （`%2E` 与 `.`）这条只能靠规范化去覆盖的问题——而规范化一旦缺席，集合成员
// 测试就会既容纳又漏掉某些写法。
//
// 它只判形状，不判含义：路径指向什么、落在哪一棵树里、是不是保留段，都由调用方
// 判。
package relpath

import "strings"

// MaxBytes 是单条路径的长度上限。
//
// 按**字节数**而不是字符数计，与别处的展示字段相反：它不是给人看的标题，而是
// 键与地址的一部分，而字节数是它的实际成本。
const MaxBytes = 512

// Valid 判定一条相对路径合不合形状（唯一入口）。
//
// 它在**写入期**被调用：形状不合的路径根本进不了库，因此读取侧不做任何猜测与
// 规范化（与"集合成员测试只查表，不解析路径"同一条取向）。
func Valid(entryPath string) bool {
	if entryPath == "" || len(entryPath) > MaxBytes {
		return false
	}
	if strings.HasPrefix(entryPath, "/") || strings.HasSuffix(entryPath, "/") {
		return false
	}
	for _, segment := range strings.Split(entryPath, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for i := 0; i < len(segment); i++ {
			if !isPathByte(segment[i]) {
				return false
			}
		}
	}
	return true
}

// isPathByte 判定一个字节能不能出现在路径里。
//
// 它是 RFC 3986 的 unreserved 字符集（字母、数字、`-`、`.`、`_`、`~`）。刻意
// 不收 `%`：收了它就得处理百分号编码的等价性，而那是集合成员测试最容易被绕过
// 的地方。
func isPathByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '-' || b == '_' || b == '.' || b == '~':
		return true
	default:
		return false
	}
}
