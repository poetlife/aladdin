package galaxy

import (
	"strings"
	"testing"
)

// mustOriginFor 构造一个发布地址派生入口，失败即终止用例。
func mustOriginFor(t *testing.T, appBaseURL string) PublicOrigin {
	t.Helper()
	origin, err := NewPublicOrigin(testBucketOrigin, testPageOrigin, appBaseURL)
	if err != nil {
		t.Fatalf("构造发布地址失败: %v", err)
	}
	return origin
}

// 分享地址与内容地址**同路径、不同来源**：前者在主站（贴给别人的那一条），
// 后者在发布域（主站壳里 iframe 的落点）。两个槽都如此。
//
// 这一条是"地址由服务端算好"的落点：客户端拿到的两条地址都来自这里，它自己不拼。
func TestShareAndContentAddresses(t *testing.T) {
	origin := mustOriginFor(t, testAppOrigin)

	cases := []struct {
		slot        ContentSlot
		wantContent string
		wantShare   string
	}{
		{SlotSite, testPageOrigin + "/g/prj_x", testAppOrigin + "/g/prj_x"},
		{SlotDocs, testPageOrigin + "/g/prj_x/docs", testAppOrigin + "/g/prj_x/docs"},
	}
	for _, tc := range cases {
		if got := origin.ContentURL("prj_x", tc.slot); got != tc.wantContent {
			t.Errorf("%s 的内容地址 = %q，期望 %q", tc.slot, got, tc.wantContent)
		}
		if got := origin.ShareURL("prj_x", tc.slot); got != tc.wantShare {
			t.Errorf("%s 的分享地址 = %q，期望 %q", tc.slot, got, tc.wantShare)
		}
	}
}

// 帧祖先只有主站：发布物不该被任何别的站点嵌进页面（钓鱼框）。
//
// 它不是 `'self'`——那等于允许发布域自己被任何人嵌，正是这一条要挡的事。它也**不
// 影响直接打开**：frame-ancestors 管的是"能不能被嵌"，不是"能不能访问"。
func TestFrameAncestorsOnlyAllowApp(t *testing.T) {
	origin := mustOriginFor(t, testAppOrigin)

	if got := origin.FrameAncestorSource(); got != testAppOrigin {
		t.Errorf("帧祖先来源 = %q，期望 %q", got, testAppOrigin)
	}
	policy := ContentSecurityPolicy(origin)
	want := "frame-ancestors " + testAppOrigin
	if !strings.Contains(policy, want) {
		t.Errorf("内容安全策略里没有 %q：\n%s", want, policy)
	}
	if strings.Contains(policy, "frame-ancestors 'self'") {
		t.Error("帧祖先落成了 'self'：那等于允许发布域被任何页面嵌")
	}
}

// 主站对外地址缺省时分享地址为空——发布启用时配置校验会拒绝这种搭配，因此走到
// 这一档只可能是"没启用发布"。
//
// 内容地址不依赖它：iframe 的落点是发布域，与主站在哪无关。
func TestShareURLNeedsAppOrigin(t *testing.T) {
	origin := mustOriginFor(t, "")

	if got := origin.ShareURL("prj_x", SlotSite); got != "" {
		t.Errorf("没有主站地址时分享地址 = %q，期望空", got)
	}
	if got := origin.ContentURL("prj_x", SlotSite); got != testPageOrigin+"/g/prj_x" {
		t.Errorf("内容地址 = %q，不该受主站地址缺失影响", got)
	}
	// 取不到主站来源时落成"一个都不许嵌"，而不是省掉这一条——省掉等于回到
	// "谁都能嵌"。
	if policy := ContentSecurityPolicy(origin); !strings.Contains(policy, "frame-ancestors 'none'") {
		t.Errorf("主站地址缺失时应落成 frame-ancestors 'none'：\n%s", policy)
	}
}

// 零值（没有配置发布域）下两个地址都是空串：未启用发布是一条完整的配置形态，
// 不是"配了一半"。
func TestZeroOriginHasNoAddresses(t *testing.T) {
	var origin PublicOrigin

	if got := origin.ShareURL("prj_x", SlotSite); got != "" {
		t.Errorf("零值的分享地址 = %q，期望空", got)
	}
	if got := origin.ContentURL("prj_x", SlotSite); got != "" {
		t.Errorf("零值的内容地址 = %q，期望空", got)
	}
}

// 主站对外地址与**发布域**都允许本地回环上的 http——与配置里那条取值要求一致：
// 本地开发没有证书，写死 https 会让发布与预览在本机根本跑不起来。回环上没有
// 网络中间人，明文不构成新的暴露面。
//
// **桶地址不在此列**：它是对象存储的对外端点，没有"本机上的桶"这种情形。
func TestLoopbackHTTPException(t *testing.T) {
	t.Run("主站对外地址", func(t *testing.T) {
		origin, err := NewPublicOrigin(testBucketOrigin, testPageOrigin, "http://localhost:5173")
		if err != nil {
			t.Fatalf("回环上的 http 主站地址被拒: %v", err)
		}
		if got := origin.ShareURL("prj_x", SlotSite); got != "http://localhost:5173/g/prj_x" {
			t.Errorf("分享地址 = %q", got)
		}
	})

	t.Run("发布域", func(t *testing.T) {
		origin, err := NewPublicOrigin(testBucketOrigin, "http://127.0.0.1:9090", testAppOrigin)
		if err != nil {
			t.Fatalf("回环上的 http 发布域被拒: %v", err)
		}
		if got := origin.ContentURL("prj_x", SlotSite); got != "http://127.0.0.1:9090/g/prj_x" {
			t.Errorf("内容地址 = %q", got)
		}
	})

	t.Run("非回环主机上的 http 仍被拒", func(t *testing.T) {
		if _, err := NewPublicOrigin(testBucketOrigin, "http://pages.example.com", testAppOrigin); err == nil {
			t.Error("非回环主机上的 http 发布域被接受了")
		}
		if _, err := NewPublicOrigin("http://127.0.0.1:9000", testPageOrigin, testAppOrigin); err == nil {
			t.Error("回环上的 http 桶地址被接受了")
		}
	})
}
