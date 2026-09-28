package profile

import (
	"errors"
	"time"

	"github.com/poetlife/aladdin/internal/objectstore"
)

const (
	// avatarKeyPrefix 是头像对象键的前缀。
	//
	// 它是常量而不是配置项：做成配置项只会多出一种失败方式——改了前缀，
	// 存量头像在一瞬间全部变成无从被引用的孤儿，而档案本身看起来毫无变化
	// （见 docs/design/profile/avatar-storage.md）。
	avatarKeyPrefix = "avatars/"

	// AvatarMaxBytes 是头像的字节上限。
	//
	// 它出现在两处，且两处都必须有：**签发时**按声明的大小早退（省一次白传），
	// **存储侧**按它对请求体长度强制（那才是真正的边界）。只留前者等于信上传方
	// 的声明，只留后者等于让用户白传一遍（见 docs/design/objectstore/README.md）。
	//
	// 只约束字节数，不约束像素尺寸：后者需要引入图像解码依赖。
	AvatarMaxBytes = 2 * 1024 * 1024

	// AvatarURLTTL 是下发地址的有效期。
	//
	// 取分钟量级：地址是一份短期凭证，有效期越长，"本人把它转出去"这件事的
	// 代价就越大。过期后重新读取档案即可拿到新地址。
	AvatarURLTTL = 10 * time.Minute
)

var (
	// ErrAvatarUnavailable 表示这个部署没有配置头像存储。
	//
	// **它不是故障**，而是"这个能力没开"：服务端照常启动，昵称与简介照常
	// 可用，前端据此不渲染上传控件（见 docs/design/config/server-config.md）。
	ErrAvatarUnavailable = errors.New("头像功能未启用")

	// ErrAvatarTooLarge 表示头像超过字节上限。
	ErrAvatarTooLarge = errors.New("头像超过大小上限")

	// ErrAvatarTypeNotAllowed 表示声明的类型不在白名单内。
	ErrAvatarTypeNotAllowed = errors.New("头像必须是 PNG、JPEG 或 GIF")

	// ErrAvatarObjectMissing 表示提交时那个键上还没有对象。
	//
	// 它是"直传没完成"这一情形的结论：网络中断、页面被关掉、客户端报错都会
	// 落到这里。**它不是故障**，而是一次可以重来的上传。
	ErrAvatarObjectMissing = errors.New("头像对象不存在，上传可能没有完成")
)

// avatarAllowedTypes 是可接受的头像类型白名单。
//
// 三种都是"无法携带可执行内容"的位图格式。刻意不收 SVG：它是 XML，可以内嵌
// 脚本，且头像会被下发到浏览器——即便放在 <img> 里不执行，把它纳入白名单也等于
// 给将来某次"改成直接打开"留下一颗雷。
//
// **这份白名单同时是签发给存储的类型条件**（见 AvatarTypeRules）：因此它不只是
// 服务端的检查，还是存储侧执行的那条边界。两者是同一个来源，不是两份清单。
var avatarAllowedTypes = []string{"image/png", "image/jpeg", "image/gif"}

// AvatarKey 返回一个主体的头像在对象存储里的对象键（唯一入口）。
//
// 一个主体一个键：替换头像就是原地覆盖，既不产生孤儿对象，也让一张旧的取图
// 地址在过期前指向新头像，而不是泄露被替换掉的那张。
//
// **头像是直传链路上唯一允许覆盖的用途**：键每次都相同，而资产的键一次一个。
// 这个差别是刻意的——头像本来就是一个"覆盖写"的字段（见
// docs/design/objectstore/README.md）。
func AvatarKey(subjectID string) string {
	return avatarKeyPrefix + subjectID
}

// NormalizeAvatarType 判定一个**声明的**类型能不能作为头像（唯一入口）。
//
// 它接受的是声明值，不接受字节：直传之后服务端看不到字节（见
// docs/design/objectstore/README.md）。白名单因此从"字节确实是图片"降级成了
// "下发时的类型必属一个无害集合"——而白名单里没有任何可执行类型，后者才是这套
// 链路能成立的原因。
//
// 归一化（去空白、去掉 `;` 之后的参数部分、转小写）在判定之前完成：上传方写
// `IMAGE/PNG` 与 `image/png; charset=binary` 都是同一个类型。
func NormalizeAvatarType(declared string) (string, error) {
	normalized := objectstore.NormalizeContentType(declared)
	for _, allowed := range avatarAllowedTypes {
		if normalized == allowed {
			return allowed, nil
		}
	}
	return "", ErrAvatarTypeNotAllowed
}

// AvatarTypeRules 返回签发直传凭证用的类型规则（唯一入口）。
//
// 规则**由白名单派生**，不另写一份：两处各写一份的表现是"服务端接受了、
// 存储侧拒绝"（或反过来），而用户看到的是一句无法归因的失败。
func AvatarTypeRules() []objectstore.TypeRule {
	rules := make([]objectstore.TypeRule, 0, len(avatarAllowedTypes))
	for _, contentType := range avatarAllowedTypes {
		rules = append(rules, objectstore.TypeRule{ContentType: contentType, MaxBytes: AvatarMaxBytes})
	}
	return rules
}
