// Package objectstore 是"用户上传的字节怎么进对象存储"这一公共机制的唯一实现。
//
// 它被个人档案（头像）与 galaxy（资产）两处消费。两处的差别只有三样：**对象键、
// 类型白名单、大小上限**；而"签发—直传—提交"这条链路、以及它承担的安全性质
// （只允许写、钉死一个键、类型与大小由存储侧强制），只在这里实现一次。
//
// 为什么不是"字节经服务端转存"：那会让同一份字节被收一遍再送出去一遍，并在
// 服务端内存里放一份完整副本（资产上限 100 MiB，并发上传时线性叠加）。见
// docs/design/objectstore/README.md。
//
// **本包不解析任何模块的业务**：它不知道"谁是工程拥有者"，也不知道"这个键属于
// 谁"。鉴权、归属、白名单与上限的**判定**都在消费方，本包只负责把它们写进策略
// 并交给存储执行。
package objectstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	// ErrObjectNotFound 表示这个键上还没有对象。
	//
	// **它不是故障。** 提交之前、或者一个从未提交的上传，桶上就是没有东西；
	// 它与"存储不可用"必须分开，否则一次数据库式的抖动会被表现成"我的图上
	// 传失败了"。
	ErrObjectNotFound = errors.New("对象不存在")

	// ErrStoreUnavailable 表示对象存储不可用。
	ErrStoreUnavailable = errors.New("对象存储不可用")

	// ErrUploadTooLarge 表示提交时对象超过允许的字节数。
	//
	// 它发生在**存储侧已经放过一次之后**：策略按声明的类型卡了长度，而这里是
	// 服务端拿真实字节数得出的自己的结论。两者都必要——存储侧那条是边界，
	// 这一条是写进元数据的那个数字的来源。
	ErrUploadTooLarge = errors.New("对象超过大小上限")
)

// VerifyUploaded 核对一次直传的结果（唯一入口）。
//
// 两个模块的提交只差"上限是多少"，因此这三件事只实现一处：
//
//  1. 对象必须存在——不存在就是"上传没完成"（网络中断、页面被关掉），
//     它是一个可以重来的结论，不是故障；
//  2. 字节数必须在上限内；
//  3. **超过上限的对象被删掉**——它是这次失败唯一的残留物，留着既无从被引用，
//     又占着配额。
//
// 它不写任何元数据：元数据行长什么样是两个模块各自的事（档案行上是一个键，
// 资产表上是一行）。
func VerifyUploaded(ctx context.Context, store Store, key string, maxBytes int64) (ObjectStat, error) {
	stat, err := store.Head(ctx, key)
	if err != nil {
		return ObjectStat{}, err
	}
	if stat.SizeBytes > maxBytes {
		if deleteErr := store.Delete(ctx, key); deleteErr != nil {
			return ObjectStat{}, fmt.Errorf("%w: 删除超限对象失败: %w", ErrStoreUnavailable, deleteErr)
		}
		return ObjectStat{}, fmt.Errorf("%w: 实际 %d 字节，上限 %d 字节", ErrUploadTooLarge, stat.SizeBytes, maxBytes)
	}
	return stat, nil
}

// Credential 是一次直传所需的全部取值。
//
// 它是**一份短时凭证**：在有效期内，拿到它的人可以往 Key 上写一次。因此它只
// 下发给发起上传的那个客户端，且**不得进日志**——把它写进日志等于把一次写入
// 的能力留在了日志文件里。
//
// 它**只能写**：策略里只允许写入动作。能取字节的凭证是另一回事（见
// Store 的读取地址），那条路径不经过本结构。
type Credential struct {
	// Bucket 是桶名（含 APPID），不是地址：客户端 SDK 要的是桶名与地域。
	Bucket string
	// Region 是桶所在地域，形如 ap-guangzhou。
	Region string
	// Key 是这次唯一允许写入的对象键，由服务端分配。
	Key string
	// SecretID / SecretKey / SessionToken 是临时凭证三元组。
	SecretID     string
	SecretKey    string
	SessionToken string
	// ExpiresAt 是凭证的失效时刻。
	ExpiresAt time.Time
	// ForbidOverwrite 要求写入时**禁止覆盖已存在的对象**。
	//
	// 内容对象（galaxy 的文本条目）按内容摘要寻址，因此"仅当不存在时写入"是
	// 版本不可变的前提。客户端据此在写入请求上带禁止覆盖的条件，由对象存储
	// 执行；服务端在提交时再读回核对一次摘要。
	ForbidOverwrite bool
}

// NeutralContentType 是**内容对象**（galaxy 的文本条目）写入时声明的类型。
//
// 文本条目的下发类型由**路径**决定（扩展名 → 按形态给出的白名单），对象本身
// 不承担类型语义——因此同一个内容对象以两种扩展名出现时是同一个对象，这正是
// 想要的。中性类型顺带消掉"预签名地址被直接打开时按 HTML 渲染"这条隐患。
const NeutralContentType = "application/octet-stream"

// TypeRule 是"某一种内容类型 + 它的大小上限"。
//
// **上限按类型分档**，因为"一张图"与"一段视频"的合理体积差两个数量级。它是
// 策略的最小单位：一条规则就是策略里一条允许项，因此**类型与它对应的大小上限
// 不可能是两个来源**——用视频的上限去卡图片这件事在策略里写不出来。
type TypeRule struct {
	// ContentType 是一个具体的内容类型，如 "image/png"。
	//
	// 刻意用具体值而不是通配（如 "image/*"）：白名单是各模块声明的那个集合，
	// 通配会让策略允许的范围**大于**白名单，而两者的差集正是"存储放进来了、
	// 但我们的规则里没有这个类型"这类只能靠现象反推的问题。
	ContentType string
	// MaxBytes 是这一类型允许的最大字节数。
	MaxBytes int64
}

// ObjectStat 是一个对象在存储上的事实。
type ObjectStat struct {
	SizeBytes int64
}

// Store 是对象存储在本机制里暴露的全部能力。
//
// **签发是唯一一条会产生对外凭证的路径**，因此它的形状被刻意收窄：只接受一个
// 键与一组类型规则。调用方给不了别的键，也给不了"什么都能写"的取值。
type Store interface {
	// IssueUpload 为一个键签发短时写入凭证，并把这组类型规则写进存储侧的
	// 策略：类型不在其中、或长度超过该类型上限的上传会被存储直接拒绝。
	//
	// key 的分配权在调用方（它是各模块的键规则），但一次上传一个**新键**是
	// 消费方必须守住的约定：复用键会让"替换已有对象"重新变成可能。
	// forbidOverwrite 进一步把这条约定交给存储执行，供**按内容摘要寻址**的
	// 内容对象使用（见 Credential.ForbidOverwrite）。
	IssueUpload(ctx context.Context, key string, rules []TypeRule, forbidOverwrite bool) (Credential, error)

	// Head 返回该键上对象的事实，不存在时返回 ErrObjectNotFound。
	//
	// 提交路径用它核对"上传真的完成了"，并取回真实字节数。
	Head(ctx context.Context, key string) (ObjectStat, error)

	// Read 读出该键上的字节。
	//
	// **它只有一个合法用途：把私有区对象上架到公开区**（发布流程的一部分，
	// 见 internal/galaxy/promote.go）。下发不走它——下发走 PresignGet，服务端
	// 不代理媒体带宽。
	Read(ctx context.Context, key string) ([]byte, error)

	// Delete 删除该键上的对象。对象不存在时也成功（幂等）。
	Delete(ctx context.Context, key string) error

	// Put 把一个对象写进**私有区**，内容类型随对象一起写入。
	//
	// **它只为 galaxy 的内容对象而存在**：那些字节在发布时由服务端生成
	// （渲染与记号替换的结果），因此没法像资产那样让客户端直传。头像与资产
	// 都不走它——它们一律直传。
	//
	// 调用方必须自己保证"仅当不存在时写入"（先 Head 再 Put）：对象存储的条件
	// 写只对客户端签名请求可用，而这是服务端自己的请求。这条约定由
	// galaxy.writeContentObject 守着。
	Put(ctx context.Context, key, contentType string, data []byte) error

	// PresignGet 签发一个短时有效的读取地址，ttl 由调用方给定。
	//
	// **它不与存储交互**：预签名是对"地址 + 密钥 + 有效期"做一次签名，对象
	// 存不存在要到真正取的时候才知道。
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// NormalizeContentType 把上传方声明的类型归一成 `type/subtype` 的小写形式。
//
// 归一化是**同一个动作**，两个消费方（头像、资产）都要做，因此只实现一处：
// 一处去掉 `;` 之后的参数、另一处不去掉的表现是"同一个文件换个写法就被拒绝"。
//
// 它只做归一，不判定：一个类型能不能收由各模块的白名单决定。
func NormalizeContentType(declared string) string {
	value := strings.TrimSpace(declared)
	if semicolon := strings.IndexByte(value, ';'); semicolon >= 0 {
		value = value[:semicolon]
	}
	return strings.ToLower(strings.TrimSpace(value))
}

// IssuedUpload 记录一次签发，供测试断言"我们发出的策略是什么"。
//
// **它只存在于内存实现上。** 生产实现不保留签发明细：凭证本身是短时的一次性
// 东西，留一份明细等于在服务端多存一份可用的写入能力。
type IssuedUpload struct {
	Key   string
	Rules []TypeRule
}

// MemoryStore 是 Store 的内存实现。
//
// **仅供测试。** 它**不模拟**对象存储的行为：签发不产生真的凭证、规则也不被
// 执行。它做的是三件事——记录签发了什么（规则是否被正确构造）、让测试模拟
// 客户端把字节传上去（Put）、以及让后续的 Head/Read/Delete 有东西可查。
//
// "桶真的照做了策略"只能在部署后冒烟里验（见 spec 的可验证性一节）。这个边界
// 必须留在明处，否则会得到一份"测过了"的错觉。
type MemoryStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	issued  []IssuedUpload

	// IssueErr 允许测试注入签发失败。
	IssueErr error
}

// NewMemoryStore 构造一个空的内存存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{objects: map[string][]byte{}}
}

// SimulateUpload 模拟客户端完成一次直传（只存在于内存实现上）。
//
// 它刻意不叫 Put：Put 是 Store 接口上"服务端自己写对象"的那个方法，而这是
// 测试里替客户端把字节放上去的动作。两者混名会让"服务端有没有绕过直传"这个
// 问题变得难以回答。
func (s *MemoryStore) SimulateUpload(key string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.objects[key] = data
}

// Issued 返回历次签发的记录（只存在于内存实现上）。
func (s *MemoryStore) Issued() []IssuedUpload {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]IssuedUpload, len(s.issued))
	copy(out, s.issued)
	return out
}

// Count 返回私有区里的对象个数（只存在于内存实现上）。
//
// 它让"重复发布不产生新对象"这类断言可以离线验证：内容对象按摘要共享，因此
// 这个数字在第二次发布之后不该变。
func (s *MemoryStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.objects)
}

// IssueUpload 实现 Store。
func (s *MemoryStore) IssueUpload(_ context.Context, key string, rules []TypeRule, forbidOverwrite bool) (Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.IssueErr != nil {
		return Credential{}, s.IssueErr
	}
	s.issued = append(s.issued, IssuedUpload{Key: key, Rules: append([]TypeRule(nil), rules...)})
	return Credential{
		Bucket:          "memory-bucket-1250000000",
		Region:          "ap-guangzhou",
		Key:             key,
		SecretID:        "memory-secret-id",
		SecretKey:       "memory-secret-key",
		SessionToken:    "memory-session-token",
		ExpiresAt:       time.Now().Add(memoryCredentialTTL),
		ForbidOverwrite: forbidOverwrite,
	}, nil
}

// memoryCredentialTTL 与生产实现取同一量级，好让"凭证是短时的"这条在两种实现
// 上是同一个结论。
const memoryCredentialTTL = 30 * time.Minute

// Head 实现 Store。
func (s *MemoryStore) Head(_ context.Context, key string) (ObjectStat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, ok := s.objects[key]
	if !ok {
		return ObjectStat{}, ErrObjectNotFound
	}
	return ObjectStat{SizeBytes: int64(len(data))}, nil
}

// Read 实现 Store。
func (s *MemoryStore) Read(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, ok := s.objects[key]
	if !ok {
		return nil, ErrObjectNotFound
	}
	out := make([]byte, len(data))
	copy(out, data)
	return out, nil
}

// Delete 实现 Store。对象不存在时也成功。
func (s *MemoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.objects, key)
	return nil
}

// Put 实现 Store。
func (s *MemoryStore) Put(_ context.Context, key, _ string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored := make([]byte, len(data))
	copy(stored, data)
	s.objects[key] = stored
	return nil
}

// PresignGet 实现 Store。
//
// 与生产实现一致：**不检查对象是否存在**，直接给出地址——预签名的性质就是
// 如此，而"下发的是地址而不是字节"这一点在两种实现上是同一个结论。
func (s *MemoryStore) PresignGet(_ context.Context, key string, ttl time.Duration) (string, error) {
	return "memory://" + key + "?ttl=" + ttl.String(), nil
}

var _ Store = (*MemoryStore)(nil)
