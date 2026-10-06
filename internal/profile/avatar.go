package profile

import (
	"errors"
	"time"

	"github.com/poetlife/aladdin/internal/imagetype"
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
	// **取值来自 imagetype**：那是"展示小图"这一类图片的统一上限，头像与技能封面
	// 共用同一份（见 docs/ssot-registry.md）。这里保留一个名字，是为了让调用方读到的
	// 仍然是"头像的上限"。
	AvatarMaxBytes = imagetype.MaxBytes

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
	//
	// 与 AvatarMaxBytes 同理：它是 imagetype 那个错误的另一个名字，不是第二份定义。
	ErrAvatarTypeNotAllowed = imagetype.ErrTypeNotAllowed

	// ErrAvatarObjectMissing 表示提交时那个键上还没有对象。
	//
	// 它是"直传没完成"这一情形的结论：网络中断、页面被关掉、客户端报错都会
	// 落到这里。**它不是故障**，而是一次可以重来的上传。
	ErrAvatarObjectMissing = errors.New("头像对象不存在，上传可能没有完成")
)

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

// NormalizeAvatarType 判定一个**声明的**类型能不能作为头像。
//
// **实现只有一处**：`imagetype.Normalize`。技能封面的"从仓库取"那条路走的是同一个
// 判断的另一半（扩展名与魔数），两处各写一份的表现是"头像收的封面不收"。
func NormalizeAvatarType(declared string) (string, error) {
	return imagetype.Normalize(declared)
}

// AvatarTypeRules 返回签发直传凭证用的类型规则。
//
// 实现同样只有一处：`imagetype.Rules`。
func AvatarTypeRules() []objectstore.TypeRule { return imagetype.Rules() }
