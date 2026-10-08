package skill

import (
	"context"
	"time"
)

// Store 是技能目录的**持久化入口**：库只回答"有哪些、是什么"，字节一律不在它
// 里面（见 docs/design/skill/README.md 的"存储分层"）。
//
// 它同时含读与写。**这里没有"只读接口 + 可变接口"的拆分**——那一套是给判定路径
// 用的（rbac 的判定依赖只读 Store，从类型上排除"判定过程中顺手改数据"，见
// AGENTS.md 的 Go 侧约定）。本包不做判定，它是这条数据唯一的写者，拆开只会多一层
// 转发。
//
// **实现有两份**（内存与 SQL），语义由同一套契约测试守着。
type Store interface {
	// ListSkills 返回全部技能，每条带上它的当前版本。
	//
	// **没有"按调用者过滤"这一维**：目录是部署级的，可见性由权限码表达，不由
	// 归属表达（见 docs/design/skill/README.md 的"归属与权限"）。筛选（关键词、
	// 标签、只看收藏）在服务层做——目录规模由管理员的维护成本决定，为此引入一套
	// 按标题与简介的分词索引不划算。
	ListSkills(ctx context.Context) ([]Skill, error)

	// GetSkill 返回一个技能。不存在时返回 ErrSkillNotFound。
	GetSkill(ctx context.Context, skillID string) (Skill, error)

	// ListVersions 返回一个技能的版本，**最新的在前**。
	//
	// 顺序固定，好让同样的数据在任何实现、任何一次里给出同样的列表。
	ListVersions(ctx context.Context, skillID string) ([]Version, error)

	// CreateSkill 落一个新技能与它的第一个版本。**一个整体**：中途失败时库里
	// 没有这个技能，也没有它的版本。
	CreateSkill(ctx context.Context, s Skill) error

	// AppendVersion 落一个新版本，并把当前指针切到它，同时更新来源里记录的提交。
	// **一个整体**：中途失败时当前指针与提交都保持原样，新版本行不留。
	AppendVersion(ctx context.Context, skillID string, version Version, source Source) error

	// SetCurrentVersion 把当前指针切到该技能已有的某个版本。
	//
	// 版本不属于这个技能时返回 ErrVersionNotFound——**在存储里判**，因为它是一次
	// 原子比较与写入；在服务层先查再写会多一个"查完之后它被删了"的窗口。
	SetCurrentVersion(ctx context.Context, skillID, versionID string) error

	// UpdateMetadata 改说明层：标题、简介与标签（整体替换）。
	//
	// **它不碰内容层任何一项**：当前版本、文件清单与所有字节在改动前后逐字不变。
	UpdateMetadata(ctx context.Context, skillID, title, summary string, tags []string) error

	// PutImage 把一张展示图写进一个技能的图集。
	//
	// **标识已经在图集里时是换图**：只更新它的对象键与字节数，位置不动；不在时
	// 追加到末尾（新增的那一张排在最后）。技能不存在时返回 ErrSkillNotFound。
	//
	// 它只改说明层的那几行：对象本身的写入与删除在服务层（见 image.go）。
	PutImage(ctx context.Context, skillID string, image Image) error

	// DeleteImage 从图集里移除一张图。
	//
	// 标识不在这个技能的图集里时返回 ErrImageNotFound。**其余图的位置不动**：
	// 顺序与对象地址无关，删中间一张不会让后面任何一张的键变（见 image.go）。
	DeleteImage(ctx context.Context, skillID, imageID string) error

	// ReorderImages 把图集顺序整体替换为 imageIDs 给出的顺序，第一项即首图。
	//
	// 它是**一次原子比较与写入**：给出的标识不是该技能当前图集的一个排列时返回
	// ErrImageOrderMismatch，且顺序保持原样；技能不存在时返回 ErrSkillNotFound。
	// 在服务层先查再写会多一个"查完之后它被删了"的窗口。
	ReorderImages(ctx context.Context, skillID string, imageIDs []string) error

	// DeleteSkill 删掉技能、它的版本、标签、图集、收藏与使用记录。
	//
	// **它不删桶上的内容字节**：内容对象按摘要全局共享，同一份字节可能正被别处
	// 引用（见 package.go 的 ContentObjectKey）。展示图的对象由服务层一起删——
	// 那些键由这一个技能独占（见 image.go）。
	DeleteSkill(ctx context.Context, skillID string) error

	// SetFavorite 设置某个主体对一个技能的收藏状态（幂等，两个方向都是）。
	SetFavorite(ctx context.Context, skillID, subjectID string, favorited bool) error

	// FavoriteIDs 返回某个主体收藏的全部技能标识。
	FavoriteIDs(ctx context.Context, subjectID string) (map[string]bool, error)

	// RecordUse 记一次取用，按（技能，主体，日期）归并成一条日次。
	//
	// **按"人日"归并而不是累加次数**：一次取用通常连着取好几个文件，按次记的
	// 数字主要由"这个包有几个文件"决定，与"有没有人在用"无关（见 skill.go 的
	// Usage）。日期由 UsageDay 折算——**它只有一处实现**，因此两个存储实现不会
	// 在"哪一天"这件事上分家。
	RecordUse(ctx context.Context, skillID, subjectID string, at time.Time) error

	// Usage 返回这些技能在 since 之后的使用量。未出现过的技能不在返回的 map 里。
	Usage(ctx context.Context, skillIDs []string, since time.Time) (map[string]Usage, error)

	// DeleteUsageBefore 回收早于 before 的日次行。
	//
	// 它在**服务端启动路径上**被调用（与过期会话、客户端事件同一时机、同一条
	// 理由：回收发生在监听之前，早于任何请求）。
	DeleteUsageBefore(ctx context.Context, before time.Time) error
}
