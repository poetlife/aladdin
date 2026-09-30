package galaxy

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/poetlife/aladdin/internal/objectstore"
)

// releaseKeySegment 是公开区对象键里的一段，位于**工程标识之后**。
//
// 它是**常量，不是配置项**，理由与 assetKeyPrefix 相同。它与同一个工程下的
// `assets/` 与 `text/` 两段并列而不相撞（见 asset.go 与 content_set.go）。
const releaseKeySegment = "release"

// MediaTypeTag 返回一个媒体类型在公开区键里的那一段。
//
// **公开区的键必须含内容类型，不能只有内容摘要。** 同一份字节可以以两种类型
// 出现（同一张图标成 PNG 或 JPEG），而一个对象只能带一个内容类型——只按摘要
// 寻址会让后一次上架覆盖前一次的类型，表现是某个页面拿到错的内容类型。
//
// 它是**从声明的类型派生的纯函数**：同一个类型在任何工程、任何次发布里都得到
// 同一个取值，因此同一个工程里"这份字节以这个类型是否已经上架"仍是一个只看地址
// 就能回答的问题。`/` 换成 `-` 是为了让它是一段路径而不是两级目录。
func MediaTypeTag(mediaType string) string {
	tag := strings.ToLower(strings.TrimSpace(mediaType))
	tag = strings.NewReplacer("/", "-", ";", "-", " ", "-", "\\", "-").Replace(tag)
	return strings.Trim(tag, "-")
}

// ReleaseObjectKey 返回一个已上架的资产在**公开区**的对象键（唯一入口）。
//
// 形状是 `galaxy/<工程标识>/release/<内容摘要>/<类型标识>`：**先按工程切分，段内按
// （内容摘要，媒体类型）寻址**。因此"这个工程的这份字节以这个类型是否已经上架"是
// 一个只看地址就能回答的问题，而在**同一个工程内**重复发布同一个版本不产生新字节。
//
// **代价要说清楚：跨工程不再共享同一份字节。** 两个工程用到同一张底图是两份对象、
// 两份存储；换来的是"这些发布物属于哪个工程"在控制台里一眼可见，以及"按工程清理"
// 这条路留着（公开区孤儿对象的回收见 docs/design/galaxy/publication.md 的待定决策）。
//
// 键上带工程标识**不改变隔离**：公开与私有靠**逐对象的公开读**区分，不由键或桶
// 区分（见 docs/design/galaxy/asset-library.md）。`release` 那一段仍然让一个键一眼
// 看出它是不是公开区的东西。
func ReleaseObjectKey(projectID, digest, mediaType string) string {
	return assetKeyPrefix + projectID + "/" + releaseKeySegment + "/" + digest + "/" + MediaTypeTag(mediaType)
}

// ErrPublicStoreUnavailable 表示公开区不可用。
//
// 与"发布功能未启用"分开：后者是配置决定的常态（前端据此不渲染入口），前者是
// 已配置但操作失败，属于故障。
var ErrPublicStoreUnavailable = errors.New("公开区不可用")

// PublicStore 是**公开区唯一的写入口**。
//
// 它只有两个动作，且都不含判断：判断（该不该上架、字节对不对）在上层。
// 这与"存储保持哑"是同一条取向——把校验放进存储，两种实现就会各写一遍。
//
// 键由上层给出（`ReleaseObjectKey`），与私有区的 Store 同形：**键规则属于各
// 模块，存储只管把字节放到给定的键上**。公开区与私有区在同一个桶里，两者的
// 区别不在桶，而在键与**写入时的权限**。
//
// 接口上没有"删除"：上架后不做回收——撤回发布或被后续发布取代时，先前上架的
// 公开副本留在原处。召回它需要一次对账，那是另一类运维职责。
type PublicStore interface {
	// Exists 判定公开区里是否已有该键的对象。
	//
	// 按内容摘要寻址让"这份字节是否已经上架"成为一个只看地址就能回答的
	// 问题，因此重复发布不产生新字节。
	Exists(ctx context.Context, key string) (bool, error)

	// Copy 把一个**私有区**的对象按给定的键复制进公开区，内容类型以传入的那个
	// 为准，并把复制过去的对象设成**公开读**。
	//
	// **它复制而不是接收字节。** 公开区与私有区是同一个桶，因此这件事由存储自己
	// 在一次请求里做完，字节不过境（见 cosupload.PublicWriter.Copy）。接口收字节
	// 的版本会让调用方不得不把字节读回内存——那正是这次要拆掉的那一段。
	//
	// 公开读是逐对象的：它不由桶级策略给出，只在这一次写入时设置。因此"哪些
	// 对象是公开的"完全由本方法的调用路径决定（见
	// docs/design/galaxy/asset-library.md）。
	Copy(ctx context.Context, srcKey, dstKey, contentType string) error
}

// PromoteOutcome 是一次资产上架的统计，用于留痕。
//
// 这几个数字就是 spec 要求的"上架阶段的完成判据"：该版本引用的**每一个**资产
// 在公开区都存在，而其中多少是新搬的、多少是本来就在的。
type PromoteOutcome struct {
	Referenced int
	Promoted   int
	Skipped    int
	Bytes      int64
}

// promoteAssets 把该版本引用到的资产上架到公开区。
//
// **只上架被引用的资产，不搬整个资产库**：一个工程可能有几百个素材而某个页面只
// 用了三个，整体搬运会让一次发布的时间与成本取决于一个与它无关的数字。
//
// 顺序是刻意的：**先看公开区有没有（有就跳过），再让存储把私有区那份复制过去。**
// 跳过时连一次复制都不发，因此重复发布同一个版本不产生新字节。
//
// **字节不经过服务端**：复制由对象存储在同一区域内完成（见 PublicStore.Copy）。
// 与之相随的一点变化是**摘要不再在这里核对**——它挪到了提交那一步（见
// asset.go 的 CommitAssetUpload）：那时对象刚上传完，核对是一次哈希调用而不是
// 一趟字节。这与文本条目早先的做法同形，两处由此一致。
func (s *Service) promoteAssets(ctx context.Context, assets []Asset) (PromoteOutcome, error) {
	outcome := PromoteOutcome{Referenced: len(assets)}
	if s.public == nil {
		return outcome, ErrPublicStoreUnavailable
	}
	for _, asset := range assets {
		key := ReleaseObjectKey(asset.ProjectID, asset.Digest, asset.MediaType)
		exists, err := s.public.Exists(ctx, key)
		if err != nil {
			return outcome, err
		}
		if exists {
			outcome.Skipped++
			continue
		}
		srcKey := AssetObjectKey(asset.ProjectID, asset.MediaKind, asset.ID)
		if err := s.public.Copy(ctx, srcKey, key, asset.MediaType); err != nil {
			return outcome, err
		}
		outcome.Promoted++
		// 字节数取自库内的行：上架这条路已经不经过字节了，而这个数字只是留痕，
		// 不值得为它再问一次存储。
		outcome.Bytes += asset.SizeBytes
	}
	return outcome, nil
}

// MemoryPublicStore 是 PublicStore 的内存实现。
//
// **仅供测试。** 它记录被上架的对象，让"只上架被引用的资产"与"上架幂等"这两条
// 可以在离线断言。
//
// 它**持有私有区**：生产里公开区与私有区是同一个桶，复制就是从那里取字节。测试
// 夹具照这个形状接（见 fixture_test.go），两边由此只差"公开读设没设"。
type MemoryPublicStore struct {
	mu      sync.Mutex
	source  *objectstore.MemoryStore
	objects map[string]memoryPublicObject

	// CopyErr 允许测试注入失败，用来构造"上架中断"这一检查点场景。
	CopyErr error
	// Calls 记录每一次上架请求的目标键（含已存在的那些）。
	Calls []string
}

type memoryPublicObject struct {
	contentType string
	data        []byte
}

// NewMemoryPublicStore 构造一个空的公开区，复制源是给定的私有区。
func NewMemoryPublicStore(source *objectstore.MemoryStore) *MemoryPublicStore {
	return &MemoryPublicStore{source: source, objects: map[string]memoryPublicObject{}}
}

// Exists 实现 PublicStore。
func (s *MemoryPublicStore) Exists(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.objects[key].data != nil, nil
}

// Copy 实现 PublicStore：从私有区取那份字节，写进公开区。
//
// 已存在时覆盖（幂等：同样的摘要对应同一份字节）。
func (s *MemoryPublicStore) Copy(ctx context.Context, srcKey, dstKey, contentType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Calls = append(s.Calls, dstKey)
	if s.CopyErr != nil {
		return s.CopyErr
	}
	data, err := s.source.Read(ctx, srcKey)
	if err != nil {
		return err
	}
	stored := make([]byte, len(data))
	copy(stored, data)
	s.objects[dstKey] = memoryPublicObject{contentType: contentType, data: stored}
	return nil
}

// Count 返回公开区里的对象个数。
func (s *MemoryPublicStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.objects)
}

// Keys 返回公开区里的全部对象键，供测试断言"只上架了被引用的那些"。
func (s *MemoryPublicStore) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	keys := make([]string, 0, len(s.objects))
	for key := range s.objects {
		keys = append(keys, key)
	}
	return keys
}

// Object 返回公开区里某个键对应的字节，供测试断言"搬过去的正是那一份"。
func (s *MemoryPublicStore) Object(key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored, ok := s.objects[key]
	if !ok {
		return nil, ErrAssetNotFound
	}
	out := make([]byte, len(stored.data))
	copy(out, stored.data)
	return out, nil
}

var _ PublicStore = (*MemoryPublicStore)(nil)
