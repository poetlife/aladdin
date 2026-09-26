package profile

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
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
	// 只约束字节数，不约束像素尺寸：后者需要引入图像解码依赖，而这一层
	// 要挡住的是"往一个展示字段里塞大文件"，不是"图太大屏放不下"。
	AvatarMaxBytes = 2 * 1024 * 1024

	// AvatarURLTTL 是下发地址的有效期。
	//
	// 取分钟量级：地址是一份短期凭证，有效期越长，"本人把它转出去"这件事
	// 的代价就越大。过期后重新读取档案即可拿到新地址。
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

	// ErrAvatarTypeNotAllowed 表示字节不是一张可接受的图片。
	ErrAvatarTypeNotAllowed = errors.New("头像必须是 PNG、JPEG 或 GIF")
)

// avatarAllowedTypes 是可接受的头像类型白名单。
//
// 只收这三种，都是"无法携带可执行内容"的位图格式。刻意不收 SVG：它是 XML，
// 可以内嵌脚本；即便放在 <img> 里不执行，把它纳入白名单也等于给将来某次
// "改成直接打开"留下一颗雷。
var avatarAllowedTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
}

// AvatarKey 返回一个主体的头像在对象存储里的对象键。**唯一入口。**
//
// 一个主体一个键：替换头像就是原地覆盖，既不产生孤儿对象，也让一张旧的取图
// 地址在过期前指向新头像，而不是泄露被替换掉的那张。
func AvatarKey(subjectID string) string {
	return avatarKeyPrefix + subjectID
}

// SniffAvatarType 从字节本身判定头像类型。**唯一入口。**
//
// **上传方不声明类型，这是有意的。** 让上传方声明，等于把一个安全属性交给
// 它自证：一段脚本可以顶着 image/png 存进去，之后以一个看起来合法的地址被
// 分发。去掉"声明"这个字段还顺带消掉了一个问题——声明与实际不符时以谁为准。
func SniffAvatarType(data []byte) (string, error) {
	// 用标准库的嗅探而不是自己认魔数：那是一份被反复审过的实现，自己写
	// 一份只会多出一处可能与它对不上的地方。
	contentType := http.DetectContentType(data)
	if !avatarAllowedTypes[contentType] {
		return "", ErrAvatarTypeNotAllowed
	}
	return contentType, nil
}

// AvatarStore 是头像字节的存放与下发抽象。
//
// 接口上**没有"读字节"这个方法**：下发走预签名地址，服务端不代理图片带宽
// （见 docs/design/profile/avatar-storage.md）。因此测试只需要一个能给出
// 地址的假实现，不必模拟对象存储的读路径。
type AvatarStore interface {
	// Put 写入或覆盖该主体的头像。覆盖写是原地替换。
	//
	// contentType 由调用方用 SniffAvatarType 判定后传入——存储层不自己判
	// 类型，否则内存实现与生产实现会各判一遍，而"哪个才算数"只能靠比对代码
	// 回答。
	Put(ctx context.Context, subjectID, contentType string, data []byte) error

	// Delete 删除该主体的头像。对象不存在时也成功（幂等）。
	Delete(ctx context.Context, subjectID string) error

	// PresignGet 返回一个短时有效的读取地址，ttl 由调用方给定。
	//
	// **它不与存储交互**：预签名是对"地址 + 密钥 + 有效期"做一次签名，对象
	// 存不存在要到真正取的时候才知道。因此对一个不存在的对象它也会给出地址
	// ——这不是缺陷，而是预签名这个机制本身的性质。
	PresignGet(ctx context.Context, subjectID string, ttl time.Duration) (string, error)
}

// MemoryAvatarStore 是 AvatarStore 的内存实现。
//
// **仅供测试。** 生产路径上不得出现它：进程内的一份头像副本重启即丢，而它
// 不会以"丢了"的形式暴露，只会表现为"图片显示不出来"。
type MemoryAvatarStore struct {
	mu      sync.RWMutex
	objects map[string]memoryAvatar
}

type memoryAvatar struct {
	contentType string
	data        []byte
}

// NewMemoryAvatarStore 构造一个空的头像存储。
func NewMemoryAvatarStore() *MemoryAvatarStore {
	return &MemoryAvatarStore{objects: map[string]memoryAvatar{}}
}

// Put 实现 AvatarStore。
func (s *MemoryAvatarStore) Put(_ context.Context, subjectID, contentType string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.objects[subjectID] = memoryAvatar{contentType: contentType, data: data}
	return nil
}

// Delete 实现 AvatarStore。对象不存在时也成功。
func (s *MemoryAvatarStore) Delete(_ context.Context, subjectID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.objects, subjectID)
	return nil
}

// PresignGet 实现 AvatarStore。
//
// 与生产实现一致：**不检查对象是否存在**，直接给出地址。测试据此断言"下发的
// 是地址而不是字节"，而这一点在两种实现上是同一个结论。
func (s *MemoryAvatarStore) PresignGet(_ context.Context, subjectID string, ttl time.Duration) (string, error) {
	return "memory://" + AvatarKey(subjectID) + "?ttl=" + ttl.String(), nil
}

// Object 返回已存对象的类型与字节，供测试断言用。
//
// 它只存在于内存实现上：生产实现刻意不提供读字节的能力，那正是"服务端不
// 代理图片"这条约束在类型上的体现。
func (s *MemoryAvatarStore) Object(subjectID string) (contentType string, data []byte, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stored, ok := s.objects[subjectID]
	if !ok {
		return "", nil, false
	}
	return stored.contentType, stored.data, true
}

var _ AvatarStore = (*MemoryAvatarStore)(nil)
