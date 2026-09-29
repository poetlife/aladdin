package galaxy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/objectstore"
)

const (
	// assetKeyPrefix 是私有区对象键的前缀。
	//
	// 它是**常量，不是配置项**：做成配置项只会多出一种失败方式——改了前缀，
	// 存量资产在一瞬间全部变成孤儿。它与头像的 avatars/ 前缀在同一个桶里
	// 共存，两者互不干扰。
	assetKeyPrefix = "galaxy/"

	// ImageMaxBytes 是图片的字节上限。
	//
	// 三档上限的理由是"一张图"与"一段视频"的合理体积差两个数量级：用同一个上限
	// 卡两者，要么放过一张过大的图，要么拒掉一段正常的视频。上限只约束字节数，
	// 不含像素尺寸或时长。
	ImageMaxBytes = 10 << 20
	// AudioMaxBytes 是音频的字节上限。
	AudioMaxBytes = 20 << 20
	// VideoMaxBytes 是视频的字节上限。
	VideoMaxBytes = 100 << 20
	// FontMaxBytes 是字体的字节上限。
	//
	// 字体很小，但它是**随构建产物进来的那一类**：任何工具产出的整站几乎都会
	// 带一份 webfont，而在文件组里它必然是二进制，因此只能作为资产。
	FontMaxBytes = 5 << 20

	// AssetURLTTL 是编辑态下发地址的有效期。
	//
	// 逐字沿用头像的下发模型（见 docs/design/profile/avatar-storage.md）：
	// 它在有效期内是一份**谁拿到都能用的凭证**，因此未发布的资产里不得承载
	// 秘密。取分钟量级，过期后重新读取资产清单即得到新地址。
	AssetURLTTL = 10 * time.Minute

	// filenameMaxRunes 是原始文件名的展示上限。
	//
	// 它只是一个标签，但标签也会进库与界面：不设上限等于让一个客户端决定的
	// 长度进入每一行资产记录。
	filenameMaxRunes = 255
)

var (
	// ErrAssetUnavailable 表示这个部署没有配置私有桶。
	//
	// **它不是故障**，而是"这个能力没开"：工程与版本照常可用，前端据此不
	// 渲染上传入口。
	ErrAssetUnavailable = errors.New("资产功能未启用")

	// ErrAssetNotFound 表示这个工程下没有这个资产。
	//
	// 它与"资产属于别的工程"是同一个结论：区分它们会给出一个跨工程的探测
	// 面（这个标识是否存在），而用户拿不到别的工程的标识。
	ErrAssetNotFound = errors.New("资产不存在")

	// ErrAssetTypeNotAllowed 表示声明的类型不在白名单内。
	ErrAssetTypeNotAllowed = errors.New("资产类型不在允许的范围内")

	// ErrAssetTooLarge 表示资产超过它所属类别的字节上限。
	ErrAssetTooLarge = errors.New("资产超过该类别的上限")

	// ErrAssetObjectMissing 表示提交时那个键上还没有对象。
	//
	// 它是"直传没完成"这一情形的结论（网络中断、页面被关掉）。**它不是故障**，
	// 而是一次可以重来的上传。
	ErrAssetObjectMissing = errors.New("资产对象不存在，上传可能没有完成")

	// ErrAssetReferenced 表示资产仍被某个版本引用，因此不能删。
	ErrAssetReferenced = errors.New("资产仍被版本引用")
)

// MediaKind 是资产按媒体分出的类别。它决定用哪一档大小上限，也是对象键里的一段。
type MediaKind string

// 四个类别。它们决定用哪一档上限，因此在策略里与上限绑在同一条允许项上。
const (
	// MediaKindImage 是图片。
	MediaKindImage MediaKind = "image"
	// MediaKindVideo 是视频。
	MediaKindVideo MediaKind = "video"
	// MediaKindAudio 是音频。
	MediaKindAudio MediaKind = "audio"
	// MediaKindFont 是字体。
	MediaKindFont MediaKind = "font"
)

// Asset 是工程资产库里的一个媒体文件。字节在对象存储，这里只有元数据。
type Asset struct {
	// ID 由 aladdin 分配。
	ID string
	// ProjectID 是所属工程：一个资产属于**唯一一个**工程。
	//
	// 不做跨工程复用：一个资产若被多个工程引用，"哪个工程有资格删它"就没有
	// 答案，而"被引用的资产不可删"这条规则会在跨工程时变成一个跨工程的锁。
	ProjectID string
	// Digest 是字节的密码学摘要，**是"同一份字节"的标识**，公开区按它寻址。
	Digest string
	// MediaKind 是摘要所属的类别，决定大小上限，也是私有区对象键里的一段。
	//
	// 它在**声明类型被校验的那一刻**就定下来，此后不随任何变化：键只按声明
	// 类型派生一次。
	MediaKind MediaKind
	// MediaType 是**上传方声明的**类型，服务端只校验它在白名单内。
	//
	// 它同时决定存进元数据的媒体类型与此后下发时回给浏览器的内容类型：两者
	// 只有一个来源，否则就会出现"库里写的是图片、下发时说的是别的"这类只能
	// 靠现象反推的问题。声明不等于验证——见 docs/design/objectstore/README.md。
	MediaType string
	SizeBytes int64
	// Filename 是原始文件名。**仅供展示与排障**：它不进对象键，不参与任何
	// 判断。它可能含路径分隔符、控制字符以及别人的名字，把它拼进对象键就是
	// 把一段不可信输入拼进存储路径。
	Filename   string
	UploadedAt time.Time
}

// AssetView 是一个资产加一条短时读取地址。
//
// 地址与元数据分开表达：地址每次读取都不同（它是一份会过期的凭证），而元数据
// 是资产自己的属性。
type AssetView struct {
	Asset Asset
	// URL 为空表示取不到地址（私有桶未配置或签发失败）。此时资产仍然列出。
	URL string
}

// assetAllowedTypes 是可接受的资产类型白名单，映射到它所属的类别。
//
// 键是**声明的**内容类型（归一化之后的形式）。它有两个用途，且必须是同一份：
// 服务端的检查，以及签发给存储的类型条件（见 AssetTypeRule）。
//
// 两处需要说明：
//
//   - **SVG 不在白名单里。** 它是唯一一种"看起来是图片、实际是带脚本能力的
//     XML"的格式，而资产会被发布到公开区。白名单决定了公开区对象**以待什么类型
//     下发**——白名单里没有可执行类型，浏览器就不会把字节当脚本执行。作为
//     **文件组里的文本条目**它已被服务：那里 `img` 不执行脚本，`object` 与
//     `frame` 被内容安全策略禁掉（见 csp.go）。
//   - **OGG 容器在标准库嗅探下是 application/ogg**，历史上无法区分音频与
//     视频。白名单按 spec 把 OGG 归在音频，因此这里映射为音频类别。
var assetAllowedTypes = map[string]MediaKind{
	"image/png":       MediaKindImage,
	"image/jpeg":      MediaKindImage,
	"image/gif":       MediaKindImage,
	"image/webp":      MediaKindImage,
	"video/mp4":       MediaKindVideo,
	"video/webm":      MediaKindVideo,
	"audio/mpeg":      MediaKindAudio,
	"audio/wave":      MediaKindAudio,
	"application/ogg": MediaKindAudio,
	"font/woff2":      MediaKindFont,
	"font/woff":       MediaKindFont,
	"font/ttf":        MediaKindFont,
	"font/otf":        MediaKindFont,
}

// AssetKindLimit 是一类资产的字节上限。
type AssetKindLimit struct {
	Kind     MediaKind
	MaxBytes int64
}

// AssetKindLimits 返回各类资产的字节上限，顺序固定（图片、视频、音频、字体）。
//
// 顺序固定是为了让"能力下发"这件事的取值可比较：同一份能力在两台部署上
// 应当逐字相同。
func AssetKindLimits() []AssetKindLimit {
	return []AssetKindLimit{
		{Kind: MediaKindImage, MaxBytes: ImageMaxBytes},
		{Kind: MediaKindVideo, MaxBytes: VideoMaxBytes},
		{Kind: MediaKindAudio, MaxBytes: AudioMaxBytes},
		{Kind: MediaKindFont, MaxBytes: FontMaxBytes},
	}
}

// MaxBytesFor 返回某个类别的大小上限。
//
// **上限按类别取**，而类别来自**声明的**类型：策略里每条允许项都把一种类型与
// 它那一档的上限绑在一起，因此"用视频的上限去卡图片"在存储侧就写不出来。
func MaxBytesFor(kind MediaKind) int64 {
	switch kind {
	case MediaKindImage:
		return ImageMaxBytes
	case MediaKindVideo:
		return VideoMaxBytes
	case MediaKindAudio:
		return AudioMaxBytes
	case MediaKindFont:
		return FontMaxBytes
	default:
		return 0
	}
}

// AssetObjectKey 返回一个资产在**私有区**的对象键（唯一入口）。
//
// 键由工程标识、**类别**与资产标识构成，与文件名无关：文件名是客户端可控的
// 输入，把它拼进存储路径等于把一段不可信输入拼进路径。
//
// 类别段换来的是：让**生命周期与配额策略能按类挂**（视频那一档的存储与清理
// 策略与图片不同），也让"这一份是媒体"在键上就看得出来。代价是同一事实的
// 第二种表达——声明类型已经是一条元数据；一致性由"键只按声明类型派生一次、
// 不随后续更改"这一条固定。
//
// 公开区不在这里：那一区按（内容摘要，类型）寻址（见 promote.go）。
func AssetObjectKey(projectID string, kind MediaKind, assetID string) string {
	return assetKeyPrefix + projectID + "/assets/" + string(kind) + "/" + assetID
}

// NormalizeAssetType 判定一个**声明的**类型能不能作为资产，并给出它的类别。
// （唯一入口）。
//
// 它接受的是声明值，不接受字节：直传之后服务端看不到字节（见
// docs/design/objectstore/README.md）。白名单因此从"字节确实是图片"降级成了
// "下发时的类型必属一个无害集合"——而白名单里没有任何可执行类型（没有 HTML、
// 没有 SVG），后者才是这套链路能成立的原因。
//
// 归一化（去空白、去掉 `;` 之后的参数部分、转小写）在判定之前完成：上传方写
// `IMAGE/PNG` 与 `image/png; charset=binary` 都是同一个类型。
func NormalizeAssetType(declared string) (string, MediaKind, error) {
	mediaType := objectstore.NormalizeContentType(declared)
	kind, ok := assetAllowedTypes[mediaType]
	if !ok {
		return "", "", ErrAssetTypeNotAllowed
	}
	return mediaType, kind, nil
}

// AssetTypeRule 返回签发直传凭证用的**那一条**类型规则（唯一入口）。
//
// 规则**由声明的类型与它那一档的上限派生**，不另写一份：两处各写一份的表现是
// "服务端接受了、存储侧拒绝"（或反过来），而用户看到的是一句无法归因的失败。
// 类型与上限在同一条里，因此"用视频的上限去卡图片"在策略层面写不出来。
//
// 一次上传只有一个声明类型，因此策略里**只放它这一条，不把整份白名单摊进去**。
// 摊进去有两个后果，第二个是实测踩过的：
//
//   - 签发的凭证允许"用一个类型声明、拿另一个类型的上限"写同一个键——比这次
//     上传需要的宽；
//   - 策略文档随白名单**线性变长**。STS 对 Policy 有长度上限，白名单从 9 类加到
//     13 类时就越过了它，换证失败，而那条路径只回一句"服务暂时不可用"。
func AssetTypeRule(mediaType string, kind MediaKind) objectstore.TypeRule {
	return objectstore.TypeRule{ContentType: mediaType, MaxBytes: MaxBytesFor(kind)}
}

// requireAssetStore 在私有桶缺席时给出统一结论。
//
// **资产功能整体缺席**：读取、上传、删除一律如此，而不是"读得到元数据、只是取不
// 到地址"。半开的能力会让界面渲染出一片半可用区域，而前端据 capabilities 本来
// 就整块不渲染它。
func (s *Service) requireAssetStore() error {
	if s.assets == nil {
		return ErrAssetUnavailable
	}
	return nil
}

// assetOfProject 判定一个资产标识是否属于某个工程（唯一入口）。
// （发布校验与删除拦阻共用）。
//
// 不属于时返回 ErrAssetNotFound——"不存在"与"属于别的工程"是同一个结论。
func (s *Service) assetOfProject(ctx context.Context, projectID, assetID string) (Asset, error) {
	asset, err := s.store.GetAsset(ctx, projectID, assetID)
	if err != nil {
		return Asset{}, err
	}
	return asset, nil
}

// BeginAssetUpload 开始一次资产上传：分配资产标识并签发一份直传凭证。
//
// **字节不经过服务端**（见 docs/design/objectstore/README.md）。这里做四件事：
// 校验工程归属、校验**声明的**类型在白名单内、按声明的大小早退，然后把"只许写
// 这一个键、且只许声明**这一个**类型、大小受它那一档约束"的策略交给对象存储执行。
//
// 一次上传分配一个**新键**：资产不可变，而"替换已有对象"在存储层就不成立。
func (s *Service) BeginAssetUpload(ctx context.Context, subjectID, projectID, declaredType string, declaredSize int64) (string, objectstore.Credential, error) {
	if err := s.requireAssetStore(); err != nil {
		return "", objectstore.Credential{}, err
	}
	if _, err := OwnedProject(ctx, s.store, projectID, subjectID); err != nil {
		return "", objectstore.Credential{}, err
	}
	mediaType, kind, err := NormalizeAssetType(declaredType)
	if err != nil {
		return "", objectstore.Credential{}, err
	}
	// 按**声明**的大小早退：这是给用户的省事（别把一个 500 MiB 的文件整个传
	// 上来只为了被拒），**不是安全边界**——真正的上限由存储侧按类型执行。
	if limit := MaxBytesFor(kind); declaredSize > limit {
		return "", objectstore.Credential{}, fmt.Errorf("%w: 声明 %d 字节，%s 类上限 %d 字节",
			ErrAssetTooLarge, declaredSize, kind, limit)
	}
	assetID, err := newAssetID()
	if err != nil {
		return "", objectstore.Credential{}, err
	}
	// 资产按标识寻址、一个标识一个对象，覆盖写不是它的语义——但仍然不允许：
	// 一次上传就是这个键的第一次也是唯一一次写入。
	credential, err := s.assets.IssueUpload(ctx, AssetObjectKey(projectID, kind, assetID),
		[]objectstore.TypeRule{AssetTypeRule(mediaType, kind)}, false)
	if err != nil {
		return "", objectstore.Credential{}, err
	}
	return assetID, credential, nil
}

// CommitAssetUpload 提交一次资产上传：核对对象确实到了，写入资产元数据。
//
// 签发之后客户端传了什么、传没传完，服务端都不知道，因此这里必须对那个键做一次
// 核对（存在性 + 真实字节数，见 objectstore.VerifyUploaded）。**没有提交的上传
// 不会进入任何清单**——签发那一步什么都没有写。
//
// 类型与摘要都是上传方**再次声明的**：两次调用之间服务端不保留任何状态，而
// "不保留状态"正是"未提交的上传不留痕迹"这条的实现方式。类型在这里被重新校验
// 并被用来取那一档的上限；摘要在这里**不核对**（见下）。
func (s *Service) CommitAssetUpload(ctx context.Context, subjectID, projectID, assetID, declaredType, declaredDigest, filename string) (Asset, error) {
	if err := s.requireAssetStore(); err != nil {
		return Asset{}, err
	}
	if _, err := OwnedProject(ctx, s.store, projectID, subjectID); err != nil {
		return Asset{}, err
	}
	mediaType, kind, err := NormalizeAssetType(declaredType)
	if err != nil {
		return Asset{}, err
	}
	// 摘要会成为**公开区的对象键**，因此先校验它的形状：一个含分隔符或
	// 控制字符的取值会把"按内容寻址"变成"按调用方给的路径写"。
	if !IsContentDigest(declaredDigest) {
		return Asset{}, ErrDigestInvalid
	}
	key := AssetObjectKey(projectID, kind, assetID)
	stat, err := objectstore.VerifyUploaded(ctx, s.assets, key, MaxBytesFor(kind))
	if err != nil {
		switch {
		case errors.Is(err, objectstore.ErrObjectNotFound):
			return Asset{}, ErrAssetObjectMissing
		case errors.Is(err, objectstore.ErrUploadTooLarge):
			return Asset{}, fmt.Errorf("%w: %s 类上限 %d 字节", ErrAssetTooLarge, kind, MaxBytesFor(kind))
		default:
			return Asset{}, err
		}
	}
	// **摘要在这里不核对。** 核对它需要把字节读回来算一遍，而这件事在上架到
	// 公开区时必然要做（它本来就要把字节搬过去，见 promote.go）。提前再读一次
	// 只为了核对，等于把直传省下的带宽又花回去。一条假的摘要不会把公开区的
	// 地址指向别人的内容——它在被使用之前就会被拒绝。
	asset := Asset{
		ID:         assetID,
		ProjectID:  projectID,
		Digest:     declaredDigest,
		MediaKind:  kind,
		MediaType:  mediaType,
		SizeBytes:  stat.SizeBytes,
		Filename:   truncateRunes(filename, filenameMaxRunes),
		UploadedAt: s.now(),
	}
	if err := s.store.CreateAsset(ctx, asset); err != nil {
		// 元数据写不进去时把对象删掉：留着它就留下一个**无从被引用**的对象，
		// 而"存在哪些资产"的权威是库内的行。
		if deleteErr := s.assets.Delete(ctx, key); deleteErr != nil && s.logger != nil {
			s.logger.Warn("回滚资产对象失败，桶上可能出现孤儿对象",
				zap.String("project_id", projectID),
				zap.String("asset_id", assetID),
				zap.Error(deleteErr))
		}
		return Asset{}, err
	}
	if s.logger != nil {
		s.logger.Info("已提交资产上传",
			zap.String("project_id", projectID),
			zap.String("asset_id", asset.ID),
			zap.String("subject_id", subjectID),
			zap.String("media_type", asset.MediaType),
			zap.Int64("bytes", asset.SizeBytes))
	}
	// 事件在留痕之后发：资产库多了一份，订阅者的资产面板该跟上。
	s.publish(projectID)
	return asset, nil
}

// ListAssets 列出工程的资产，并逐个签发短时读取地址。
func (s *Service) ListAssets(ctx context.Context, subjectID, projectID string) ([]AssetView, error) {
	if err := s.requireAssetStore(); err != nil {
		return nil, err
	}
	if _, err := OwnedProject(ctx, s.store, projectID, subjectID); err != nil {
		return nil, err
	}
	assets, err := s.store.ListAssets(ctx, projectID)
	if err != nil {
		return nil, err
	}
	views := make([]AssetView, 0, len(assets))
	for _, asset := range assets {
		views = append(views, AssetView{Asset: asset, URL: s.listAssetURL(ctx, asset)})
	}
	return views, nil
}

// DeleteAsset 删除一个资产。
//
// **被任一版本引用时拒绝**，错误信息指出被哪些版本引用（见 version.go 的
// referencingVersions）。未被引用时删除元数据行与私有区对象。
//
// **公开区的副本不因删除私有资产而消失**：它是一份独立的对象，由发布形成，
// 为已发布的页面服务。
func (s *Service) DeleteAsset(ctx context.Context, subjectID, projectID, assetID string) error {
	if err := s.requireAssetStore(); err != nil {
		return err
	}
	if _, err := OwnedProject(ctx, s.store, projectID, subjectID); err != nil {
		return err
	}
	asset, err := s.assetOfProject(ctx, projectID, assetID)
	if err != nil {
		return err
	}
	referencing, err := s.referencingVersions(ctx, projectID, assetID)
	if err != nil {
		return err
	}
	if len(referencing) > 0 {
		return fmt.Errorf("%w: %s", ErrAssetReferenced, describeVersions(referencing))
	}
	if err := s.store.DeleteAsset(ctx, projectID, assetID); err != nil {
		return err
	}
	// 对象删除失败不影响"资产已删除"这一结论（库内是权威），与头像同源。
	s.deleteAssetObjects(ctx, projectID, []Asset{asset}, "删除资产")
	s.publish(projectID)
	if s.logger != nil {
		s.logger.Info("已删除资产",
			zap.String("project_id", projectID),
			zap.String("asset_id", assetID),
			zap.String("subject_id", subjectID))
	}
	return nil
}

// AssetURL 签发一个资产的编辑态读取地址（唯一入口）。
//
// 它要求调用者是该工程的拥有者：编辑态的服务端**要参与判权**（发布态不参与，
// 见 publish.go 的 CurrentArtifact，两者不是同一件事的两种实现）。
func (s *Service) AssetURL(ctx context.Context, subjectID, projectID, assetID string) (string, error) {
	if _, err := OwnedProject(ctx, s.store, projectID, subjectID); err != nil {
		return "", err
	}
	asset, err := s.assetOfProject(ctx, projectID, assetID)
	if err != nil {
		return "", err
	}
	return s.presignAsset(ctx, asset)
}

// presignAsset 签发一个资产的读取地址。
func (s *Service) presignAsset(ctx context.Context, asset Asset) (string, error) {
	if s.assets == nil {
		return "", ErrAssetUnavailable
	}
	return s.assets.PresignGet(ctx, AssetObjectKey(asset.ProjectID, asset.MediaKind, asset.ID), AssetURLTTL)
}

// listAssetURL 为清单签发读取地址。
//
// 签发失败只留痕、不返错：资产清单本身仍然有意义，而一个取不到地址的条目的
// 表现形式是"这张图暂时显示不出来"，与头像过期后的表现同源。
func (s *Service) listAssetURL(ctx context.Context, asset Asset) string {
	url, err := s.presignAsset(ctx, asset)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("签发资产读取地址失败",
				zap.String("project_id", asset.ProjectID),
				zap.String("asset_id", asset.ID),
				zap.Error(err))
		}
		return ""
	}
	return url
}

// truncateRunes 按字符数截断一段展示用文本。
func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}
