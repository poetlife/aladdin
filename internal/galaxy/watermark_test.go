package galaxy

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/poetlife/aladdin/internal/watermark"
)

// 站点水印的领域侧：签发、复用、按码查回，以及**它不碰内容**。
//
// 算法本身（图案怎么画、怎么解得回来）在 internal/watermark 里有它自己的测试；
// 这里只钉住"谁算同一个访客""窗口内复用""查不到只有一个结论"这几条，以及
// "产品物逐字不变"这条本模块最要紧的不变量（见 docs/design/galaxy/watermark.md）。

// viewer 造一个访客身份。
func viewer(subject, source, userAgent string) WatermarkViewer {
	return WatermarkViewer{SubjectID: subject, SourceAddress: source, UserAgent: userAgent}
}

// TestIssueWatermarkRegistersSubject 钉住"身份优先取主体"。
func TestIssueWatermarkRegistersSubject(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "水印·主体")

	visit, err := f.service.IssueWatermark(context.Background(), project.ID, SlotSite,
		viewer(testOther, "203.0.113.9", "Mozilla/5.0 (Macintosh)"), f.now)
	if err != nil {
		t.Fatalf("签发水印失败: %v", err)
	}
	if !watermark.ValidCode(visit.Code) {
		t.Fatalf("签发的码不是合法水印码: %q", visit.Code)
	}
	if visit.SubjectID != testOther {
		t.Fatalf("登记的主体 = %q，期望 %q", visit.SubjectID, testOther)
	}
	if visit.ProjectID != project.ID || visit.Slot != SlotSite {
		t.Fatalf("登记的归属 = (%s, %s)，期望 (%s, %s)", visit.ProjectID, visit.Slot, project.ID, SlotSite)
	}
	// 浏览器标识**只进摘要**：原值是一串能指纹化设备的字符串，而溯源只需要
	// "是不是同一台"。
	if visit.UserAgentDigest == "" || strings.Contains(visit.UserAgentDigest, "Mozilla") {
		t.Fatalf("浏览器标识没有折成摘要: %q", visit.UserAgentDigest)
	}

	found, err := f.service.LookupWatermark(context.Background(), visit.Code, f.now)
	if err != nil {
		t.Fatalf("按码查回失败: %v", err)
	}
	if found.Code != visit.Code || found.SubjectID != testOther {
		t.Fatalf("查回的登记与签发的不一致: %+v", found)
	}
}

// TestIssueWatermarkWithoutSessionFallsBackToSource 钉住匿名那一档：没有主体时记来源
// 与浏览器摘要，而那仍然是同一次访问的一条留痕。
func TestIssueWatermarkWithoutSessionFallsBackToSource(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "水印·匿名")

	visit, err := f.service.IssueWatermark(context.Background(), project.ID, SlotDocs,
		viewer("", "203.0.113.9", "Mozilla/5.0"), f.now)
	if err != nil {
		t.Fatalf("签发水印失败: %v", err)
	}
	if visit.SubjectID != "" {
		t.Fatalf("匿名访问不该有主体，得到 %q", visit.SubjectID)
	}
	if visit.SourceAddress != "203.0.113.9" {
		t.Fatalf("来源地址 = %q，期望 203.0.113.9", visit.SourceAddress)
	}
	if visit.UserAgentDigest == "" {
		t.Fatal("匿名访问也要记浏览器摘要：那是「同一台」的另一半判据")
	}
}

// TestIssueWatermarkReusesWithinWindow 钉住复用：窗口内同一访客得到同一枚码，且
// **只新增一行**——登记表的增长由访客数决定，而不是由页面浏览量决定。
func TestIssueWatermarkReusesWithinWindow(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "水印·复用")

	first, err := f.service.IssueWatermark(context.Background(), project.ID, SlotSite,
		viewer(testOwner, "198.51.100.7", "UA-1"), f.now)
	if err != nil {
		t.Fatalf("第一次签发失败: %v", err)
	}
	second, err := f.service.IssueWatermark(context.Background(), project.ID, SlotSite,
		viewer(testOwner, "198.51.100.7", "UA-1"), f.now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("第二次签发失败: %v", err)
	}
	if second.Code != first.Code {
		t.Fatalf("窗口内同一访客拿到了两枚码：%q 与 %q", first.Code, second.Code)
	}
	// VisitedAt 是"这枚码第一次发出去"的时刻，不被复用改写——明水印上印的日期
	// 就是它。
	if !second.VisitedAt.Equal(first.VisitedAt) {
		t.Fatalf("复用改写了首次访问时间：%v → %v", first.VisitedAt, second.VisitedAt)
	}
	if got := f.store.watermarkCount(); got != 1 {
		t.Fatalf("窗口内复用了却写了 %d 行，期望 1 行", got)
	}

	// 超出复用窗口之后重新签一枚：那时它已经是"另一次访问"了。
	third, err := f.service.IssueWatermark(context.Background(), project.ID, SlotSite,
		viewer(testOwner, "198.51.100.7", "UA-1"), f.now.Add(WatermarkReuseWindow+time.Minute))
	if err != nil {
		t.Fatalf("第三次签发失败: %v", err)
	}
	if third.Code == first.Code {
		t.Fatal("超出复用窗口后仍沿用旧码：登记表的增长会变成按页面浏览量")
	}
}

// TestIssueWatermarkReusesAcrossPorts 钉住"端口不是身份的一部分"。
//
// 一次请求的远端地址是 `主机:端口`，而端口是每条连接临时分配的。把它算进判据里，
// 同一个人每打开一次页面就是一个新访客，**窗口内复用当场失效**——登记表的增长
// 于是又变成按页面浏览量，而那正是复用本来要消掉的东西。
//
// 这条是在真机验收时发现的：同一个浏览器连开三次，库里写了三行、三个不同的码。
func TestIssueWatermarkReusesAcrossPorts(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "水印·端口")

	first, err := f.service.IssueWatermark(context.Background(), project.ID, SlotSite,
		viewer("", "127.0.0.1:50185", "UA-1"), f.now)
	if err != nil {
		t.Fatalf("第一次签发失败: %v", err)
	}
	second, err := f.service.IssueWatermark(context.Background(), project.ID, SlotSite,
		viewer("", "127.0.0.1:50404", "UA-1"), f.now.Add(time.Minute))
	if err != nil {
		t.Fatalf("第二次签发失败: %v", err)
	}
	if second.Code != first.Code {
		t.Fatalf("换了临时端口的同一访客拿到了两枚码：%q 与 %q", first.Code, second.Code)
	}
	if got := f.store.watermarkCount(); got != 1 {
		t.Fatalf("同一访客写了 %d 行，期望 1 行", got)
	}
	// 登记里存的也应当是**去掉端口**的来源：留着端口只会让同一个人在库里看起来
	// 像很多个人。
	if second.SourceAddress != "127.0.0.1" {
		t.Fatalf("登记里的来源 = %q，期望 127.0.0.1", second.SourceAddress)
	}
}

// TestNormalizeSourceAddress 钉住这一处的形状（它是唯一入口）。
func TestNormalizeSourceAddress(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:50185":    "127.0.0.1",
		"[::1]:50404":        "::1",
		"203.0.113.9:443":    "203.0.113.9",
		"203.0.113.9":        "203.0.113.9",
		"":                   "",
		"  host.example:80 ": "host.example",
	}
	for input, want := range cases {
		if got := NormalizeSourceAddress(input); got != want {
			t.Errorf("NormalizeSourceAddress(%q) = %q，期望 %q", input, got, want)
		}
	}
}

// TestIssueWatermarkIsPerProjectAndSlot 钉住复用不跨工程、不跨槽：同一访客打开两个
// 工程得到两枚码——"这一份是哪个站点上的哪一次访问"必须能分开。
func TestIssueWatermarkIsPerProjectAndSlot(t *testing.T) {
	f := newFixture(t)
	first := f.createProject(t, "水印·工程一")
	second := f.createProject(t, "水印·工程二")
	who := viewer(testOwner, "198.51.100.7", "UA-1")

	a, err := f.service.IssueWatermark(context.Background(), first.ID, SlotSite, who, f.now)
	if err != nil {
		t.Fatalf("工程一签发失败: %v", err)
	}
	b, err := f.service.IssueWatermark(context.Background(), second.ID, SlotSite, who, f.now)
	if err != nil {
		t.Fatalf("工程二签发失败: %v", err)
	}
	if a.Code == b.Code {
		t.Fatal("两个工程共用了同一枚码：提取出来将无法回答「是哪一份内容被带走」")
	}

	// 同一个工程的两个槽也要分开：文档槽与站点槽是两份内容。
	c, err := f.service.IssueWatermark(context.Background(), first.ID, SlotDocs, who, f.now)
	if err != nil {
		t.Fatalf("文档槽签发失败: %v", err)
	}
	if c.Code == a.Code {
		t.Fatal("同一工程的两个槽共用了同一枚码")
	}
}

// TestLookupWatermarkFoldsEveryMissIntoOneAnswer 钉住"查不到只有一个结论"：形状非法、
// 不存在、超出保留期，三者同一个错误。
//
// 溯源场景里**误报比查不到更糟**，但反过来"能区分出这枚码曾经是真的"同样是个坏
// 信号——它把一次查询变成一次试探。
func TestLookupWatermarkFoldsEveryMissIntoOneAnswer(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "水印·否定结论")

	visit, err := f.service.IssueWatermark(context.Background(), project.ID, SlotSite,
		viewer(testOwner, "", ""), f.now)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}

	cases := []struct {
		name string
		code string
		at   time.Time
	}{
		{"形状非法", "not-a-code", f.now},
		{"不存在", "0123456789ABCDEF", f.now},
		{"超出保留期", visit.Code, f.now.Add(WatermarkRetention + time.Minute)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.service.LookupWatermark(context.Background(), tc.code, tc.at)
			if !errors.Is(err, ErrWatermarkNotFound) {
				t.Fatalf("期望 ErrWatermarkNotFound，得到 %v", err)
			}
		})
	}
}

// TestWatermarkMaterialCarriesPatternAndDisplayText 钉住壳要的那一层：
// 明水印那一行字**不含身份明文**，盲水印的图案由服务端生成。
func TestWatermarkMaterialCarriesPatternAndDisplayText(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "水印·呈现")
	visit, err := f.service.IssueWatermark(context.Background(), project.ID, SlotSite,
		viewer(testOwner, "203.0.113.9", "Mozilla/5.0"), f.now)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}

	material := f.service.WatermarkFor(visit)
	if len(material.Pattern) == 0 {
		t.Fatal("没有生成盲水印图案")
	}
	if !bytes.HasPrefix(material.Pattern, []byte("\x89PNG")) {
		t.Fatal("图案不是 PNG 字节")
	}
	if material.DisplayText == "" {
		t.Fatal("没有给出明水印文字")
	}
	// **不含身份明文**：把访客身份印在一张会被转发的图上，等于把"谁看过这一页"
	// 交给任何拿到图的人——而溯源只需要码。
	for _, forbidden := range []string{testOwner, "203.0.113.9", "Mozilla"} {
		if strings.Contains(material.DisplayText, forbidden) {
			t.Fatalf("明水印里出现了身份明文 %q: %q", forbidden, material.DisplayText)
		}
	}
	// 码的前几位在里面：那是"同一枚码"的唯一可见线索，也是抄下来能查的那一段。
	if !strings.Contains(material.DisplayText, visit.Code[:8]) {
		t.Fatalf("明水印里没有码的前几位: %q", material.DisplayText)
	}
}

// TestPublishIsUnaffectedByWatermark 钉住本模块最要紧的一条：**水印不改发布物**。
//
// 同一版本连发两次产物逐字相同，且产物里不出现水印码、图案与明水印文字——水印
// 是壳上的一层，不是注入进内容的东西（见 docs/design/galaxy/watermark.md 的"铺在哪"）。
func TestPublishIsUnaffectedByWatermark(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "水印·不改产物")
	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html", "<h1>正文</h1>"),
	})
	version := f.saveVersion(t, project.ID)

	first, err := f.service.Publish(context.Background(), testOwner, project.ID, SlotSite, version.ID)
	if err != nil {
		t.Fatalf("第一次发布失败: %v", err)
	}
	// 有一次真实的访问（因此有一枚码与一张图案），然后重新发同一个版本。
	visit, err := f.service.IssueWatermark(context.Background(), project.ID, SlotSite,
		viewer(testOwner, "203.0.113.9", "Mozilla/5.0"), f.now)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if err := f.service.Unpublish(context.Background(), testOwner, project.ID, SlotSite); err != nil {
		t.Fatalf("撤回失败: %v", err)
	}
	second, err := f.service.Publish(context.Background(), testOwner, project.ID, SlotSite, version.ID)
	if err != nil {
		t.Fatalf("第二次发布失败: %v", err)
	}

	// 整组产物的**字节**逐字相同，且里面一条水印都不出现——水印是壳上的一层，
	// 不是注入进内容的东西。
	if len(first.Manifest) != len(second.Manifest) {
		t.Fatalf("两次发布的产物条数不同：%d vs %d", len(first.Manifest), len(second.Manifest))
	}
	for index, entry := range first.Manifest {
		if entry != second.Manifest[index] {
			t.Fatalf("同一版本重新发布后产物变了：%+v → %+v", entry, second.Manifest[index])
		}
		if strings.Contains(entry.Path, visit.Code) || strings.Contains(entry.Digest, visit.Code) {
			t.Fatalf("产物里出现了水印码: %+v", entry)
		}
	}
	// 图案与明水印文字也不在产物里：它们只经壳下发（见 watermark.go 的 WatermarkFor）。
	material := f.service.WatermarkFor(visit)
	for _, entry := range first.Manifest {
		if entry.Path == material.DisplayText {
			t.Fatalf("明水印文字出现在产物路径里: %q", entry.Path)
		}
	}
}
