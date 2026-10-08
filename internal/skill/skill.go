// Package skill 是"平台提供一组辅助创作的技能，由管理员从远端纳管，创作者与
// agent 检索并取用"这一能力的唯一实现。
//
// 行为约定见 docs/design/skill/。四条贯穿本包的不变量：
//
//   - **版本不可变**：一个技能版本一旦产生就逐字不变，同步产生新版本、回滚切
//     指针（见 catalog.go）。
//   - **库只存清单，不存字节**：文件树落到库里是一组「路径 → 内容摘要 + 字节
//     数」，字节一律在对象存储、按内容摘要全局共享（见 package.go）。
//   - **平台从不写调用方的文件系统**：本包没有任何一条会创建文件的路径，取用
//     只把字节交回给调用方（见 docs/design/skill/agent-access.md）。
//   - **正文不被执行、不被解释**：除了读 SKILL.md 的 frontmatter 取两项（name 与
//     description），一切字节都当作不透明的文本。
//
// 边界：它不回答"你是谁"（认证）也不回答"你能做什么"（RBAC）。判定发生在服务端
// 的鉴权拦截器里，本包消费的是它的结论与一个**已确认的主体标识**（后者只用在
// 收藏与使用统计上）。
//
// **本包没有归属判定。** 目录是部署级的：技能不属于任何主体，谁能看见全部由
// `skill.catalog.read` 表达。这与 galaxy 正相反（那边每一个动作都以"调用者自己
// 的工程"为目标），因此这里没有第二把闸门。
package skill

import (
	"errors"
	"fmt"
	"time"

	"github.com/poetlife/aladdin/internal/idgen"
	"github.com/poetlife/aladdin/internal/tagging"
)

// 标识前缀。**唯一入口**是本文件的分配函数：技能标识与版本标识由此分配，
// 分配即冻结、永不复用——与主体标识、工程标识同一条约定。
//
// 随机部分从哪来、取多少熵由 `idgen` 决定（那里是全部标识分配的公共入口），
// 这里只声明"哪一类标识长什么前缀"。
const (
	skillIDPrefix   = "skl_"
	versionIDPrefix = "skv_"
)

// 包契约的上限。**它们都是常量，不是配置项**：做成配置项只会让"这个部署收得下
// 的包"变成一个要在别处解释的事实，而它与任何部署形态无关。
const (
	// MaxFiles 是一个技能包的文件数上限。
	MaxFiles = 200
	// MaxFileBytes 是单个文件的字节上限。
	MaxFileBytes = 256 << 10
	// MaxPackageBytes 是一个包的字节总数上限。
	MaxPackageBytes = 2 << 20
	// MaxNameRunes 是 SKILL.md 里 name 的长度上限。
	//
	// 按**字符数**而不是字节数计，与别处的展示字段同一条理由。
	MaxNameRunes = 64
	// MaxDescriptionRunes 是 SKILL.md 里 description 的长度上限。
	MaxDescriptionRunes = 1024
	// MaxTitleRunes 是说明层标题的长度上限。
	MaxTitleRunes = 128
	// MaxSummaryRunes 是说明层简介的长度上限。与 description 同档。
	MaxSummaryRunes = 1024
	// MaxRefRunes 是来源引用的长度上限。
	MaxRefRunes = 256
)

// ManifestPath 是包契约要求的那份清单文件，必须在包根。
const ManifestPath = "SKILL.md"

// 目录读侧的规模与口径。
const (
	// ListLimit 是一次列表返回的条数上限。
	//
	// **不做游标翻页**：目录由管理员维护，规模有天然上界，而"翻到第几页"在一个
	// 人维护的目录里没有用途。上限存在的意义只是让响应有界，超出时由调用方用
	// 关键词缩小范围。
	ListLimit = 200
	// UsageWindow 是使用量的统计窗口。
	UsageWindow = 30 * 24 * time.Hour
	// UsageRetention 是使用日次明细的保留期。
	//
	// 比窗口多留一天，好让"最近 30 天"这个口径不会在日界上少算一格（与遥测那边
	// 的回收多留一天同一条理由）。
	UsageRetention = UsageWindow + 24*time.Hour
)

var (
	// ErrSkillNotFound 表示技能不存在（或已被删除）。两者是同一个结论。
	ErrSkillNotFound = errors.New("技能不存在")

	// ErrVersionNotFound 表示版本不存在，或者它不属于这个技能。
	//
	// 后者与前者的结论相同是有意的：把"这个版本是别人的"与"这个版本不存在"
	// 分开报，等于提供一个跨技能的版本枚举接口。
	ErrVersionNotFound = errors.New("技能版本不存在")

	// ErrFileNotFound 表示这个技能里没有这条路径。
	//
	// **它与 ErrSkillNotFound 必须分开**：技能在，是那条路径不在。混为一谈会让
	// 调用方在拼错路径时以为整个技能没了。
	ErrFileNotFound = errors.New("技能里没有这条路径")

	// ErrRepositoryInvalid 表示来源的地址形状不合法。
	//
	// 它是**用法错误**：改调用参数即可，不涉及任何远端状态。它发生在解析阶段，
	// **此时不发生任何出站请求**。
	ErrRepositoryInvalid = errors.New("远端仓库地址不合法")

	// ErrRepositoryNotFound 表示远端没有这个仓库或这个引用。
	ErrRepositoryNotFound = errors.New("远端仓库或引用不存在")

	// ErrRemoteUnavailable 表示远端不可达（网络故障、5xx、超时、限频）。
	//
	// 它与"仓库不存在"必须分开：前者是**稍后重试**，后者是**改引用或改来源**。
	ErrRemoteUnavailable = errors.New("远端不可达")

	// ErrPackageInvalid 表示这份内容不满足包契约。
	//
	// 它是**内容问题**：只有改远端仓库才能过。错误信息点名那一处（哪一个路径、
	// 违反了哪一条）。
	ErrPackageInvalid = errors.New("技能包不合法")

	// ErrObjectStoreUnavailable 表示对象存储不可用，或**这个部署根本没有配置它**。
	//
	// 与 galaxy 的资产同一条降级取向：没有桶时技能目录整体不可用，因为它的字节
	// 没有地方放。前端据此不渲染入口，而不是渲染一个点了报错的控件。
	ErrObjectStoreUnavailable = errors.New("对象存储不可用")

	// ErrRemoteNotConfigured 表示这个部署没有可用的远端拉取实现（装配缺失）。
	ErrRemoteNotConfigured = errors.New("远端拉取未装配")

	// ErrStoreUnavailable 表示关系库不可用。
	//
	// 它与"技能不存在"必须分开：后者是正常状态，这个是一次故障。两种存储实现
	// 在这一点上的表现由同一套契约测试守着。
	ErrStoreUnavailable = errors.New("技能目录存储不可用")
)

// Skill 是一个技能：说明层 + 来源 + **当前版本**。
//
// 版本内联在这里而不是分两张表读：目录里"这一条是什么"永远要连着当前版本一起
// 回答（有效标题与简介取自它、文件数与字节数也取自它），分开读只会让每一处消费
// 都自己拼一次。
type Skill struct {
	// ID 是技能标识。由服务端分配、不可猜、不可改、不复用。
	ID string
	// Title 与 Summary 是**说明层的原值**。空表示没有覆盖、走回退。
	Title   string
	Summary string
	// Tags 是归一化之后的标签（小写、去重、有序）。见 tagging。
	Tags []string
	// Images 是**展示图集**，有序：第一张即卡片上的封面。空表示没有图。
	//
	// **它是说明层的一项**，与标题、简介、标签同级：可改、不进版本、不进包（见
	// image.go）。每一项存的是对象键而不是地址——地址是短时签发的，落库只会留下
	// 一份过期的。
	Images []Image
	// Source 是内容的来源。纳管之后不可改。
	Source Source
	// Current 是当前对外服务的那一份快照。**它永远存在**——技能是随第一个版本
	// 一起产生的，没有"建好了还没有版本"这个状态。
	Current Version
}

// Source 是一个技能的内容从哪来。
//
// 它**不可改**：换仓库、换引用、换子路径都要重新纳管，得到一个**新的技能**。
// 一个技能的历次版本因此被固定为"同一条来源的不同时间点"，回滚才有意义。
type Source struct {
	// Owner 与 Name 是仓库地址的两段。**分开存**而不是存一个地址串：地址串的
	// 形状规则将来若变，存量行不会因此解析不出来；而取字节的地址由这两段现拼，
	// 不存在"存进去的地址与用出去的地址不是一回事"这条缝。
	Owner string
	Name  string
	// Ref 是分支 / 标签 / 提交标识。为空表示仓库的默认分支。
	Ref string
	// SubPath 是包在仓库里的子路径。为空表示仓库根。
	SubPath string
	// Commit 是**上次同步到的提交**。它是"这个技能更新到哪了"的唯一答案，
	// 也是同步时"远端有没有变化"的比对对象。
	Commit string
}

// URL 返回来源的展示形态（唯一入口）。
//
// 它**由两段现拼**，不是把调用方给的那串原样存下来：原样存的话，一个多写了
// `.git` 或结尾斜杠的地址会一直以那个形状出现在界面上，而两种写法的技能看起来
// 像是两个不同的来源。
func (s Source) URL() string {
	if s.Owner == "" || s.Name == "" {
		return ""
	}
	return "https://github.com/" + s.Owner + "/" + s.Name
}

// Repository 是从地址里解析出来的仓库——取字节的输入。
//
// 它是**两段名字，不是一个地址**：远端实现据此自己拼请求地址，因此"调用方给的
// 地址"与"真正被请求的地址"之间不存在一条可以被塞进别的东西的缝（见
// docs/design/skill/onboarding.md 的"来源的形状"）。
type Repository struct {
	Owner string
	Name  string
}

// Version 是一个技能的一份**不可变快照**。
type Version struct {
	ID      string
	SkillID string
	// Commit 是这一份取自哪个提交。
	Commit string
	// Name 与 Description 是这一份的 SKILL.md 里声明的两项。
	//
	// **在纳管时解析一次并存下来**，不在每次读取时回到对象里取：它们是版本快照的
	// 一部分（内容不变它们就不变），而目录列表要读很多条——每次回去读一个对象会
	// 把一次列表变成几百次对象读取。同时它们也是说明层留空时的回退目标。
	Name        string
	Description string
	// Files 是这一份的文件清单（路径 → 内容摘要 + 字节数）。**不含正文**。
	Files []File
	// SkippedFiles 是**上游有、而平台没收**的条目数。
	//
	// 平台只分发文本，因此真实仓库里那些示例图一类的东西不进包。这个计数让
	// "平台里的包比上游少几个文件"成为一件看得见的事——静默地少才是问题（见
	// docs/design/skill/onboarding.md 的"收得下的文件"）。
	SkippedFiles int
	CreatedAt    time.Time
}

// TotalBytes 返回这一份所有文件的字节总数。
func (v Version) TotalBytes() int64 {
	var total int64
	for _, f := range v.Files {
		total += f.SizeBytes
	}
	return total
}

// File 是版本清单里的一条。
type File struct {
	// Path 是包内相对路径。
	Path string
	// Digest 是内容摘要。同一份字节在**整个目录里**只有一个对象，因此两个技能里
	// 内容相同的文件有同一个摘要。
	Digest string
	// SizeBytes 是字节数。
	SizeBytes int64
}

// Manifest 是 SKILL.md 里被平台读出来的那两项。
//
// 其余 frontmatter 字段与正文**原样存储、不做解释**——它们属于这个技能的读者，
// 不属于平台。
type Manifest struct {
	Name        string
	Description string
}

// EffectiveTitle 返回对外展示的标题：说明层留空时回退到当前版本的 name。
//
// **一个判据是"空"，不是"内容是一个空字符串"**：与 galaxy 资产的展示标题为空时
// 回退到文件名同一条取向。回退在服务端算完才下发，三端不得各算一遍。
func (s Skill) EffectiveTitle() string {
	if s.Title != "" {
		return s.Title
	}
	return s.Current.Name
}

// EffectiveSummary 返回对外展示的简介：说明层留空时回退到当前版本的 description。
//
// 回退取的是**当前版本**的那一项：同步换到一个 description 不同的版本之后，
// 没有覆盖过简介的技能展示简介会跟着变——它是"这一份内容的自我描述"，内容换了
// 它就换了。
func (s Skill) EffectiveSummary() string {
	if s.Summary != "" {
		return s.Summary
	}
	return s.Current.Description
}

// NormalizeMetadata 归一化说明层并校验长度（唯一入口）。
//
// 标题与简介按**字符数**限长；首尾空白一律去掉——"只由空白组成的标题"与"没有
// 标题"是同一件事，而回退靠的就是"空"这一个判据。中间部分的空白与换行原样保留。
//
// 标签交给 `tagging.Normalize`：它与 galaxy 资产的标签是**同一处实现**，两处
// 归一化出不同的样子的表现是"这个标签看着对、就是筛不出来"。
func NormalizeMetadata(title, summary string, tags []string) (string, string, []string, error) {
	title, err := trimToRunes(title, MaxTitleRunes, ErrTitleTooLong)
	if err != nil {
		return "", "", nil, err
	}
	summary, err = trimToRunes(summary, MaxSummaryRunes, ErrSummaryTooLong)
	if err != nil {
		return "", "", nil, err
	}
	normalized, err := tagging.Normalize(tags)
	if err != nil {
		return "", "", nil, err
	}
	return title, summary, normalized, nil
}

var (
	// ErrTitleTooLong 表示说明层标题超过长度上限。
	ErrTitleTooLong = errors.New("技能标题过长")
	// ErrSummaryTooLong 表示说明层简介超过长度上限。
	ErrSummaryTooLong = errors.New("技能简介过长")
)

// trimToRunes 去掉首尾空白并按字符数校验长度。
func trimToRunes(value string, max int, tooLong error) (string, error) {
	value = trimSpace(value)
	if len([]rune(value)) > max {
		return "", fmt.Errorf("%w: 上限 %d 个字", tooLong, max)
	}
	return value, nil
}

// NewSkillID 分配一个技能标识（唯一入口）。
func NewSkillID() (string, error) { return idgen.New(skillIDPrefix) }

// newVersionID 分配一个版本标识（唯一入口）。
func newVersionID() (string, error) { return idgen.New(versionIDPrefix) }

// Usage 是一个技能在统计窗口内的使用量。
//
// **按"人日"计，不按次数计**：一次取用通常连着取好几个文件（正文加它引用的
// 说明），按次记的数字主要由"这个包有几个文件"决定，与"有没有人在用"无关。按
// 人日记还有一条它没有的性质——**它有天然上界**：一个主体对一个技能一天最多
// 贡献 1，想把它刷上去只能换更多的主体或等更多天。
type Usage struct {
	// UseDays 是窗口内有多少个人日用过它。
	UseDays int
	// Users 是窗口内去重之后的主体数。
	Users int
	// LastUsedAt 是最近一次使用的时间。零值表示从未被取用。
	LastUsedAt time.Time
}

// UsageDay 把一个时刻折成使用统计里的那一个"日"（唯一入口）。
//
// 两个实现都用它，因此"哪一天"这个判据不会因为库怎么存而不同（见
// internal/database 的 SkillUsageDailyRecord）。按**服务器本地时区**折算而不是
// UTC：使用量的读者看的是"昨天有没有人用"，而那个"昨天"是他所在的那一天——与
// 遥测的 TODAY 窗口同一个锚。
func UsageDay(t time.Time) string { return t.Format("2006-01-02") }
