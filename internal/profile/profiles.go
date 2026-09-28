package profile

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/identity"
	"github.com/poetlife/aladdin/internal/objectstore"
	"github.com/poetlife/aladdin/internal/rbac"
)

// IdentityLister 是本模块对身份模块的**全部**依赖。
//
// 取一个窄接口而不是具体的 *identity.Identities：本模块只需要"列出这个主体的
// 渠道"这一个查询，依赖具体类型会让测试与将来的替换都多一层无关的构造。
// 依赖方向单向——身份模块不感知本模块。
type IdentityLister interface {
	// List 返回该主体的全部渠道，顺序稳定（由身份模块定）。
	List(ctx context.Context, subjectID string) ([]identity.Identity, error)
}

// ProfilesDeps 是档案入口的依赖。
type ProfilesDeps struct {
	// Store 是档案的持久化存储。
	Store Store
	// Subjects 用于确认主体已登记——未登记的主体不得产生档案行。
	Subjects rbac.Store
	// Identities 供展示名回退的第二步使用。
	Identities IdentityLister
	// Avatars 为 nil 表示这个部署没有配置头像存储。这是合法且常见的：头像
	// 是可选能力，未配置时昵称与简介照常可用。
	//
	// 它的类型是**直传这条公共链路的契约**（见 internal/objectstore）：头像与
	// galaxy 资产消费同一个 Store，差别只在键、白名单与上限。
	Avatars objectstore.Store
	// Logger 记录"影响展示、但不值得让整个请求失败"的降级事件。为空时丢弃。
	Logger *zap.Logger
}

// View 是一个主体的档案在界面上要呈现的样子。
//
// DisplayName 与 AvatarURL 是**派生值**，不落库：前者把回退规则收在一处，
// 后者是短时地址，每次读取都要重新生成。
type View struct {
	Profile
	// DisplayName 是界面应当显示的名字。**客户端不得自行拼接**——回退规则
	// 只有这一处实现（见 docs/ssot-registry.md）。
	DisplayName string
	// AvatarURL 是短时有效的读取地址。空表示当前没有可显示的头像：可能是真
	// 的没设，也可能是头像存储未启用或暂时取不到地址。
	AvatarURL string
}

// Profiles 是档案的读写入口，也是**展示名回退规则的唯一实现**。
type Profiles struct {
	store      Store
	subjects   rbac.Store
	identities IdentityLister
	avatars    objectstore.Store
	logger     *zap.Logger
}

// NewProfiles 构造档案入口。
func NewProfiles(deps ProfilesDeps) *Profiles {
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Profiles{
		store:      deps.Store,
		subjects:   deps.Subjects,
		identities: deps.Identities,
		avatars:    deps.Avatars,
		logger:     logger,
	}
}

// AvatarUploadEnabled 报告这个部署是否配置了头像存储。
//
// 服务端用它填充下发给客户端的"头像功能是否可用"，从而让客户端不渲染一个
// 点了报错的控件（见 docs/design/profile/avatar-storage.md）。
func (p *Profiles) AvatarUploadEnabled() bool { return p.avatars != nil }

// Get 返回该主体在界面上的档案。
func (p *Profiles) Get(ctx context.Context, subjectID string) (View, error) {
	stored, err := p.load(ctx, subjectID)
	if err != nil {
		return View{}, err
	}
	display, err := p.displayName(ctx, subjectID, stored.Nickname)
	if err != nil {
		return View{}, err
	}
	return View{
		Profile:     stored,
		DisplayName: display,
		AvatarURL:   p.avatarURL(ctx, subjectID, stored.AvatarKey),
	}, nil
}

// Update 设置昵称与简介。传空串表示清空该项，此时展示名回退到下一条规则。
func (p *Profiles) Update(ctx context.Context, subjectID, nickname, bio string) (View, error) {
	// 去首尾空白放在长度校验之前：一个只由空格组成的昵称应当被判成"清空"，
	// 而不是"设了一个看不见的名字"。
	nickname = strings.TrimSpace(nickname)
	bio = strings.TrimSpace(bio)

	if utf8.RuneCountInString(nickname) > NicknameMaxRunes {
		return View{}, ErrNicknameTooLong
	}
	if utf8.RuneCountInString(bio) > BioMaxRunes {
		return View{}, ErrBioTooLong
	}
	if err := p.requireSubject(ctx, subjectID); err != nil {
		return View{}, err
	}
	if err := p.store.PutText(ctx, subjectID, nickname, bio, time.Now().UTC()); err != nil {
		return View{}, err
	}
	return p.Get(ctx, subjectID)
}

// BeginAvatarUpload 签发一次头像直传的凭证。
//
// **字节不经过本服务端**（见 docs/design/objectstore/README.md）：这里只做三件事
// ——校验声明的类型在白名单内、按声明的大小早退、把"只许写头像这一个键、类型与
// 大小受条件约束"的策略交给对象存储执行。
//
// 两次校验都不能省，理由是它们各自解决一件事：不校验类型，白名单就只剩服务端
// 的一句承诺；不校验大小，用户会把一个 50 MiB 的文件整份传上去只为了被拒。
// 而两者都不是最终边界——真正的边界在存储侧。
func (p *Profiles) BeginAvatarUpload(ctx context.Context, subjectID, declaredType string, declaredSize int64) (objectstore.Credential, error) {
	if p.avatars == nil {
		return objectstore.Credential{}, ErrAvatarUnavailable
	}
	if _, err := NormalizeAvatarType(declaredType); err != nil {
		return objectstore.Credential{}, err
	}
	if declaredSize > AvatarMaxBytes {
		return objectstore.Credential{}, fmt.Errorf("%w: 声明 %d 字节，上限 %d 字节",
			ErrAvatarTooLarge, declaredSize, AvatarMaxBytes)
	}
	if err := p.requireSubject(ctx, subjectID); err != nil {
		return objectstore.Credential{}, err
	}
	return p.avatars.IssueUpload(ctx, AvatarKey(subjectID), AvatarTypeRules())
}

// CommitAvatarUpload 提交一次头像上传：核对对象确实到了，把档案指向它。
//
// **顺序是先有对象、再有键。** 反过来的话，库里那一行会声称有一个不存在的
// 头像，表现为"图片一直显示不出来"；而按这个顺序，最坏的结果只是留下一个无从
// 被引用的孤儿对象——它没有功能影响（见 docs/design/profile/avatar-storage.md
// 的待定决策）。
func (p *Profiles) CommitAvatarUpload(ctx context.Context, subjectID string) (View, error) {
	if p.avatars == nil {
		return View{}, ErrAvatarUnavailable
	}
	if err := p.requireSubject(ctx, subjectID); err != nil {
		return View{}, err
	}
	key := AvatarKey(subjectID)
	// 核对共用一处：存在性、真实字节数、超限即删（见 objectstore.VerifyUploaded）。
	if _, err := objectstore.VerifyUploaded(ctx, p.avatars, key, AvatarMaxBytes); err != nil {
		switch {
		case errors.Is(err, objectstore.ErrObjectNotFound):
			return View{}, ErrAvatarObjectMissing
		case errors.Is(err, objectstore.ErrUploadTooLarge):
			return View{}, fmt.Errorf("%w: 上限 %d 字节", ErrAvatarTooLarge, AvatarMaxBytes)
		default:
			return View{}, err
		}
	}
	if err := p.store.PutAvatarKey(ctx, subjectID, key, time.Now().UTC()); err != nil {
		return View{}, err
	}
	return p.Get(ctx, subjectID)
}

// ClearAvatar 删除头像。没有头像时也成功（幂等）。
func (p *Profiles) ClearAvatar(ctx context.Context, subjectID string) (View, error) {
	if err := p.requireSubject(ctx, subjectID); err != nil {
		return View{}, err
	}

	// **先清库内的键。** 库内是"这个主体有没有头像"的权威：反过来的话，对象
	// 删掉了而键没清，那一行就会一直声称有一个取不到的头像。
	if err := p.store.PutAvatarKey(ctx, subjectID, "", time.Now().UTC()); err != nil {
		return View{}, err
	}

	if p.avatars != nil {
		// 对象删除失败**不影响**"档案已清空"这个结论，因此只留痕、不返回错误。
		// 代价是对象存储上可能留下一个孤儿对象，而它无从被引用、没有功能影响。
		if err := p.avatars.Delete(ctx, AvatarKey(subjectID)); err != nil {
			p.logger.Warn("删除头像对象失败，档案已清空",
				zap.String("subject_id", subjectID),
				zap.Error(err))
		}
	}
	return p.Get(ctx, subjectID)
}

// load 读档案；行不存在时返回一个只带主体标识的空档案。
//
// **行不存在不是错误。** 档案行是惰性创建的，而"没有这一行"与"有一行但每个
// 字段都为空"在展示上完全等价——把前者当成错误，会让每个刚登录、还没设过
// 档案的人看到一次失败。
func (p *Profiles) load(ctx context.Context, subjectID string) (Profile, error) {
	stored, err := p.store.Get(ctx, subjectID)
	switch {
	case err == nil:
		return stored, nil
	case errors.Is(err, ErrProfileNotFound):
		return Profile{SubjectID: subjectID}, nil
	default:
		return Profile{}, err
	}
}

// displayName 按回退规则算出展示名。这是该规则的**唯一实现**。
func (p *Profiles) displayName(ctx context.Context, subjectID, nickname string) (string, error) {
	if nickname != "" {
		return nickname, nil
	}
	// 身份模块缺席时（只服务机器凭证的部署）没有渠道可读标识可用，直接落到
	// 主体标识。这不是错误：回退的每一步都能独立地"没有数据"。
	if p.identities == nil {
		return subjectID, nil
	}
	// 第二步：取第一条非空的渠道可读标识。顺序由身份模块的排序定，本模块
	// 不另定一份——两处各排一次，必然在某个渠道组合上不一致。
	list, err := p.identities.List(ctx, subjectID)
	if err != nil {
		return "", err
	}
	for _, ident := range list {
		if ident.Display != "" {
			return ident.Display, nil
		}
	}
	// 第三步：连渠道标识都没有（未登记主体、或渠道没给可读标识）。
	// 显示内部标识不理想，但比显示一个空字符串好——后者在界面上看起来
	// 像是渲染坏了。
	return subjectID, nil
}

// avatarURL 生成短时读取地址。返回空串表示"当前没有可显示的头像"。
//
// 地址生成失败**不让整个档案读取失败**：昵称与简介与头像无关，为了一张图
// 让页头连名字都显示不出来，是拿次要功能去挡主要功能。失败留痕、以空地址
// 呈现，界面上表现为没有头像。
func (p *Profiles) avatarURL(ctx context.Context, subjectID, key string) string {
	if key == "" || p.avatars == nil {
		return ""
	}
	url, err := p.avatars.PresignGet(ctx, key, AvatarURLTTL)
	if err != nil {
		p.logger.Warn("生成头像读取地址失败，按没有头像处理",
			zap.String("subject_id", subjectID),
			zap.Error(err))
		return ""
	}
	return url
}

// requireSubject 确认主体已登记。
//
// 未登记的主体**不得产生档案行**：档案挂在主体上，而"这个主体存在"的唯一
// 入口是认证流程的登记动作（见 docs/design/persistence/schema.md）。少了这一步，
// 一份过期的会话就能在库里留下一条永远无人认领的档案。
func (p *Profiles) requireSubject(ctx context.Context, subjectID string) error {
	_, err := p.subjects.Subject(ctx, subjectID)
	return err
}
