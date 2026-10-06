package galaxy

import (
	"strconv"
	"strings"
	"testing"
)

// 接入桥：`docs` 槽的每一页带且**只带**这一段常量脚本。
//
// 判据见 docs/design/galaxy/site-model.md 的"平台接入桥"。这里钉住的是三件事：
// 桥恰好一段、它就是常量（不含用户写的东西）、以及它与通道常量没有漂移。
func TestDocsFrameBridgeIsTheOnlyInjectedScript(t *testing.T) {
	f := newFixture(t)
	project := f.createProjectSlots(t, "文档站", SlotDocs)
	// 正文里放一段独特的字符串，用它回答"脚本里有没有拼进用户内容"。
	const marker = "用户写的一段独特正文"
	f.pushDraftSlot(t, project.ID, SlotDocs, []Entry{
		f.textEntry(t, project.ID, "index.md", "# 首页\n\n"+marker+"\n"),
	})
	if report := f.reportSlot(t, project.ID, SlotDocs); !report.OK() {
		t.Fatalf("文档站被拒: %v", report.Messages())
	}

	page := string(f.buildArtifactsSlot(t, project.ID, SlotDocs)["index.html"])
	if count := strings.Count(page, "<script"); count != 1 {
		t.Fatalf("文档产物里的 <script> 有 %d 处，期望恰好 1 处（接入桥）", count)
	}
	if !strings.Contains(page, frameBridgeTag()) {
		t.Error("文档产物里的那一段不是接入桥")
	}
	// **桥是常量**：不含正文里的任何东西。这是"没有用户数据被拼进脚本"的判据。
	if strings.Contains(frameBridgeScript, marker) {
		t.Error("接入桥里出现了用户写的内容")
	}
	// 模板的占位都被填上了；漏填会留下 `%!` 这种标记。
	if strings.Contains(frameBridgeScript, "%!") {
		t.Errorf("接入桥的模板有没填上的占位：%s", frameBridgeScript)
	}
	// 填的是那两个常量——改了常量却忘了模板，宿主就认不出页面发的消息，
	// 而两侧的代码各自看都自洽。这一条把那种漂移挡在这里。
	if !strings.Contains(frameBridgeScript, "'"+FrameChannelName+"'") {
		t.Errorf("接入桥里没有通道标识 %q", FrameChannelName)
	}
	if !strings.Contains(frameBridgeScript, "VERSION="+strconv.Itoa(FrameChannelVersion)) {
		t.Errorf("接入桥里没有协议版本 %d", FrameChannelVersion)
	}
	// 向外发送**不限定目标来源**：不透明源的页面不知道宿主的来源（见 spec）。
	if !strings.Contains(frameBridgeScript, "postMessage({channel:CHANNEL,version:VERSION,type:'image-preview'") {
		t.Error("接入桥没有按约定的信封发出 image-preview")
	}
}

// `site` 槽不吃接入桥：那条路逐字交付用户的整站，aladdin 一个字节都不加。
func TestSiteArtifactsCarryNoFrameBridge(t *testing.T) {
	f := newFixture(t)
	project := f.staticSite(t, map[string]string{
		"index.html": "<!doctype html><html><body><p>hi</p></body></html>",
	})

	artifacts := f.buildArtifactsSlot(t, project.ID, SlotSite)
	if len(artifacts) == 0 {
		t.Fatal("整站没有产物")
	}
	for entryPath, body := range artifacts {
		if strings.Contains(string(body), FrameChannelName) {
			t.Errorf("site 产物 %s 里出现了接入桥", entryPath)
		}
	}
}
