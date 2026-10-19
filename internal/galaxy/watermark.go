package galaxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/watermark"
)

// 站点水印：发布出来的站点上怎么标记「这一份是给谁看的」。
//
// 水印本身由壳叠在 iframe 之上（见 docs/design/galaxy/watermark.md），前台一个字节
// 都不进用户内容——因此这里只做两件事：**签发一枚码并登记它对应谁**，以及**按码
// 查回那一次访问**。像素里只有码，码对回谁在库里，这一层间接是整套设计的关键：
// 就算有人把图案解出来，读到的也只是一串随机字符。
//
// 三个窗口都是常量而不是配置项（与预览凭证、会话、资产短时地址同一条取向）：配置
// 只回答"这个部署长什么样"，而"登记留多久"能让谁获得什么——答案不是"什么也不能"。

// WatermarkReuseWindow 是同一访客复用同一枚码的窗口。
//
// 窗口内同一（工程，槽，访客）重复打开得到同一枚码：**登记表的增长于是由访客数
// 决定，而不是由页面浏览量决定**；也因此"一张截图带同一枚标识"成立——提取时不
// 必先知道那是哪一次打开，这恰恰是它能对抗裁剪与缩放的前提。
const WatermarkReuseWindow = 24 * time.Hour

// WatermarkRetention 是一条登记保留多久。
//
// 它是**窗口**而不是"码的有效期"：过了这个时间，码仍然解得出来，只是对不回人。
// 取值比复用窗口长一个量级：泄露的截图往往是在事后才被发现的。
const WatermarkRetention = 90 * 24 * time.Hour

// ErrWatermarkNotFound 是一次查询"查不到"的唯一否定结论。
//
// 码形状非法、码不存在、超出保留窗口——**全是同一个结论**。与发布态那条六合一
// 否定同一条取向：区分它们只会告诉一个拿着截图的人"这枚码曾经是真的"。
//
// 而且这里比发布态更不能区分：溯源场景里**误报比查不到更糟**——它会把一次泄露
// 指向一个没看过这份内容的人。
var ErrWatermarkNotFound = errors.New("水印查不到")

// WatermarkVisit 是一条被水印标记过的访问。
//
// 它**不含任何内容**，也不含浏览器标识的原值：原值是一串能指纹化这台设备的字符串，
// 而溯源只需要"是不是同一台"，因此只留摘要（见 DigestUserAgent）。
type WatermarkVisit struct {
	// Code 是水印码：像素里编着的就是它。
	Code string
	// ProjectID 与 Slot 是这次访问落在哪个工程的哪个槽。
	ProjectID string
	Slot      ContentSlot
	// SubjectID 是访客的主体标识。**匿名为空**——那一档只记来源与浏览器摘要。
	//
	// 身份优先取主体：带着会话打开分享地址时，这是"这一份是给谁的"最强的一种回答。
	SubjectID string
	// IdentityKey 是"同一访客"这个判据的取值，由 WatermarkViewer.IdentityKey
	// 派生（唯一入口）。存下来是为了复用时按它找，而不是每次重新算一份——
	// 匿名那一档的取值包含来源地址，而来源地址是会变的**输入**，判据不能跟着它漂。
	IdentityKey string
	// SourceAddress 是访客的来源地址。**只进这张表**：不进日志、不进明水印、
	// 不进给访客的响应。
	SourceAddress string
	// UserAgentDigest 是浏览器标识的摘要（见 DigestUserAgent）。
	UserAgentDigest string
	// VisitedAt 是这次访问的时刻。
	VisitedAt time.Time
}

// Expired 判定这条登记在给定时刻是否已超出保留期。
//
// **判定只比较时间**：一条没被回收掉的行也不会多给一秒钟的可查性（与预览凭证
// 同一条）。
func (v WatermarkVisit) Expired(now time.Time) bool {
	return !now.Before(v.VisitedAt.Add(WatermarkRetention))
}

// WatermarkViewer 是"谁在打开这一页"的那几个维度。
//
// 它是水印登记的全部身份输入，**且只在水印这一条路上用**：发布态的判定一个字节都
// 不看它（见 docs/design/galaxy/publication.md 的"发布即公开"）。
type WatermarkViewer struct {
	// SubjectID 是已确认的会话主体。空表示这次访问是匿名的。
	SubjectID string
	// SourceAddress 是来源地址。
	SourceAddress string
	// UserAgent 是浏览器标识的**原值**，进库前会折成摘要。
	UserAgent string
}

// DigestUserAgent 把浏览器标识折成摘要（唯一入口）。
//
// 取 sha256 的前 8 字节、十六进制。**不存原值**：溯源只需要"是不是同一台"，而原值
// 进库就是一条可长期关联的指纹。
func DigestUserAgent(userAgent string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(userAgent)))
	return hex.EncodeToString(sum[:8])
}

// IdentityKey 返回这次访问在"同一访客"这一维度上的键（唯一入口）。
//
// 有主体时用主体：换一台设备、换一个网络，仍然是同一个人。没有主体时退到
// （来源地址，浏览器标识摘要）这一对——那是匿名能给出的最强的一种"同一访客"。
func (v WatermarkViewer) IdentityKey() string {
	if v.SubjectID != "" {
		return "subject:" + v.SubjectID
	}
	return "anon:" + strings.TrimSpace(v.SourceAddress) + "\x00" + DigestUserAgent(v.UserAgent)
}

// watermarkDisplayText 给出明水印要显示的那一行字（唯一入口）。
//
// **由服务端给出，不让前端拼**：水印显示什么是产品决定，而拼字符串是它的实现；
// 两处各拼一份的表现是"壳上写的与提取出来的不是同一枚码"。
//
// 它**不含任何身份明文**：没有主体标识、没有昵称、没有来源地址。发布态本身是公开
// 匿名的（见 docs/design/galaxy/README.md 的"发布即公开"），把访客身份印在一张会被
// 转发的图上，等于把"谁看过这一页"交给任何拿到图的人——而这与溯源无关，码就够了。
func watermarkDisplayText(code string, at time.Time) string {
	short := code
	if len(short) > 8 {
		short = short[:8]
	}
	// 日期取 UTC 而不是本地时区：同一枚码在"刚签发"与"从库里读回来"两条路上必须
	// 印出同一个日期，而库里存的是时刻、读回来的时区不由我们决定。
	return "aladdin · " + at.UTC().Format("2006-01-02") + " · " + short
}

// WatermarkMaterial 是壳要叠上去的那一层东西。
type WatermarkMaterial struct {
	// DisplayText 是明水印显示的那一行字。
	DisplayText string
	// Pattern 是盲水印的平铺图案（PNG 字节）。**由服务端生成**：前端再算一遍就是
	// 同一个算法的第二份实现，而它的漂移只有在拿到一张真截图时才会被发现。
	//
	// 图案按**原尺寸**（CSS 像素）铺在壳上，因此它的几何只有一处定义——生成它的那
	// 一处。壳不按另一个尺寸去等比拉伸它：拉一格就等于换了一套格子尺寸，而提取侧
	// 假设的是生成时那一套。
	Pattern []byte
}

// IssueWatermark 为一次访问签发（或复用）一枚水印码并登记它（唯一入口）。
//
// **签发时机只有一个**：壳解析分享地址的那一次调用——那是访客打开分享地址时平台
// 唯一一次知道有人来了的时刻（见 docs/design/galaxy/watermark.md）。
//
// 复用与登记是一步：窗口内命中一条登记就直接用它，不新增行。查询与写入放在同一
// 个临界区（存储侧一个事务）里，否则两次同时打开会各签一枚、各写一行。
func (s *Service) IssueWatermark(ctx context.Context, projectID string, slot ContentSlot, viewer WatermarkViewer, now time.Time) (WatermarkVisit, error) {
	code, err := watermark.NewCode()
	if err != nil {
		return WatermarkVisit{}, err
	}
	candidate := WatermarkVisit{
		Code:            code,
		ProjectID:       projectID,
		Slot:            slot,
		SubjectID:       viewer.SubjectID,
		IdentityKey:     viewer.IdentityKey(),
		SourceAddress:   strings.TrimSpace(viewer.SourceAddress),
		UserAgentDigest: DigestUserAgent(viewer.UserAgent),
		VisitedAt:       now,
	}
	// 复用与登记是**一次**存储操作（见 ReuseOrPutWatermarkVisit）：分两步的话，
	// 同一访客同时打开两次会各签一枚，"窗口内复用同一枚码"当场失效。
	//
	// 命中时沿用那一条，**连 VisitedAt 也不改写**——它记的是这枚码第一次发出去的
	// 时刻，而明水印上印的那个日期正是它。
	return s.store.ReuseOrPutWatermarkVisit(ctx, candidate, now.Add(-WatermarkReuseWindow), now.Add(-WatermarkRetention))
}

// WatermarkFor 返回一次访问要叠上去的那一层东西。
//
// 图案由算法包生成；生成失败时**只有盲水印缺席**，明水印照旧——一次图案生成的
// 失败不该让一个访客打不开页面（那也是水印唯一允许的降级方向）。
func (s *Service) WatermarkFor(visit WatermarkVisit) WatermarkMaterial {
	material := WatermarkMaterial{
		DisplayText: watermarkDisplayText(visit.Code, visit.VisitedAt),
	}
	// 图案生成失败时**只有盲水印缺席**，明水印照旧——一次图案生成的失败不该让一个
	// 访客打不开页面（那也是水印唯一允许的降级方向）。失败原因由调用方留痕。
	if pattern, err := watermark.Tile(visit.Code); err == nil {
		material.Pattern = pattern
	} else if s.logger != nil {
		s.logger.Warn("生成水印图案失败，本次只有明水印",
			zap.String("code", visit.Code),
			zap.Error(err))
	}
	return material
}

// LookupWatermark 按枚码查回那一次访问（唯一入口）。
//
// **查不到不是错误的一种特例，而是唯一的那种否定**：码形状非法、不存在、超出保留
// 窗口，一律 ErrWatermarkNotFound（理由见 ErrWatermarkNotFound 的说明）。
func (s *Service) LookupWatermark(ctx context.Context, code string, now time.Time) (WatermarkVisit, error) {
	if !watermark.ValidCode(code) {
		return WatermarkVisit{}, ErrWatermarkNotFound
	}
	visit, err := s.store.GetWatermarkVisit(ctx, code)
	if err != nil {
		if errors.Is(err, ErrWatermarkNotFound) {
			return WatermarkVisit{}, ErrWatermarkNotFound
		}
		return WatermarkVisit{}, err
	}
	if visit.Expired(now) {
		return WatermarkVisit{}, ErrWatermarkNotFound
	}
	return visit, nil
}
