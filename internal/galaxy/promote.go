package galaxy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// releaseKeyPrefix 是公开区对象键的前缀。
//
// 它是**常量，不是配置项**，理由与 assetKeyPrefix 相同。它与私有区的键
// `galaxy/<工程标识>/…` 在**同一个桶**里共存而不相撞：本前缀的第二段恒为
// `release`，而工程标识恒以 `prj_` 开头。
const releaseKeyPrefix = "galaxy/release/"

// MediaTypeTag 返回一个媒体类型在公开区键里的那一段。
//
// **公开区的键必须含内容类型，不能只有内容摘要。** 同一份字节可以以两种类型
// 出现（同一张图标成 PNG 或 JPEG），而一个对象只能带一个内容类型——只按摘要
// 寻址会让后一次上架覆盖前一次的类型，表现是某个页面拿到错的内容类型。
//
// 它是**从声明的类型派生的纯函数**：同一个类型在任何工程、任何次发布里都得到
// 同一个取值，因此"这份字节以这个类型是否已经上架"仍是一个只看地址就能回答的
// 问题。`/` 换成 `-` 是为了让它是一段路径而不是两级目录。
func MediaTypeTag(mediaType string) string {
	tag := strings.ToLower(strings.TrimSpace(mediaType))
	tag = strings.NewReplacer("/", "-", ";", "-", " ", "-", "\\", "-").Replace(tag)
	return strings.Trim(tag, "-")
}

// ReleaseObjectKey 返回一个已上架的资产在**公开区**的对象键（唯一入口）。
//
// 这一区按**（内容摘要，媒体类型）**寻址：同一份字节在任何工程、任何版本、
// 任何次发布里都落在同一个键上，因此"这份字节是否已经上架"是一个只看地址就能
// 回答的问题。
//
// **公开区里没有工程标识段**：跨工程共享同一份字节，也就不该带任何一个工程的
// 标识。私有区按归属切分、公开区按内容寻址，两者形状不同是因为它们是两种边界。
func ReleaseObjectKey(digest, mediaType string) string {
	return releaseKeyPrefix + digest + "/" + MediaTypeTag(mediaType)
}

// ErrPublicStoreUnavailable 表示公开区不可用。
//
// 与"发布功能未启用"分开：后者是配置决定的常态（前端据此不渲染入口），前者是
// 已配置但操作失败，属于故障。
var ErrPublicStoreUnavailable = errors.New("公开区不可用")

// ErrAssetDigestMismatch 表示一个资产的字节与它声明的内容摘要不符。
//
// 它只在**上架**这一步可能被发现：直传让服务端看不到字节，而摘要是上传方声明
// 的（见 docs/design/objectstore/README.md）。核对放在这里，是因为这里本来就要
// 把字节读回来搬到公开区——为了核对再提前读一次，等于把直传省下的带宽花回去。
var ErrAssetDigestMismatch = errors.New("资产字节与声明的摘要不符")

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

	// Put 把一个键对应的字节写进公开区，内容类型随对象一起写入，并把该对象
	// 设成**公开读**。
	//
	// 公开读是逐对象的：它不由桶级策略给出，只在这一次写入时设置。因此"哪些
	// 对象是公开的"完全由本方法的调用路径决定（见
	// docs/design/galaxy/asset-library.md）。
	Put(ctx context.Context, key, contentType string, data []byte) error
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
// 顺序是刻意的：**先看公开区有没有（有就跳过），再读私有区，核对摘要，最后写**。
// 跳过时连字节都不读；核对在写入之前，因此一条假的摘要不会在公开区留下对象。
func (s *Service) promoteAssets(ctx context.Context, assets []Asset) (PromoteOutcome, error) {
	outcome := PromoteOutcome{Referenced: len(assets)}
	if s.public == nil {
		return outcome, ErrPublicStoreUnavailable
	}
	for _, asset := range assets {
		key := ReleaseObjectKey(asset.Digest, asset.MediaType)
		exists, err := s.public.Exists(ctx, key)
		if err != nil {
			return outcome, err
		}
		if exists {
			outcome.Skipped++
			continue
		}
		data, err := s.assets.Read(ctx, AssetObjectKey(asset.ProjectID, asset.MediaKind, asset.ID))
		if err != nil {
			return outcome, err
		}
		// **摘要在这里被核对**：它是公开区地址的键，而它由上传方声明。核对不过
		// 就不写——否则公开区上那个地址会指向另一份内容，而它看起来完全正常。
		if actual := ContentDigest(data); actual != asset.Digest {
			return outcome, fmt.Errorf("%w: 资产 %s", ErrAssetDigestMismatch, asset.ID)
		}
		if err := s.public.Put(ctx, key, asset.MediaType, data); err != nil {
			return outcome, err
		}
		outcome.Promoted++
		outcome.Bytes += int64(len(data))
	}
	return outcome, nil
}

// MemoryPublicStore 是 PublicStore 的内存实现。
//
// **仅供测试。** 它记录被上架的对象，让"只上架被引用的资产"、"上架幂等"与
// "摘要不符即拒绝"这三条可以在离线断言。
type MemoryPublicStore struct {
	mu      sync.Mutex
	objects map[string]memoryPublicObject

	// PutErr 允许测试注入失败，用来构造"上架中断"这一检查点场景。
	PutErr error
	// Calls 记录每一次上架请求的目标键（含已存在的那些）。
	Calls []string
}

type memoryPublicObject struct {
	contentType string
	data        []byte
}

// NewMemoryPublicStore 构造一个空的公开区。
func NewMemoryPublicStore() *MemoryPublicStore {
	return &MemoryPublicStore{objects: map[string]memoryPublicObject{}}
}

// Exists 实现 PublicStore。
func (s *MemoryPublicStore) Exists(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.objects[key].data != nil, nil
}

// Put 实现 PublicStore。已存在时覆盖（幂等：同样的摘要对应同一份字节）。
func (s *MemoryPublicStore) Put(_ context.Context, key, contentType string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Calls = append(s.Calls, key)
	if s.PutErr != nil {
		return s.PutErr
	}
	stored := make([]byte, len(data))
	copy(stored, data)
	s.objects[key] = memoryPublicObject{contentType: contentType, data: stored}
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
