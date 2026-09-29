package galaxy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/objectstore"
)

// 整组与单份的体积上限、文件数上限。
//
// 它们都是**常量，不是配置项**：做成配置项只会多出一种失败方式——两台部署上
// 同一个工程的上限不同，而"这份内容能不能发布"因此变成一个要看部署的问题。
const (
	// MaxTextBytes 是**单份**文本条目的字节上限。
	MaxTextBytes = 1 << 20
	// MaxFileSetBytes 是**整组**文本条目的字节上限（各项之和）。
	//
	// 它对的是**产物**的量，而不是磁盘上整站的量：资产（图片、视频、字体）
	// 不在其中——它们由各自的类别上限约束，且不进产物。
	MaxFileSetBytes = 16 << 20
	// MaxFiles 是整组的文件数上限。
	MaxFiles = 1000
	// maxPathBytes 是单条路径的长度上限。
	maxPathBytes = 512
)

var (
	// ErrSiteFormInvalid 表示形态取值不是两种之一。零值（未填）也在此列。
	ErrSiteFormInvalid = fmt.Errorf("工程形态不合法")

	// ErrEntryPathInvalid 表示一条条目路径不合形状。
	ErrEntryPathInvalid = fmt.Errorf("条目路径不合法")

	// ErrEntrySetInvalid 表示清单本身有问题：路径重复、来源缺失或多于一个、
	// 入口缺失。
	ErrEntrySetInvalid = fmt.Errorf("文件清单不合法")

	// ErrDigestInvalid 表示一个**声明的**内容摘要形状不合法。
	//
	// 摘要是内容对象的键，因此它的形状必须在被使用之前校验：一个含 `/`、
	// `..` 或控制字符的取值会把"按内容寻址"变成"按调用方给的路径写"。
	ErrDigestInvalid = fmt.Errorf("内容摘要的形状不合法")

	// ErrTextTooLarge 表示一份文本条目超过单份体积上限。
	ErrTextTooLarge = fmt.Errorf("单份文本超过大小上限")

	// ErrFileSetTooLarge 表示整组文本条目之和超过体积上限。
	ErrFileSetTooLarge = fmt.Errorf("整组内容超过大小上限")

	// ErrTooManyFiles 表示整组的文件数超过上限。
	ErrTooManyFiles = fmt.Errorf("文件数超过上限")

	// ErrContentObjectMissing 表示提交时那个内容对象还不存在。
	//
	// 它是"直传没完成"这一情形的结论（网络中断、页面被关掉）。**它不是故障**，
	// 而是一次可以重来的上传。
	ErrContentObjectMissing = fmt.Errorf("内容对象不存在，上传可能没有完成")
)

// ContentObjectKey 返回一条文本条目在**私有区**的对象键（唯一入口）。
//
// 它按**内容摘要**寻址，因此同一份字节在任何工程、任何版本、任何次发布里都
// 落在同一个键上——"这份字节是否已经存在"是一个只看键就能回答的问题。改一个
// 字只产生一个新对象。
//
// **键上不体现工程形态**：同一个摘要既可能是某个工程的 markdown，也可能是
// 另一个工程的 JS 或 CSS，因此同一个键在两种形态下长得完全一样。要判断一个
// 对象属于谁、拿来做什么，读库，不要读键。
func ContentObjectKey(projectID, digest string) string {
	return assetKeyPrefix + projectID + "/text/" + digest
}

// EntryKind 是一条条目的类别。它决定发布态怎么取字节。
type EntryKind string

const (
	// EntryKindText 是文本条目：字节是按内容摘要寻址的内容对象。
	EntryKindText EntryKind = "text"
	// EntryKindAsset 是资产条目：字节是资产库里的一个媒体文件。
	EntryKindAsset EntryKind = "asset"
)

// Entry 是文件组里的一条条目：一个路径，加上它的字节从哪来。
//
// 它的 JSON 形状就是清单列的序列化形状（见 internal/galaxy/gormstore）——列的
// 内容只是这条结构的数组，因此"清单长什么样"只有一处定义。
type Entry struct {
	// Path 是相对路径。它同时是公开地址的一部分。
	Path string `json:"path"`
	// Kind 是条目类别。它与 Digest / AssetID 二者之一一一对应。
	Kind EntryKind `json:"kind"`
	// Digest 是文本条目的内容摘要（Kind 为 EntryKindText 时非空）。
	Digest string `json:"digest,omitempty"`
	// AssetID 是资产条目的资产标识（Kind 为 EntryKindAsset 时非空）。
	AssetID string `json:"asset_id,omitempty"`
}

// Manifest 是一组具名文件的清单。
//
// **它没有字节。** 文件组落到库里就是一组「路径 → 内容摘要或资产标识」，工程
// 行、草稿行与版本行都不带内容本身——库只回答"有哪些、是什么"，字节一律交给
// 对象存储。
type Manifest []Entry

// Find 按路径取一条条目。
func (m Manifest) Find(path string) (Entry, bool) {
	for _, entry := range m {
		if entry.Path == path {
			return entry, true
		}
	}
	return Entry{}, false
}

// HasPath 判定一个路径是不是本清单里的一条条目。
//
// **这是发布态分派的集合成员测试**：只查表，不拼路径、不规范化、不碰文件
// 系统。`..`、重复斜杠、大小写与编码差异因此无从生效——路径在写入时就已被
// 形状约束限定死。
func (m Manifest) HasPath(path string) bool {
	_, ok := m.Find(path)
	return ok
}

// Assets 返回资产条目，按路径升序。
//
// **它是"这个版本引用了哪些资产"的唯一读取入口**，不解析任何文本。顺序固定
// 是为了让同样的输入得到同样的上架顺序与留痕，排障时可比。
func (m Manifest) Assets() []Entry {
	assets := make([]Entry, 0, len(m))
	for _, entry := range m {
		if entry.Kind == EntryKindAsset {
			assets = append(assets, entry)
		}
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Path < assets[j].Path })
	return assets
}

// AssetIDs 返回被引用的资产标识，去重且按路径序。
func (m Manifest) AssetIDs() []string {
	seen := make(map[string]bool, len(m))
	var ids []string
	for _, entry := range m.Assets() {
		if seen[entry.AssetID] {
			continue
		}
		seen[entry.AssetID] = true
		ids = append(ids, entry.AssetID)
	}
	return ids
}

// NormalizeManifest 校验并规范化一份清单（唯一入口）。
//
// 它回答的全是**形状**问题：路径合不合法、有没有重复、来源是不是恰好一个、
// 数量有没有超。它**不查资产是否存在、不读任何字节**——那些要走存储，属于
// 校验规则集合（见 validate.go），而"清单本身是否成型"是接收任何一份清单时
// 都要先问的问题。
//
// 返回的清单按路径升序：顺序固定让同样的输入得到同样的留痕与错误信息。
func NormalizeManifest(entries []Entry) (Manifest, error) {
	if len(entries) > MaxFiles {
		return nil, fmt.Errorf("%w: 当前 %d 个，上限 %d 个", ErrTooManyFiles, len(entries), MaxFiles)
	}
	manifest := make(Manifest, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if !ValidEntryPath(entry.Path) {
			return nil, fmt.Errorf("%w: %q", ErrEntryPathInvalid, entry.Path)
		}
		if seen[entry.Path] {
			return nil, fmt.Errorf("%w: 路径 %q 重复", ErrEntrySetInvalid, entry.Path)
		}
		seen[entry.Path] = true
		switch entry.Kind {
		case EntryKindText:
			if !IsContentDigest(entry.Digest) {
				return nil, fmt.Errorf("%w: 条目 %q 的摘要 %q", ErrDigestInvalid, entry.Path, entry.Digest)
			}
			if entry.AssetID != "" {
				return nil, fmt.Errorf("%w: 文本条目 %q 不得带资产标识", ErrEntrySetInvalid, entry.Path)
			}
		case EntryKindAsset:
			if entry.AssetID == "" {
				return nil, fmt.Errorf("%w: 资产条目 %q 缺少资产标识", ErrEntrySetInvalid, entry.Path)
			}
			if entry.Digest != "" {
				return nil, fmt.Errorf("%w: 资产条目 %q 不得带内容摘要", ErrEntrySetInvalid, entry.Path)
			}
		default:
			return nil, fmt.Errorf("%w: 条目 %q 的类别 %q 不是文本也不是资产", ErrEntrySetInvalid, entry.Path, entry.Kind)
		}
		manifest = append(manifest, entry)
	}
	sort.Slice(manifest, func(i, j int) bool { return manifest[i].Path < manifest[j].Path })
	return manifest, nil
}

// ValidEntryPath 判定一条条目路径合不合形状（唯一入口）。
//
// 规则（见 docs/design/galaxy/site-model.md）：相对路径、以 `/` 分隔、受限于
// URL 安全字符集、不含 `..`、不以 `/` 开头或结尾、不含空段。
//
// **字符集不是风格偏好，是地址的约束**：路径会成为公开地址的一部分。把字符集
// 收在 URL 的非保留字符里，等于顺带消掉"同一个文件有两种写法"
// （`%2E` 与 `.`）这条只能靠规范化去覆盖的问题——而规范化一旦缺席，集合成员
// 测试就会既容纳又漏掉某些写法。
func ValidEntryPath(entryPath string) bool {
	if entryPath == "" || len(entryPath) > maxPathBytes {
		return false
	}
	if strings.HasPrefix(entryPath, "/") || strings.HasSuffix(entryPath, "/") {
		return false
	}
	for _, segment := range strings.Split(entryPath, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for i := 0; i < len(segment); i++ {
			if !isPathByte(segment[i]) {
				return false
			}
		}
	}
	return true
}

// isPathByte 判定一个字节能不能出现在条目路径里。
//
// 它是 RFC 3986 的 unreserved 字符集（字母、数字、`-`、`.`、`_`、`~`）。刻意
// 不收 `%`：收了它就得处理百分号编码的等价性，而那是集合成员测试最容易被绕过
// 的地方。
func isPathByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '-' || b == '_' || b == '.' || b == '~':
		return true
	default:
		return false
	}
}

// contentTypeRules 是签发内容对象直传凭证用的类型规则（唯一入口）。
//
// **内容对象没有"声明类型"这一层**：它的下发类型由路径扩展名派生（见 site.go），
// 对象本身不承担类型语义。因此写入时用一个**中性类型**——顺带消掉"预签名地址
// 被直接打开时按 HTML 渲染"这条隐患。
func contentTypeRules() []objectstore.TypeRule {
	return []objectstore.TypeRule{{ContentType: objectstore.NeutralContentType, MaxBytes: MaxTextBytes}}
}

// BeginContentUpload 开始一次内容对象（文本条目）的上传。
//
// 它与资产共用直传链路，差别只有三处：键按内容摘要、类型是中性类型（不存在
// "声明"这一层）、不进公开区。
//
// **按内容摘要寻址提了一条硬性约束：仅当对象不存在时才允许写入。** 否则一个
// 伪造的摘要会落到另一个版本已经在用的键上，把那个对象改写掉——而那是"版本
// 不可变"的反面。因此：
//
//   - 对象已存在时**直接返回 already_exists**，不发凭证。这既省一次上传，也
//     让"改一个字只产生一个新对象"在接口上看得见；
//   - 发出去的凭证带禁止覆盖的写入条件，由对象存储执行。
//
// 第二个返回值 alreadyExists 为真时不需要上传，也不需要提交。
func (s *Service) BeginContentUpload(ctx context.Context, subjectID, projectID, digest string, declaredSize int64) (bool, objectstore.Credential, error) {
	if err := s.requireAssetStore(); err != nil {
		return false, objectstore.Credential{}, err
	}
	if _, err := OwnedProject(ctx, s.store, projectID, subjectID); err != nil {
		return false, objectstore.Credential{}, err
	}
	if !IsContentDigest(digest) {
		return false, objectstore.Credential{}, ErrDigestInvalid
	}
	// 按**声明**的大小早退：这是给用户的省事（别把一份过大的文件整个传上来只
	// 为了被拒），不是安全边界——真正的上限由存储侧按策略执行（见 contentTypeRules）。
	if declaredSize > MaxTextBytes {
		return false, objectstore.Credential{}, fmt.Errorf("%w: 声明 %d 字节，上限 %d 字节",
			ErrTextTooLarge, declaredSize, MaxTextBytes)
	}
	key := ContentObjectKey(projectID, digest)
	if _, err := s.assets.Head(ctx, key); err == nil {
		return true, objectstore.Credential{}, nil
	} else if !errors.Is(err, objectstore.ErrObjectNotFound) {
		return false, objectstore.Credential{}, err
	}
	credential, err := s.assets.IssueUpload(ctx, key, contentTypeRules(), true)
	if err != nil {
		return false, objectstore.Credential{}, err
	}
	return false, credential, nil
}

// CommitContentUpload 提交一次内容对象上传：读回对象、核对摘要。
//
// **摘要是寻址键，而服务端没有字节可以自己算它**——它由上传方声明。因此写入
// 之后必须读回核对一遍。
//
// 核对不过时删掉那个对象：**它的危害范围只有调用者自己的工程**。内容对象的键
// 带工程前缀（跨工程的字节共享是刻意不做的，见 spec 的待定决策），因此一个
// 工程拥有者最坏也只能弄坏自己的内容，而不是别人的。
func (s *Service) CommitContentUpload(ctx context.Context, subjectID, projectID, digest string) error {
	if err := s.requireAssetStore(); err != nil {
		return err
	}
	if _, err := OwnedProject(ctx, s.store, projectID, subjectID); err != nil {
		return err
	}
	if !IsContentDigest(digest) {
		return ErrDigestInvalid
	}
	key := ContentObjectKey(projectID, digest)
	data, err := s.assets.Read(ctx, key)
	if err != nil {
		if errors.Is(err, objectstore.ErrObjectNotFound) {
			return ErrContentObjectMissing
		}
		return err
	}
	if actual := ContentDigest(data); actual != digest {
		if deleteErr := s.assets.Delete(ctx, key); deleteErr != nil && s.logger != nil {
			s.logger.Warn("回滚内容对象失败，桶上可能出现错误的对象",
				zap.String("project_id", projectID),
				zap.String("digest", digest),
				zap.Error(deleteErr))
		}
		return fmt.Errorf("%w: 内容对象 %s", ErrDigestMismatch, digest)
	}
	if s.logger != nil {
		s.logger.Info("已提交内容对象",
			zap.String("project_id", projectID),
			zap.String("subject_id", subjectID),
			zap.String("digest", digest),
			zap.Int("bytes", len(data)))
	}
	return nil
}

// ErrDigestMismatch 表示一段字节与它声明的摘要不符。
//
// 它有两个发现点：内容对象在**提交**时读回核对，资产在**上架**到公开区时读回
// 核对（见 promote.go）。两处都是"摘要是寻址键，而服务端没有别的办法自己算它"。
var ErrDigestMismatch = fmt.Errorf("字节与声明的摘要不符")

// ContentURL 为一个文本条目签发编辑态的短时读取地址（唯一入口）。
//
// 编辑器读一份文件的原文与读一份资产是同一种下发：服务端给一个短时地址，客户端
// 直连取，服务端不代理字节。查看也一样——它不经过服务端转发。
func (s *Service) ContentURL(ctx context.Context, projectID string, entry Entry) (string, error) {
	if entry.Kind != EntryKindText {
		return "", fmt.Errorf("%w: 只有文本条目有内容对象", ErrEntrySetInvalid)
	}
	if s.assets == nil {
		return "", ErrAssetUnavailable
	}
	return s.assets.PresignGet(ctx, ContentObjectKey(projectID, entry.Digest), AssetURLTTL)
}
