package identity

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"

	"github.com/poetlife/aladdin/internal/rbac"
)

// subjectEntropyBytes 是新主体标识的随机字节数。
//
// 128 位。主体标识**不参与信任决策**——拿到它也冒充不了谁，登录仍要过
// 渠道的校验——因此不需要"不可猜测"。取一个足够大的值，是为了让
// "分配即冻结、永不复用"在任何规模的库里都成立，而不依赖某处去查重。
const subjectEntropyBytes = 16

// subjectIDPrefix 是 aladdin 分配的主体标识的前缀。
//
// 它让日志与库里一眼能看出"这是认证流程分配的"，与运维手写进配置的
// 开发用主体（如种子数据的 `dev-user`）区分开。
const subjectIDPrefix = "usr_"

// Identities 是"一个渠道身份属于哪个主体"的唯一入口。
//
// 它同时承担主体的**登记**：那是认证流程的职责，授权面刻意不做那一步
// （见 docs/design/persistence/schema.md）。
//
// 它不读角色、不参与任何判定：判定在 internal/rbac。
type Identities struct {
	identities IdentityStore
	subjects   rbac.MutableStore
}

// NewIdentities 构造身份解析入口。
//
// subjects 需要可写，是因为解析未命中时要登记新主体；读回主体的当前属性
// 也走同一个抽象，不绕过它自己查库。
func NewIdentities(identities IdentityStore, subjects rbac.MutableStore) *Identities {
	return &Identities{identities: identities, subjects: subjects}
}

// ResolveOrRegister 解析一份**已校验**的渠道身份对应哪个主体。
//
// 命中已登记的身份，就用它指向的主体；没有命中，就登记一个新主体并记下
// 这条对应关系。这是"同一个人两次登录不会变成两个主体"的落点——它由
// （来源，身份标识）的唯一归属保证，而不是由主体标识的构造方式保证。
//
// 它**不做任何令牌校验，也不做任何归并**：调用方必须先完成确认
// （见 google_verifier.go），而"这个身份与那个主体是同一个人"在这里
// 永远不是一个可以推断的结论——只由登录与绑定两次动作写下来的事实决定。
func (i *Identities) ResolveOrRegister(ctx context.Context, source, externalID, display string) (rbac.Subject, error) {
	existing, err := i.identities.Lookup(ctx, source, externalID)
	switch {
	case err == nil:
		// 展示信息不在这里刷新：登录是读路径，读路径不写库——一次登录有
		// 没有副作用，应当只取决于"是否新登记了一个主体"。渠道侧改名后，
		// 界面上显示的邮箱会停在登记那一刻的值，而它不参与任何判定。
		return i.subjects.Subject(ctx, existing.SubjectID)
	case errors.Is(err, ErrIdentityNotFound):
		return i.register(ctx, source, externalID, display)
	default:
		return rbac.Subject{}, err
	}
}

// Bind 把一个渠道身份绑到某个主体上。
//
// **归属由调用方传入的主体决定，且必须已经确认过。** 令牌只证明"发起者
// 控制着这个身份"，不决定"这个身份应该属于谁"；本方法不做这个判断，
// 因为那需要"当前是谁在调用"这个上下文，而它属于服务端。
//
// 该身份已属于另一个主体时返回 ErrIdentityTaken——**不转移、不合并**。
// 转移意味着任何拿到该渠道令牌的人都能把别人的进入方式夺走一部分，
// 而这个动作在系统里与一次正常绑定没有区别。
func (i *Identities) Bind(ctx context.Context, subjectID, source, externalID, display string) error {
	return i.identities.Put(ctx, Identity{
		Source:     source,
		ExternalID: externalID,
		SubjectID:  subjectID,
		Display:    display,
	})
}

// Unbind 从主体上摘掉一个渠道身份。
//
// 摘掉之后该渠道不再通向这个主体：下次用它登录会按"未命中"登记出一个
// 新的、零权限的主体。这是预期行为，不是权限丢失。
//
// 两条不变式——"只能摘自己的"与"不能摘掉最后一个"——由存储在同一次
// 操作里保证，因此这里不先查后写：先查后写在并发下会同时被打破。
func (i *Identities) Unbind(ctx context.Context, subjectID, source, externalID string) error {
	removed, err := i.identities.Delete(ctx, source, externalID, subjectID)
	if err != nil {
		return err
	}
	if !removed {
		return ErrIdentityNotFound
	}
	return nil
}

// List 返回主体已绑定的全部身份，顺序稳定：按（来源，身份标识）。
func (i *Identities) List(ctx context.Context, subjectID string) ([]Identity, error) {
	list, err := i.identities.ListBySubject(ctx, subjectID)
	if err != nil {
		return nil, err
	}
	sortIdentities(list)
	return list, nil
}

// register 登记一个新主体，并把这条身份记到它名下。
func (i *Identities) register(ctx context.Context, source, externalID, display string) (rbac.Subject, error) {
	subjectID, err := newSubjectID()
	if err != nil {
		return rbac.Subject{}, err
	}
	// 新主体的默认作用域为空：默认作用域来自绑定关系，而它一条绑定也没有。
	// 于是这个人能登录、能看到界面框架，但什么都做不了——这是预期路径。
	subject := rbac.Subject{ID: subjectID, Type: rbac.SubjectTypeUser}

	// 先登记主体、再写身份。反过来的话，一次写入失败会留下一条指向不存在
	// 的主体的身份，而"未登记的主体在判定时视为不存在"：这个人每次登录都
	// 会被解析到一个判定看不见的主体上，且再也无法自愈。
	if err := i.subjects.PutSubject(ctx, subject); err != nil {
		return rbac.Subject{}, err
	}

	err = i.identities.Put(ctx, Identity{
		Source:     source,
		ExternalID: externalID,
		SubjectID:  subjectID,
		Display:    display,
	})
	if err == nil {
		return subject, nil
	}
	if !errors.Is(err, ErrIdentityTaken) {
		return rbac.Subject{}, err
	}

	// 并发下另一个请求抢先写下了同一条身份。让它赢——两次并发的首次登录
	// 本来就该落到同一个主体上。刚登记的那个主体成了没有任何身份引用的
	// 空壳：它没有任何绑定、也没有进入方式，留在库里无害，重查一次即可。
	winner, err := i.identities.Lookup(ctx, source, externalID)
	if err != nil {
		return rbac.Subject{}, err
	}
	return i.subjects.Subject(ctx, winner.SubjectID)
}

// newSubjectID 分配一个新主体的标识。
//
// 它**不由渠道标识派生，也不含任何渠道取值**：一个主体可以有多个渠道，
// 把渠道编进主体标识等于替这个人选定了一个。随机分配还让"两个来源各自
// 独立地分配出同一个字符串"不会把两个主体合成一个。
func newSubjectID() (string, error) {
	buf := make([]byte, subjectEntropyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("分配主体标识失败: %w", err)
	}
	return subjectIDPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// sortIdentities 让同一份数据在任何实现下得到同一个顺序。
//
// 顺序由这里定，不由存储的返回顺序定：内存实现按 map 遍历、SQL 实现按
// 它自己的次序返回，两者天然不同，而"列出来的渠道每次顺序都不一样"
// 会让界面看起来在抖动。
func sortIdentities(list []Identity) {
	sort.Slice(list, func(a, b int) bool {
		if list[a].Source != list[b].Source {
			return list[a].Source < list[b].Source
		}
		return list[a].ExternalID < list[b].ExternalID
	})
}
