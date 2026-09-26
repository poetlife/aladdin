package cosstore

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/poetlife/aladdin/internal/profile"
)

const (
	testBucketURL = "https://aladdin-1250000000.cos.ap-guangzhou.myqcloud.com"
	testSecretID  = "the-secret-id"
	testSecretKey = "the-secret-key"
)

// 桶地址必须是带主机名的 https：用明文把密钥与图片送出去，与"桶私有"这个
// 前提直接冲突。
func TestNewRejectsUnusableBucketURL(t *testing.T) {
	cases := []string{
		"http://aladdin-1250000000.cos.ap-guangzhou.myqcloud.com",
		"aladdin-1250000000.cos.ap-guangzhou.myqcloud.com",
		"https://",
		"",
	}
	for _, bucketURL := range cases {
		if _, err := New(Config{BucketURL: bucketURL, SecretID: testSecretID, SecretKey: testSecretKey}); err == nil {
			t.Errorf("桶地址 %q 被接受了，期望拒绝", bucketURL)
		}
	}
}

// 签发的地址必须是一个**带签名的、指向本主体对象键的**地址。
//
// 这条能在不联网的前提下断言，是因为预签名只做一次本地计算、不访问对象
// 存储（见 PresignGet 的说明）。桶私有这条约束全靠地址上的签名成立，
// 因此"签名在不在"必须被测试守着。
func TestPresignGetProducesSignedURL(t *testing.T) {
	const (
		subjectID = "usr_abc"
		ttl       = 10 * time.Minute
	)
	store, err := New(Config{BucketURL: testBucketURL, SecretID: testSecretID, SecretKey: testSecretKey})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}

	signed, err := store.PresignGet(context.Background(), subjectID, ttl)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}

	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("签发的地址不是合法 URL: %v", err)
	}
	if parsed.Host != "aladdin-1250000000.cos.ap-guangzhou.myqcloud.com" {
		t.Errorf("host = %q，期望落在配置的桶上", parsed.Host)
	}
	if !strings.Contains(parsed.Path, profile.AvatarKey(subjectID)) {
		t.Errorf("路径 = %q，期望含对象键 %q", parsed.Path, profile.AvatarKey(subjectID))
	}

	query := parsed.Query()
	if query.Get("q-signature") == "" {
		t.Error("地址上没有签名——未签名的地址在私有桶上取不到任何东西")
	}
	// q-sign-time 是这次签名的有效区间。它的存在就是"地址带有效期"的凭据。
	if query.Get("q-sign-time") == "" {
		t.Error("地址上没有有效期")
	}

	// 密钥**原文**不得出现在地址里。
	//
	// 注意 SecretID 是会出现的（在 q-ak 里）：那是 COS 的签名格式决定的，
	// 与 S3 把 AccessKeyId 放进预签名地址是一回事。真正不能出现的是 SecretKey。
	if strings.Contains(signed, testSecretKey) {
		t.Errorf("地址里出现了密钥原文: %s", signed)
	}
}

// 有效期由调用方给出，且必须真的落在签名上。
func TestPresignGetHonoursTTL(t *testing.T) {
	store, err := New(Config{BucketURL: testBucketURL, SecretID: testSecretID, SecretKey: testSecretKey})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}

	signed, err := store.PresignGet(context.Background(), "usr_abc", 90*time.Minute)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("签发的地址不是合法 URL: %v", err)
	}

	// q-sign-time 形如 "<起始秒>;<结束秒>"：两个时间戳之差就是这次签名的有效期。
	parts := strings.Split(parsed.Query().Get("q-sign-time"), ";")
	if len(parts) != 2 {
		t.Fatalf("q-sign-time = %q，期望两个以分号分隔的时间戳", parsed.Query().Get("q-sign-time"))
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		t.Fatalf("起始时间戳无法解析: %v", err)
	}
	end, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		t.Fatalf("结束时间戳无法解析: %v", err)
	}
	if got := time.Duration(end-start) * time.Second; got != 90*time.Minute {
		t.Errorf("有效期 = %v，期望 %v", got, 90*time.Minute)
	}
}
