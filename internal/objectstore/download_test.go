package objectstore

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"
)

// 下载那一档的响应头是**固定**的：中性的内容类型加上 attachment。
//
// 这一条是 galaxy 的附件能收任意类型的前提（见 docs/design/galaxy/attachments.md）：
// 文件名以什么结尾都不改变它。
func TestDownloadDispositionIsForced(t *testing.T) {
	disposition := DownloadDisposition("index.html")
	if !strings.HasPrefix(disposition, "attachment") {
		t.Fatalf("disposition = %q，期望以 attachment 开头", disposition)
	}
	if !strings.Contains(disposition, `filename="index.html"`) {
		t.Errorf("disposition 缺少 ASCII 回退名：%q", disposition)
	}
	if !strings.Contains(disposition, `filename*=UTF-8''index.html`) {
		t.Errorf("disposition 缺少 RFC 5987 的名字：%q", disposition)
	}
}

// 非 ASCII 文件名两种写法都在：回退名被压成 ASCII，完整名字走 `filename*`。
//
// 只给后者会让一部分客户端存成一个乱码名字；只给前者则中文名丢了。
func TestDownloadDispositionKeepsNonASCII(t *testing.T) {
	const filename = "构建产物.zip"
	disposition := DownloadDisposition(filename)
	if !strings.Contains(disposition, `filename="____.zip"`) {
		t.Errorf("ASCII 回退名不对：%q", disposition)
	}

	// `filename*` 那一段要能**逐字解回原名**——这是"用户看到的还是他给的那个名字"
	// 的判据，比逐字比较那串百分号编码结实。
	marker := "filename*=UTF-8''"
	index := strings.Index(disposition, marker)
	if index < 0 {
		t.Fatalf("disposition 缺少 RFC 5987 的名字：%q", disposition)
	}
	encoded := disposition[index+len(marker):]
	decoded, err := url.PathUnescape(encoded)
	if err != nil {
		t.Fatalf("RFC 5987 的名字解不开：%v", err)
	}
	if decoded != filename {
		t.Errorf("解回来的名字 = %q，期望 %q", decoded, filename)
	}
}

// 文件名是客户端给的自由文本，**必须先滤再拼**：引号会提前闭合 `filename="…"`，
// 反斜杠是它的转义字符，而换行可以凭空多出一个响应头。
func TestDownloadDispositionSanitizesFilename(t *testing.T) {
	disposition := DownloadDisposition("ev\"il\\name\r\nX-Injected: 1")

	if strings.ContainsAny(disposition, "\r\n") {
		t.Fatalf("disposition 里有换行，响应头注入：%q", disposition)
	}
	if strings.Contains(disposition, `"evilname`) || strings.Contains(disposition, `\`) {
		t.Errorf("引号与反斜杠没有被替换：%q", disposition)
	}
	if strings.Count(disposition, `"`) != 2 {
		t.Errorf("引号的数量不是一对，说明名字里还有未被转义的引号：%q", disposition)
	}
	if !strings.Contains(disposition, "X-Injected: 1") {
		// 名字里的冒号与空格本身无害，保留它们让用户看得出这里原来写的是什么。
		t.Errorf("保留下来的部分丢了：%q", disposition)
	}
}

// 名字为空（或滤完为空）时只给 `attachment`：**"要下载"这件事不依赖有名**。
func TestDownloadDispositionWithoutFilename(t *testing.T) {
	for _, filename := range []string{"", "   ", "\n"} {
		if got := DownloadDisposition(filename); got != "attachment" {
			t.Errorf("DownloadDisposition(%q) = %q，期望 attachment", filename, got)
		}
	}
}

// 名字过长时截断：响应头里的名字只影响"存到本地叫什么"，而它的长度由客户端决定。
func TestDownloadDispositionTruncates(t *testing.T) {
	disposition := DownloadDisposition(strings.Repeat("a", 400))
	if len(disposition) > 400 {
		t.Errorf("disposition 没有截断：%d 字节", len(disposition))
	}
	if !strings.Contains(disposition, "attachment") {
		t.Errorf("截断之后仍应是一个附件头：%q", disposition)
	}
}

// 内存实现的下载地址带着固定下来的那两个参数，且留了签发记录。
func TestMemoryStorePresignDownload(t *testing.T) {
	store := NewMemoryStore()
	signed, err := store.PresignDownload(context.Background(), "galaxy/prj_x/attachments/atc_y", "a.zip", time.Minute)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if !strings.Contains(signed, "response-content-type="+url.QueryEscape(NeutralContentType)) {
		t.Errorf("地址里没有固定的内容类型：%s", signed)
	}
	downloads := store.Downloads()
	if len(downloads) != 1 {
		t.Fatalf("签发记录 %d 条，期望 1 条", len(downloads))
	}
	if downloads[0].Disposition != DownloadDisposition("a.zip") {
		t.Errorf("记录的 disposition = %q", downloads[0].Disposition)
	}
}
