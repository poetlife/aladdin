// Package imagetype 是**"这份字节能不能当一张会下发给浏览器的小图"**这一判断的
// 唯一实现：收哪些格式、上限多大、以及服务端看得见字节时怎么核对。
//
// 消费方有两处：个人档案的头像，与技能目录的封面。它们问的是同一个问题——一张由
// 用户提供、存在对象存储里、由浏览器直接取的展示图，收哪些格式——因此只有一份
// 白名单。各写一份的表现是"头像收的封面不收"这类没人解释得清的差异。
//
// **它与 galaxy 资产的白名单不是同一个判断，因此刻意不合并。** 那边问的是"这份
// 字节能不能当工程素材"：收 WebP、上限 10 MiB、而且会发布到公开区给所有人取。
// 判据不同（展示小图 / 素材），合并只会让两边的规则互相迁就（见
// docs/ssot-registry.md）。判据是不是同一件事，看的是"问的是什么"，不是"表里有没有
// 重合的取值"。
package imagetype

import (
	"bytes"
	"errors"
	"path"
	"strings"

	"github.com/poetlife/aladdin/internal/objectstore"
)

// MaxBytes 是这类图片的字节上限。
//
// 它出现在两处，且两处都必须有：**签发时**按声明的大小早退（省一次白传），
// **存储侧**按它对请求体长度强制（那才是真正的边界）。只留前者等于信上传方的
// 声明，只留后者等于让用户白传一遍（见 docs/design/objectstore/README.md）。
//
// 只约束字节数，不约束像素尺寸：后者需要引入图像解码依赖。
const MaxBytes = 2 * 1024 * 1024

// ErrTypeNotAllowed 表示这个类型不在白名单内。
var ErrTypeNotAllowed = errors.New("图片必须是 PNG、JPEG 或 GIF")

// allowedTypes 是可接受的白名单。
//
// 三种都是"无法携带可执行内容"的位图格式。刻意不收 SVG：它是 XML，可以内嵌脚本，
// 而这类图会被下发到浏览器——即便放在 <img> 里不执行，把它纳入白名单也等于给将来
// 某次"改成直接打开"留下一颗雷。
//
// **这份白名单同时是签发给存储的类型条件**（见 Rules）：因此它不只是服务端的检查，
// 还是存储侧执行的那条边界。两者是同一个来源，不是两份清单。
var allowedTypes = []string{"image/png", "image/jpeg", "image/gif"}

// extensionTypes 是"扩展名 → 类型"，供**服务端自己取回字节**的那条路用。
//
// 上传那条路没有它：客户端直传，服务端看不到路径，只知道上传方声明的类型。
// 这条路则先按扩展名判断"这看着是不是一张图"，再用魔数核对它究竟是哪一种。
var extensionTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
}

// Allowed 返回白名单的副本。
//
// 返回副本而不是那份切片本身：调用方（界面上的文案、测试）拿到的是取值，不是
// 一个能被就地改掉的全局变量。
func Allowed() []string {
	out := make([]string, len(allowedTypes))
	copy(out, allowedTypes)
	return out
}

// Rules 返回签发直传凭证用的类型规则（唯一入口）。
//
// 规则**由白名单派生**，不另写一份：两处各写一份的表现是"服务端接受了、存储侧
// 拒绝"（或反过来），而用户看到的是一句无法归因的失败。
func Rules() []objectstore.TypeRule {
	rules := make([]objectstore.TypeRule, 0, len(allowedTypes))
	for _, contentType := range allowedTypes {
		rules = append(rules, objectstore.TypeRule{ContentType: contentType, MaxBytes: MaxBytes})
	}
	return rules
}

// Normalize 判定一个**声明的**类型能不能当这类图（唯一入口）。
//
// 它接受的是声明值，不接受字节：直传之后服务端看不到字节（见
// docs/design/objectstore/README.md）。白名单因此从"字节确实是图片"降级成了
// "下发时的类型必属一个无害集合"——而白名单里没有任何可执行类型，后者才是这套
// 链路能成立的原因。
//
// 归一化（去空白、去掉 `;` 之后的参数部分、转小写）在判定之前完成：上传方写
// `IMAGE/PNG` 与 `image/png; charset=binary` 都是同一个类型。
func Normalize(declared string) (string, error) {
	normalized := objectstore.NormalizeContentType(declared)
	for _, allowed := range allowedTypes {
		if normalized == allowed {
			return allowed, nil
		}
	}
	return "", ErrTypeNotAllowed
}

// FromExtension 判定一个路径**看着**是不是一张这类图，并给出它的类型。
//
// "看着"是它的全部含义：扩展名是调用方给的一个字符串，撒谎不花成本。它的用途是
// 在取字节**之前**把明显不是图的东西挑出去；取回来之后还要用 MatchMagic 核对一次。
func FromExtension(name string) (string, bool) {
	contentType, ok := extensionTypes[strings.ToLower(path.Ext(name))]
	return contentType, ok
}

// MatchMagic 按**字节本身**判定这是哪一种图（唯一入口）。
//
// 它存在的前提是"服务端看得见这份字节"——只有自己取回字节的那条路做得到。直传那条
// 路看不到，因此那里只能信声明。**看得见就验一次，看不见就只信声明**，这条对照本身
// 是要写进 spec 的事实，而不是一处疏漏。
//
// 判据是文件头（PNG 的签名、JPEG 的 SOI、GIF 的版本串），不解析图像结构：目的是
// 确认"这是它所声称的那类图"，不是判断它能不能解码。
func MatchMagic(data []byte) (string, bool) {
	switch {
	case bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}):
		return "image/png", true
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg", true
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "image/gif", true
	default:
		return "", false
	}
}
