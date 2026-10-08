package skill

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/idgen"
	"github.com/poetlife/aladdin/internal/imagetype"
	"github.com/poetlife/aladdin/internal/objectstore"
	"github.com/poetlife/aladdin/internal/relpath"
)

// 本文件是**展示图集**：详情页画廊上的那组图，其中第一张是卡片上的封面。
//
// 它是**说明层的一项**（与标题、简介、标签同级），不是包的内容：
//
//   - 不进文件清单、不进版本、不参与取用，`skipped_files` 也不因它而变；
//   - 可改：加图、删图、换图、重排都不产生版本、不动指针（见
//     docs/design/skill/catalog.md 的"展示图集"）。
//
// 由此得到本文件的两条性质，它们与内容对象正好相反：
//
//   - **一张一个键，键按图标识走**——顺序不是键的一部分，因此重排与删中间一张
//     都不改任何一张的地址；换图是就地覆盖。
//   - **删技能时图一起删**——每个键都由这一个技能独占，不像内容对象那样可能被
//     别处引用。

// 图集的规模上限。
//
// **它们是常量，不是配置项**：做成配置项只会让"这个部署收得下几张图"变成一个
// 要在别处解释的事实，而它与任何部署形态无关。单张的类型与大小不在这里——那一份
// 与头像共用（见 imagetype）。
const (
	// MaxImages 是一个技能展示图集的张数上限。
	MaxImages = 12
	// MaxImageTotalBytes 是图集所有图的字节总数上限。
	MaxImageTotalBytes = 12 << 20
)

// imageKeyPrefix 是展示图对象的键前缀。
//
// 它是**常量，不是配置项**，理由与头像、galaxy 那些前缀相同：做成配置项只会多出
// 一种失败方式——改了前缀，存量图在一瞬间全部变成孤儿，而技能本身看起来毫无变化。
const imageKeyPrefix = "skills/image/"

// imageIDPrefix 是展示图标识的前缀。
//
// 分配即冻结、永不复用：**键由它派生**，复用一个标识就等于让新图去覆盖旧图的对象。
const imageIDPrefix = "ski_"

// ImageURLTTL 是展示图下发地址的有效期。
//
// 与头像同值、同一条理由：地址在有效期内是一份**谁拿到都能用的凭证**，取分钟量级，
// 过期后重新读一次技能就有新的。换图时，旧地址在过期前指向新那一张——与头像同一个
// 代价，也同一个理由（换的是"当前这一张"）。
const ImageURLTTL = 10 * time.Minute

var (
	// ErrImageNotAllowed 表示指定的图路径不在包里、看着不是图、或字节与类型不符。
	ErrImageNotAllowed = errors.New("展示图不合法")

	// ErrImageTooLarge 表示单张图超过大小上限。
	ErrImageTooLarge = errors.New("展示图超过单张大小上限")

	// ErrTooManyImages 表示图集的张数超过上限。
	ErrTooManyImages = errors.New("展示图张数超过上限")

	// ErrImagesTooLarge 表示图集的字节总数超过上限。
	ErrImagesTooLarge = errors.New("展示图合计超过大小上限")

	// ErrImageNotFound 表示这个技能的图集里没有这一张。
	//
	// 它与"技能不存在"必须分开：技能在，是那一张不在。混为一谈会让调用方在用过
	// 一次的标识上以为整个技能没了。
	ErrImageNotFound = errors.New("技能里没有这张展示图")

	// ErrImageUploadMissing 表示提交时那个键上还没有对象：直传没完成（或没开始）。
	//
	// 它与"存储故障"必须分开：这是一次可以重来的上传，不是一个故障，也不是内容
	// 问题（与头像的 ErrAvatarObjectMissing 同一条）。
	ErrImageUploadMissing = errors.New("展示图对象不存在，上传可能没有完成")

	// ErrImageOrderMismatch 表示重排给出的标识不是当前图集的一个排列。
	//
	// 它是**用法错误**：重排表达的是期望的完整状态，因此"少了一张"不能理解成
	// "把那些没提到的删掉"，也不能理解成"只排提到的那些"。
	ErrImageOrderMismatch = errors.New("展示图顺序与当前图集不符")
)

// ImageKey 返回一个技能的一张展示图在对象存储里的对象键（唯一入口）。
//
// 它按**图标识**而不是位置拼：位置会随重排而变，键不能跟着变——跟着变就意味着
// 一次重排要把每一张都搬家，而"删中间一张"会让后面每一张的地址都作废。
func ImageKey(skillID, imageID string) string {
	return imageKeyPrefix + skillID + "/" + imageID
}

// NewImageID 分配一个展示图标识（唯一入口）。
func NewImageID() (string, error) { return idgen.New(imageIDPrefix) }

// Image 是一张已就位的展示图。
//
// **它不带地址**：地址是读的时候才签发的（见 View），把一份短时凭证存进领域对象
// 只会让人以为它是一个可以存下来的取值。
type Image struct {
	// ID 是图标识。由服务端分配、不可猜、不可改、不复用。
	//
	// 它是这一张在重排、设为首图与删除时的指认方式——**不是它的位置**。
	ID string
	// ObjectKey 是这一张在对象存储里的键。
	//
	// 它由 ImageKey 派生，**存下来只是为了那些不按当前规则派生的存量行**（早期的
	// 单张封面用的是另一个前缀）。业务代码一律按 ID 指认这一张，不解析键。
	ObjectKey string
	// SizeBytes 是字节数。图集的合计上限按它算，因此它必须与桶上的对象一致。
	SizeBytes int64
}

// PickedImage 是一张**已经从包里取回来、并通过全部校验**的图。
//
// 它不带路径：取回那一步的错误已经点名了那一条，而落盘用的是按图标识派生的键——
// 包内路径此后不再参与任何判定（见 writeImages）。
type PickedImage struct {
	// Data 是字节。
	Data []byte
	// ContentType 是**由字节本身推出来**的类型（不是由扩展名）：它是写进对象、
	// 此后决定浏览器怎么渲染它的那个取值。
	ContentType string
}

// pickImage 从**已经取回的那棵树**里挑出一条路径，变成一张图（唯一入口）。
//
// imagePath 是**包内路径**（与文件清单同一个坐标系）。它对应的仓库路径由来源的
// 子路径拼出来——因此它逃不出这次纳管的范围，与文件清单受同一个约束。
//
// 四道校验，任何一道不过即拒，且错误点名那一条路径：
//
//  1. 它在这一次的目录树里（**不能凭路径凭空去取一份远端字节**）；
//  2. 扩展名看着是图（PNG / JPEG / GIF，见 imagetype）；
//  3. 它在上限内；
//  4. **文件头与它说的类型相符**。
//
// 第 4 条只有这条路做得到：字节是服务端自己取回来的，所以看得见。界面上传那条路
// 字节直传、服务端看不到，只能信声明——这条对照是直传的已知代价，不是疏漏
// （见 docs/design/skill/catalog.md 的"展示图集"）。
func (s *Service) pickImage(ctx context.Context, repo Repository, tree Tree, imagePath string) (PickedImage, error) {
	if !relpath.Valid(imagePath) {
		return PickedImage{}, fmt.Errorf("%w: 路径 %q 形状不合法", ErrImageNotAllowed, imagePath)
	}
	blob, ok := tree.Blob(imagePath)
	if !ok {
		return PickedImage{}, fmt.Errorf("%w: 包里没有 %q", ErrImageNotAllowed, imagePath)
	}
	if blob.Size > imagetype.MaxBytes {
		return PickedImage{}, fmt.Errorf("%w: %q 有 %d 字节，超过单张上限 %d",
			ErrImageNotAllowed, imagePath, blob.Size, imagetype.MaxBytes)
	}
	if _, ok := imagetype.FromExtension(imagePath); !ok {
		return PickedImage{}, fmt.Errorf("%w: %q 看着不是一张图（收 PNG / JPEG / GIF）",
			ErrImageNotAllowed, imagePath)
	}
	data, err := s.remote.FetchBlob(ctx, repo, blob.SHA, imagetype.MaxBytes)
	if err != nil {
		return PickedImage{}, err
	}
	contentType, ok := imagetype.MatchMagic(data)
	if !ok {
		return PickedImage{}, fmt.Errorf("%w: %q 的字节不是 PNG / JPEG / GIF", ErrImageNotAllowed, imagePath)
	}
	return PickedImage{Data: data, ContentType: contentType}, nil
}

// pickImages 逐张取回并校验一次纳管给的整组图（唯一入口）。
//
// 它在**写任何东西之前**跑完：张数、重复路径、单张与合计的上限都在这里判，因此
// 超限的纳管不留痕迹（与别的校验同一条，见 onboarding.md）。
//
// **顺序即给定顺序**：第一张就是首图，中间不做任何排序。
func (s *Service) pickImages(ctx context.Context, repo Repository, tree Tree, imagePaths []string) ([]PickedImage, error) {
	if len(imagePaths) > MaxImages {
		return nil, fmt.Errorf("%w: 给了 %d 张，上限 %d 张", ErrTooManyImages, len(imagePaths), MaxImages)
	}
	seen := make(map[string]bool, len(imagePaths))
	picked := make([]PickedImage, 0, len(imagePaths))
	var total int64
	for _, imagePath := range imagePaths {
		// 同一条路径给两次是一次"我以为给了两张"的用法错误：图标识是每张一个的，
		// 放过去就会得到两张字节完全相同、地址不同的图。
		if seen[imagePath] {
			return nil, fmt.Errorf("%w: 路径 %q 给了两次", ErrImageNotAllowed, imagePath)
		}
		seen[imagePath] = true
		image, err := s.pickImage(ctx, repo, tree, imagePath)
		if err != nil {
			return nil, err
		}
		total += int64(len(image.Data))
		if total > MaxImageTotalBytes {
			return nil, fmt.Errorf("%w: 前 %d 张合计 %d 字节，上限 %d",
				ErrImagesTooLarge, len(picked)+1, total, MaxImageTotalBytes)
		}
		picked = append(picked, image)
	}
	return picked, nil
}

// writeImage 把一张图写进对象存储（覆盖写）。
//
// **覆盖是刻意的**：换图就是换掉这一张的字节，键不变。它因此不走
// `PutContentObject` 那条"仅当不存在时写入"的路——那条路服务的是按摘要寻址的
// 内容对象。
func (s *Service) writeImage(ctx context.Context, key string, image PickedImage) error {
	if err := s.objects.Put(ctx, key, image.ContentType, image.Data); err != nil {
		s.logger.Warn("写入展示图对象失败", zap.String("key", key), zap.Error(err))
		return fmt.Errorf("%w: 写入展示图对象失败: %w", objectstore.ErrStoreUnavailable, err)
	}
	return nil
}

// writeImages 给一组已校验的图分配标识、写进对象存储，并返回它们的图集（唯一入口）。
//
// **分配标识与写对象在同一处**：键由标识派生，分两处拼的表现是一个技能指向别人
// 的图。它同上一步（写内容对象）一样，是"字节先于行落地"——行写失败会留下无引用
// 的对象，与删除技能不删内容对象是同一种残留。
func (s *Service) writeImages(ctx context.Context, skillID string, picked []PickedImage) ([]Image, error) {
	images := make([]Image, 0, len(picked))
	for _, image := range picked {
		imageID, err := NewImageID()
		if err != nil {
			return nil, err
		}
		key := ImageKey(skillID, imageID)
		if err := s.writeImage(ctx, key, image); err != nil {
			return nil, err
		}
		images = append(images, Image{ID: imageID, ObjectKey: key, SizeBytes: int64(len(image.Data))})
	}
	return images, nil
}

// BeginImageUpload 签发一份展示图上传的直传凭证（唯一入口）。
//
// 返回这次上传对应的**图标识**：不给表示新增一张（这里分配、排到末尾），给了已有的
// 一张表示换掉它的字节（标识、键与位置都不变）。
//
// **字节不经过服务端**：这里只校验**声明的**类型与大小，真正的边界由存储侧按签发的
// 策略执行。与头像完全一致。张数与合计的上限在这里按**声明**早退一次，提交时再按
// 真实字节数复核一次——声明可以撒谎，而提交那一步做了 Head。
func (s *Service) BeginImageUpload(ctx context.Context, skillID, imageID, declaredType string, declaredSize int64) (string, objectstore.Credential, error) {
	if s.objects == nil {
		return "", objectstore.Credential{}, ErrObjectStoreUnavailable
	}
	item, err := s.store.GetSkill(ctx, skillID)
	if err != nil {
		return "", objectstore.Credential{}, err
	}
	if _, err := imagetype.Normalize(declaredType); err != nil {
		return "", objectstore.Credential{}, err
	}
	if declaredSize > imagetype.MaxBytes {
		return "", objectstore.Credential{}, fmt.Errorf("%w: 声明 %d 字节，单张上限 %d",
			ErrImageTooLarge, declaredSize, imagetype.MaxBytes)
	}

	key := ""
	if imageID == "" {
		if len(item.Images) >= MaxImages {
			return "", objectstore.Credential{}, fmt.Errorf("%w: 已有 %d 张，上限 %d 张",
				ErrTooManyImages, len(item.Images), MaxImages)
		}
		if totalImageBytes(item.Images)+declaredSize > MaxImageTotalBytes {
			return "", objectstore.Credential{}, tooManyBytes(item.Images, declaredSize)
		}
		imageID, err = NewImageID()
		if err != nil {
			return "", objectstore.Credential{}, err
		}
		key = ImageKey(skillID, imageID)
	} else {
		current, found := findImage(item.Images, imageID)
		if !found {
			return "", objectstore.Credential{}, fmt.Errorf("%w: %s 里没有 %s", ErrImageNotFound, skillID, imageID)
		}
		// 换图：这一张原来占的字节让出来，再加上这次声明的。
		if totalImageBytes(item.Images)-current.SizeBytes+declaredSize > MaxImageTotalBytes {
			return "", objectstore.Credential{}, tooManyBytes(withoutImage(item.Images, imageID), declaredSize)
		}
		key = current.ObjectKey
	}

	credential, err := s.objects.IssueUpload(ctx, key, imagetype.Rules(), false)
	if err != nil {
		// **留痕**：换一次凭证是一次外部调用，失败的原因（凭证服务限频、网络、
		// 密钥不对）只有这里看得见——交给客户端的那句话是给人看的，不是给排障用的。
		s.logger.Warn("签发展示图直传凭证失败",
			zap.String("skill_id", skillID), zap.String("key", key), zap.Error(err))
		return "", objectstore.Credential{}, fmt.Errorf("%w: 签发直传凭证失败: %w",
			objectstore.ErrStoreUnavailable, err)
	}
	return imageID, credential, nil
}

// CommitImageUpload 核对字节确实到了，把这一张记进图集（唯一入口）。
//
// 签发之后客户端传了什么、传没传完，服务端都不知道，因此这里对那个键做一次 Head：
// 不存在即失败，字节数超过单张上限即失败并删除对象（那是这次失败唯一的残留物）。
//
// **按真实字节数复核张数与合计**：签发时按的是声明，而这一步看得见真实字节数。
// 复核不过时把刚传上去的对象删掉——它没有被任何一行引用。
//
// 标识已经在图集里时是**换图**：只更新它的大小，顺序、标识与键都不动。
func (s *Service) CommitImageUpload(ctx context.Context, skillID, imageID string) (Skill, error) {
	if s.objects == nil {
		return Skill{}, ErrObjectStoreUnavailable
	}
	item, err := s.store.GetSkill(ctx, skillID)
	if err != nil {
		return Skill{}, err
	}
	current, replacing := findImage(item.Images, imageID)
	// 新增的键按标识现拼；换图用的是这一张已有的键（存量行可能有别的派生规则）。
	key := ImageKey(skillID, imageID)
	if replacing {
		key = current.ObjectKey
	}

	stat, err := objectstore.VerifyUploaded(ctx, s.objects, key, imagetype.MaxBytes)
	if err != nil {
		switch {
		case errors.Is(err, objectstore.ErrUploadTooLarge):
			return Skill{}, fmt.Errorf("%w: %w", ErrImageTooLarge, err)
		case errors.Is(err, objectstore.ErrObjectNotFound):
			return Skill{}, fmt.Errorf("%w: %s", ErrImageUploadMissing, imageID)
		default:
			// **留痕**：核对失败的原因（网络、权限、存储侧故障）只有这里看得见。
			// 交给客户端的那句话是给人看的，不是给排障用的。
			s.logger.Warn("核对展示图上传失败",
				zap.String("skill_id", skillID), zap.String("image_id", imageID), zap.Error(err))
			return Skill{}, fmt.Errorf("%w: 核对上传失败: %w", objectstore.ErrStoreUnavailable, err)
		}
	}

	if !replacing {
		if len(item.Images) >= MaxImages {
			s.discardUpload(ctx, key, imageID)
			return Skill{}, fmt.Errorf("%w: 已有 %d 张，上限 %d 张", ErrTooManyImages, len(item.Images), MaxImages)
		}
		if totalImageBytes(item.Images)+stat.SizeBytes > MaxImageTotalBytes {
			s.discardUpload(ctx, key, imageID)
			return Skill{}, tooManyBytes(item.Images, stat.SizeBytes)
		}
		if err := s.store.PutImage(ctx, skillID, Image{ID: imageID, ObjectKey: key, SizeBytes: stat.SizeBytes}); err != nil {
			return Skill{}, err
		}
	} else {
		// **换图是就地覆盖：字节已经换上了，回不去。** 因此先把真实字节数记进库
		// （那一行必须与桶上一致），再看合计有没有被顶出去——顶出去了照样报错，
		// 但报的是一句管理员能照着做的话：这一张已经生效，去删掉别的一张。
		if err := s.store.PutImage(ctx, skillID, Image{ID: imageID, ObjectKey: key, SizeBytes: stat.SizeBytes}); err != nil {
			return Skill{}, err
		}
		if total := totalImageBytes(item.Images) - current.SizeBytes + stat.SizeBytes; total > MaxImageTotalBytes {
			return Skill{}, fmt.Errorf(
				"%w: 换上的这一张已经生效，合计 %d 字节，超出上限 %d 字节——请先删掉别的一张",
				ErrImagesTooLarge, total, total-MaxImageTotalBytes)
		}
	}
	s.logger.Info("技能展示图已更新",
		zap.String("skill_id", skillID),
		zap.String("image_id", imageID),
		zap.Bool("replaced", replacing))
	return s.store.GetSkill(ctx, skillID)
}

// discardUpload 删掉一张**已经被判超限、因而没有进图集**的对象。
//
// 尽力而为：删不掉只留下一个无从被引用的孤儿，与别处的残留同一条结论。
func (s *Service) discardUpload(ctx context.Context, key, imageID string) {
	if err := s.objects.Delete(ctx, key); err != nil {
		s.logger.Warn("删除超限的展示图对象失败，它没有被任何技能引用",
			zap.String("image_id", imageID), zap.Error(err))
	}
}

// DeleteImage 从图集里移除一张（唯一入口）。
//
// 标识不在这个技能的图集里时返回 ErrImageNotFound：删一张已经不在的图是一次拼错
// 了标识，不是一个状态，因此**这里不是幂等的**（与"删技能"那类幂等动作不同）。
func (s *Service) DeleteImage(ctx context.Context, skillID, imageID string) (Skill, error) {
	if s.objects == nil {
		return Skill{}, ErrObjectStoreUnavailable
	}
	item, err := s.store.GetSkill(ctx, skillID)
	if err != nil {
		return Skill{}, err
	}
	image, found := findImage(item.Images, imageID)
	if !found {
		return Skill{}, fmt.Errorf("%w: %s 里没有 %s", ErrImageNotFound, skillID, imageID)
	}
	// 先清库再删对象：反过来的话，删对象成功而清库失败会留下一个指向空键的图，
	// 而那个状态的表现在界面上是"图裂了"。留着孤儿对象的代价只是几个字节。
	if err := s.store.DeleteImage(ctx, skillID, imageID); err != nil {
		return Skill{}, err
	}
	if err := s.objects.Delete(ctx, image.ObjectKey); err != nil {
		// 尽力而为：库里的行才是权威，桶上的孤儿对象没有功能影响（与 galaxy 的
		// 同一条结论）。
		s.logger.Warn("删除展示图对象失败，技能已不再引用它",
			zap.String("skill_id", skillID), zap.String("image_id", imageID))
	}
	return s.store.GetSkill(ctx, skillID)
}

// ReorderImages 把图集顺序整体替换为给定顺序（唯一入口）。
//
// 请求给出的是**期望的完整状态**（与改说明层同取向）：第一项即首图，"设为首图"
// 就是把它排到第一位，没有第二个开关。给出的标识不是当前图集的一个排列时整个拒绝
// ——在存储里判，因为那是一次原子比较与写入，在服务层先查再写会多一个"查完之后它
// 被删了"的窗口。
func (s *Service) ReorderImages(ctx context.Context, skillID string, imageIDs []string) (Skill, error) {
	if err := s.store.ReorderImages(ctx, skillID, imageIDs); err != nil {
		return Skill{}, err
	}
	return s.store.GetSkill(ctx, skillID)
}

// ShownImage 是一张对调用方呈现的展示图：领域对象加上它的短时地址。
type ShownImage struct {
	Image
	// URL 是这一张的短时读取地址。空串表示这个部署没有对象存储、或签名失败
	// ——界面据此渲染占位，而不是去取一张取不到的图。
	URL string
}

// ViewOf 把一个领域对象包成对调用方呈现的样子（唯一入口）。
//
// detail 为真时签出**整个图集**的地址（详情，以及各处"刚写下去的那一份"返回），
// 为假时只签**首图**（列表）：列表会读很多行，而卡片只用第一张。
//
// **写路径必须走它。** 纳管、同步、回滚、改说明层、加图各自返回"刚写下去的那一份"，
// 而图地址是**读的时候才签发的**——不在这一处补上，就会出现"刚纳管回来的那条没有
// 图、重新读一次才有"这种差异，而它在界面上表现成"卡片先是空的，刷新一下图才出来"。
func (s *Service) ViewOf(ctx context.Context, item Skill, detail bool) View {
	images := item.Images
	if !detail && len(images) > 1 {
		images = images[:1]
	}
	shown := make([]ShownImage, 0, len(images))
	for _, image := range images {
		shown = append(shown, ShownImage{Image: image, URL: s.imageURL(ctx, item.ID, image)})
	}
	return View{Skill: item, Shown: shown}
}

// imageURL 生成一张图的短时读取地址（唯一入口）。返回空串表示"这张图取不到地址"。
//
// 失败也返回空串：一张展示图取不到地址，不该让整个目录读不出来——界面会据此渲染
// 占位，而"图没出来"与"这一页打不开"是两件严重程度差很远的事。
func (s *Service) imageURL(ctx context.Context, skillID string, image Image) string {
	if image.ObjectKey == "" || s.objects == nil {
		return ""
	}
	address, err := s.objects.PresignGet(ctx, image.ObjectKey, ImageURLTTL)
	if err != nil {
		s.logger.Warn("签发展示图地址失败，按没有这一张处理",
			zap.String("skill_id", skillID), zap.String("image_id", image.ID))
		return ""
	}
	return address
}

// IsImagePermutation 判断 imageIDs 是不是 images 的一个排列（同集合、不重、不多不少）。
//
// 它是**重排这一次写入的判据**，两个存储实现共用（因此导出）：各写一遍的表现是
// "内存实现通过、SQL 实现拒绝"这类只在一边出现的偏差。
func IsImagePermutation(images []Image, imageIDs []string) bool {
	if len(images) != len(imageIDs) {
		return false
	}
	wanted := make(map[string]bool, len(images))
	for _, image := range images {
		wanted[image.ID] = true
	}
	seen := make(map[string]bool, len(imageIDs))
	for _, imageID := range imageIDs {
		if !wanted[imageID] || seen[imageID] {
			return false
		}
		seen[imageID] = true
	}
	return true
}

// findImage 在有序图集里按标识找一张。
func findImage(images []Image, imageID string) (Image, bool) {
	for _, image := range images {
		if image.ID == imageID {
			return image, true
		}
	}
	return Image{}, false
}

// totalImageBytes 返回图集的字节总数。
func totalImageBytes(images []Image) int64 {
	var total int64
	for _, image := range images {
		total += image.SizeBytes
	}
	return total
}

// withoutImage 返回去掉某一张之后的图集（保持顺序）。
func withoutImage(images []Image, imageID string) []Image {
	kept := make([]Image, 0, len(images))
	for _, image := range images {
		if image.ID != imageID {
			kept = append(kept, image)
		}
	}
	return kept
}

// tooManyBytes 造一条"这次写入会超过合计上限"的错误，并点名现有的张数与合计。
func tooManyBytes(images []Image, incoming int64) error {
	return fmt.Errorf("%w: 现有 %d 张共 %d 字节，再加 %d 字节，上限 %d",
		ErrImagesTooLarge, len(images), totalImageBytes(images), incoming, MaxImageTotalBytes)
}
