package skill

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/imagetype"
	"github.com/poetlife/aladdin/internal/objectstore"
	"github.com/poetlife/aladdin/internal/relpath"
)

// 本文件是**封面**：技能卡片上那张展示图。
//
// 它是**说明层的一项**（与标题、简介、标签同级），不是包的内容：
//
//   - 不进文件清单、不进版本、不参与取用，`skipped_files` 也不因它而变；
//   - 可改：换封面不产生版本、不动指针（见 docs/design/skill/catalog.md）。
//
// 由此得到本文件的两条性质，它们与内容对象正好相反：
//
//   - **键按技能标识走，允许覆盖**——一个技能一张，换就是原地覆盖。内容对象按摘要
//     寻址、跨技能共享、写入时禁止覆盖（见 package.go）。
//   - **删技能时一起删**——这个键由这一个技能独占，不像内容对象那样可能被别处引用。

// coverKeyPrefix 是封面对象的键前缀。
//
// 它是**常量，不是配置项**，理由与头像、galaxy 那些前缀相同：做成配置项只会多出
// 一种失败方式——改了前缀，存量封面在一瞬间全部变成孤儿，而技能本身看起来毫无变化。
const coverKeyPrefix = "skills/cover/"

// CoverURLTTL 是封面下发地址的有效期。
//
// 与头像同值、同一条理由：地址在有效期内是一份**谁拿到都能用的凭证**，取分钟量级，
// 过期后重新读一次技能就有新的。换封面时，旧地址在过期前指向新那一张——与头像
// 同一个代价，也同一个理由（换的是"当前这一张"）。
const CoverURLTTL = 10 * time.Minute

var (
	// ErrCoverNotAllowed 表示指定的封面路径不在包里、看着不是图、或字节与类型不符。
	ErrCoverNotAllowed = errors.New("封面不合法")

	// ErrCoverTooLarge 表示封面超过大小上限。
	ErrCoverTooLarge = errors.New("封面超过大小上限")
)

// CoverKey 返回一个技能的封面在对象存储里的对象键（唯一入口）。
func CoverKey(skillID string) string { return coverKeyPrefix + skillID }

// Cover 是一张已就绪的封面：字节与它的类型。
//
// 类型**由字节本身推出来**（不是由扩展名），因为它是写进对象、此后决定浏览器怎么
// 渲染它的那个取值。
type Cover struct {
	Data        []byte
	ContentType string
}

// PickCover 从**已经取回的那棵树**里挑出 coverPath 那一条，变成一张封面（唯一入口）。
//
// coverPath 是**包内路径**（与文件清单同一个坐标系）。它对应的仓库路径由来源的
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
// （见 docs/design/skill/catalog.md 的"封面"）。
func (s *Service) pickCover(ctx context.Context, repo Repository, tree Tree, coverPath string) (Cover, error) {
	if !relpath.Valid(coverPath) {
		return Cover{}, fmt.Errorf("%w: 路径 %q 形状不合法", ErrCoverNotAllowed, coverPath)
	}
	blob, ok := tree.Blob(coverPath)
	if !ok {
		return Cover{}, fmt.Errorf("%w: 包里没有 %q", ErrCoverNotAllowed, coverPath)
	}
	if blob.Size > imagetype.MaxBytes {
		return Cover{}, fmt.Errorf("%w: %q 有 %d 字节，超过上限 %d",
			ErrCoverNotAllowed, coverPath, blob.Size, imagetype.MaxBytes)
	}
	if _, ok := imagetype.FromExtension(coverPath); !ok {
		return Cover{}, fmt.Errorf("%w: %q 看着不是一张图（收 PNG / JPEG / GIF）",
			ErrCoverNotAllowed, coverPath)
	}
	data, err := s.remote.FetchBlob(ctx, repo, blob.SHA, imagetype.MaxBytes)
	if err != nil {
		return Cover{}, err
	}
	contentType, ok := imagetype.MatchMagic(data)
	if !ok {
		return Cover{}, fmt.Errorf("%w: %q 的字节不是 PNG / JPEG / GIF", ErrCoverNotAllowed, coverPath)
	}
	return Cover{Data: data, ContentType: contentType}, nil
}

// writeCover 把一张封面写进对象存储（覆盖写）。
//
// **覆盖是刻意的**：封面是"当前这一张"，不是一个要被版本钉住的事实。它因此不走
// `PutContentObject` 那条"仅当不存在时写入"的路——那条路服务的是按摘要寻址的内容
// 对象。
func (s *Service) writeCover(ctx context.Context, skillID string, cover Cover) error {
	if err := s.objects.Put(ctx, CoverKey(skillID), cover.ContentType, cover.Data); err != nil {
		return mapObjectError(err)
	}
	return nil
}

// BeginCoverUpload 签发一份封面上传的直传凭证（唯一入口）。
//
// **字节不经过服务端**：这里只校验**声明的**类型与大小，真正的边界由存储侧按签发的
// 策略执行。与头像完全一致。
func (s *Service) BeginCoverUpload(ctx context.Context, skillID, declaredType string, declaredSize int64) (objectstore.Credential, error) {
	if s.objects == nil {
		return objectstore.Credential{}, ErrObjectStoreUnavailable
	}
	if _, err := s.store.GetSkill(ctx, skillID); err != nil {
		return objectstore.Credential{}, err
	}
	if _, err := imagetype.Normalize(declaredType); err != nil {
		return objectstore.Credential{}, err
	}
	if declaredSize > imagetype.MaxBytes {
		return objectstore.Credential{}, fmt.Errorf("%w: 声明 %d 字节，上限 %d",
			ErrCoverTooLarge, declaredSize, imagetype.MaxBytes)
	}
	return s.objects.IssueUpload(ctx, CoverKey(skillID), imagetype.Rules(), false)
}

// CommitCoverUpload 核对字节确实到了，把技能指向它（唯一入口）。
//
// 签发之后客户端传了什么、传没传完，服务端都不知道，因此这里对那个键做一次 Head：
// 不存在即失败，字节数超过上限即失败并删除对象（那是这次失败唯一的残留物）。
func (s *Service) CommitCoverUpload(ctx context.Context, skillID string) (Skill, error) {
	if s.objects == nil {
		return Skill{}, ErrObjectStoreUnavailable
	}
	if _, err := s.store.GetSkill(ctx, skillID); err != nil {
		return Skill{}, err
	}
	key := CoverKey(skillID)
	if _, err := objectstore.VerifyUploaded(ctx, s.objects, key, imagetype.MaxBytes); err != nil {
		if errors.Is(err, objectstore.ErrUploadTooLarge) {
			return Skill{}, fmt.Errorf("%w: %w", ErrCoverTooLarge, err)
		}
		return Skill{}, mapObjectError(err)
	}
	if err := s.store.SetCover(ctx, skillID, key); err != nil {
		return Skill{}, err
	}
	s.logger.Info("技能封面已更新", zap.String("skill_id", skillID))
	return s.store.GetSkill(ctx, skillID)
}

// ClearCover 移除封面（唯一入口）。没有封面时也成功（幂等）。
func (s *Service) ClearCover(ctx context.Context, skillID string) (Skill, error) {
	if s.objects == nil {
		return Skill{}, ErrObjectStoreUnavailable
	}
	current, err := s.store.GetSkill(ctx, skillID)
	if err != nil {
		return Skill{}, err
	}
	if current.CoverKey == "" {
		return current, nil
	}
	// 先清库再删对象：反过来的话，删对象成功而清库失败会留下一个指向空键的封面，
	// 而那个状态的表现在界面上是"图裂了"。留着孤儿对象的代价只是几个字节。
	if err := s.store.SetCover(ctx, skillID, ""); err != nil {
		return Skill{}, err
	}
	if err := s.objects.Delete(ctx, current.CoverKey); err != nil {
		// 尽力而为：库里的行才是权威，桶上的孤儿对象没有功能影响（与 galaxy 的
		// 同一条结论）。
		s.logger.Warn("删除封面对象失败，技能已不再引用它", zap.String("skill_id", skillID))
	}
	return s.store.GetSkill(ctx, skillID)
}

// ViewOf 把一个领域对象包成对调用方呈现的样子（唯一入口）。
//
// **写路径必须走它。** 纳管、同步、回滚、改说明层、换封面各自返回"刚写下去的那一份"，
// 而封面地址是**读的时候才签发的**——不在这一处补上，就会出现"刚纳管回来的那条没有
// 封面、重新读一次才有"这种差异，而它在界面上表现成"卡片先是空的，刷新一下图才出来"。
func (s *Service) ViewOf(ctx context.Context, item Skill) View {
	return View{Skill: item, CoverURL: s.CoverURL(ctx, item)}
}

// CoverURL 生成封面的短时读取地址（唯一入口）。返回空串表示"当前没有可显示的封面"。
//
// 失败也返回空串：一张展示图取不到地址，不该让整个目录读不出来——界面会据此渲染
// 占位，而"图没出来"与"这一页打不开"是两件严重程度差很远的事。
func (s *Service) CoverURL(ctx context.Context, item Skill) string {
	if item.CoverKey == "" || s.objects == nil {
		return ""
	}
	address, err := s.objects.PresignGet(ctx, item.CoverKey, CoverURLTTL)
	if err != nil {
		s.logger.Warn("签发封面地址失败，按没有封面处理", zap.String("skill_id", item.ID))
		return ""
	}
	return address
}
