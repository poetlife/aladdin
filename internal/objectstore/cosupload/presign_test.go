package cosupload

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/poetlife/aladdin/internal/objectstore"
)

// mustStore 构造一个不联网的 COS 实现：签发读取与下载地址都只是对"地址 + 密钥 +
// 有效期"做一次签名，不发起任何请求。
func mustStore(t *testing.T) *Store {
	t.Helper()
	store, err := New(Config{
		BucketURL: testBucketURL,
		SecretID:  "AKIDexample",
		SecretKey: "secretexample",
	})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	return store
}

// 下载地址把**强制下载**的两个响应头写进查询串，而且它们参与签名。
//
// 这是"附件收任意类型"能成立的那一层保证：签名覆盖整条请求（含查询串），因此地址
// 一旦签发，那两个参数就改不动——改一个字节都会让签名对不上。**它在离线上可断言**，
// 而"桶真的照做了"只能在部署后冒烟里验。
func TestPresignDownloadPinsResponseHeaders(t *testing.T) {
	store := mustStore(t)
	signed, err := store.PresignDownload(context.Background(),
		"galaxy/prj_x/attachments/atc_y", "index.html", 5*time.Minute)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("解析地址失败: %v", err)
	}
	query := parsed.Query()
	if got := query.Get("response-content-type"); got != objectstore.NeutralContentType {
		t.Errorf("response-content-type = %q，期望 %q", got, objectstore.NeutralContentType)
	}
	disposition := query.Get("response-content-disposition")
	if disposition != objectstore.DownloadDisposition("index.html") {
		t.Errorf("response-content-disposition = %q，期望 %q",
			disposition, objectstore.DownloadDisposition("index.html"))
	}
	// `.html` 结尾的文件名不改变任何一项：固定的那一套与文件自己无关。
	if got := query.Get("response-content-type"); got == "text/html" {
		t.Error("下发类型取到了文件自己的类型")
	}
	if query.Get("sign") == "" && query.Get("q-signature") == "" {
		t.Error("地址没有签名")
	}
}

// 普通读取那一档**不带**这两个参数：它的内容类型是对象自己的属性。
//
// 两档的差别只有这一处，因此把它钉住：多带一个参数会让"谁在用哪一档"变得含糊，
// 而不带则会把保险箱里的字节以一个可渲染的类型下发。
func TestPresignGetDoesNotPinResponseHeaders(t *testing.T) {
	store := mustStore(t)
	signed, err := store.PresignGet(context.Background(), "galaxy/prj_x/assets/image/ast_y", 10*time.Minute)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("解析地址失败: %v", err)
	}
	query := parsed.Query()
	if query.Get("response-content-type") != "" || query.Get("response-content-disposition") != "" {
		t.Errorf("普通读取不该固定响应头：%s", signed)
	}
}
