package telemetry

import (
	"testing"

	telemetryv1 "github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1"
	"github.com/poetlife/aladdin/internal/observability"
)

// webEvent 造一条形状合法的事件，测试再逐项改坏它。
func webEvent(action telemetryv1.Action) *telemetryv1.Event {
	return &telemetryv1.Event{
		Client:  telemetryv1.Client_CLIENT_WEB,
		Surface: telemetryv1.Surface_SURFACE_WEB_EDITOR,
		Action:  action,
		Result:  telemetryv1.Result_RESULT_OK,
	}
}

func TestNormalizeAcceptsWellFormedEvent(t *testing.T) {
	ev := webEvent(telemetryv1.Action_ACTION_AUTH_LOGIN)
	ev.Surface = telemetryv1.Surface_SURFACE_WEB_AUTH
	ev.Attrs = map[string]string{"channel": "google"}
	ev.DurationMs = 120
	ev.TraceId = "4bf92f3577b34da6a3ce929d0e0e4736"

	rec, reason, ok := normalize(ev, "sub_1", observability.ClientWeb)
	if !ok {
		t.Fatalf("合法事件被判为丢弃：%s", reason)
	}
	if rec.Client != "web" || rec.Surface != "web.auth" || rec.Action != "auth.login" || rec.Result != "ok" {
		t.Errorf("折算结果不对：%+v", rec)
	}
	if rec.SubjectID != "sub_1" || rec.DurationMS != 120 || rec.TraceID != ev.TraceId {
		t.Errorf("字段透传不对：%+v", rec)
	}
	if rec.Attrs["channel"] != "google" {
		t.Errorf("channel 应保留，得到 %v", rec.Attrs)
	}
}

func TestNormalizeRejectsBadShapes(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*telemetryv1.Event)
		reason string
	}{
		{"未知动作", func(e *telemetryv1.Event) { e.Action = telemetryv1.Action_ACTION_UNSPECIFIED }, reasonUnknownAction},
		{"未知界面", func(e *telemetryv1.Event) { e.Surface = telemetryv1.Surface_SURFACE_UNSPECIFIED }, reasonUnknownSurface},
		{"未知结局", func(e *telemetryv1.Event) { e.Result = telemetryv1.Result_RESULT_UNSPECIFIED }, reasonUnknownResult},
		{"未知上报端", func(e *telemetryv1.Event) { e.Client = telemetryv1.Client_CLIENT_UNSPECIFIED }, reasonUnknownClient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := webEvent(telemetryv1.Action_ACTION_PUBLISH)
			tc.mutate(ev)
			if _, reason, ok := normalize(ev, "sub_1", ""); ok || reason != tc.reason {
				t.Errorf("应被丢弃且原因 %q，得到 ok=%v reason=%q", tc.reason, ok, reason)
			}
		})
	}
}

// TestNormalizeEnforcesAnonymousAllowlist 是匿名与已认证两条路径的分界。
func TestNormalizeEnforcesAnonymousAllowlist(t *testing.T) {
	// 允许匿名的动作：无主体也放行（登录页失败、CLI 未登录退出）。
	login := webEvent(telemetryv1.Action_ACTION_AUTH_LOGIN)
	login.Surface = telemetryv1.Surface_SURFACE_WEB_AUTH
	if _, reason, ok := normalize(login, "", ""); !ok {
		t.Errorf("登录动作应允许匿名，却被丢弃：%s", reason)
	}

	// 其余动作在匿名时一律丢弃。
	publish := webEvent(telemetryv1.Action_ACTION_PUBLISH)
	if _, reason, ok := normalize(publish, "", ""); ok || reason != reasonAnonymousForbidden {
		t.Errorf("发布动作不应允许匿名，得到 ok=%v reason=%q", ok, reason)
	}
}

// TestNormalizeChecksClientAgainstHeader 覆盖"事件自报的端与请求头不一致"。
func TestNormalizeChecksClientAgainstHeader(t *testing.T) {
	ev := webEvent(telemetryv1.Action_ACTION_PUBLISH)
	if _, reason, ok := normalize(ev, "sub_1", observability.ClientCLI); ok || reason != reasonClientMismatch {
		t.Errorf("端不一致应被丢弃，得到 ok=%v reason=%q", ok, reason)
	}
	// 请求头缺失时不校验：老客户端可能还没带这个头。
	if _, _, ok := normalize(ev, "sub_1", ""); !ok {
		t.Error("请求头缺失时不应因端校验而丢弃")
	}
}

func TestNormalizeSanitizesAttrs(t *testing.T) {
	ev := webEvent(telemetryv1.Action_ACTION_AUTH_LOGIN)
	ev.Surface = telemetryv1.Surface_SURFACE_WEB_AUTH
	ev.Attrs = map[string]string{
		"channel":     "google", // 合法
		"command":     "galaxy", // 白名单外（登录动作不带它）
		"email":       "a@b.c",  // 未登记键
		"subject_id":  "sub_x",  // 未登记键（主体只允许来自会话）
		"authoritati": "1",      // 无关键
	}
	rec, _, ok := normalize(ev, "sub_1", "")
	if !ok {
		t.Fatal("应被接受")
	}
	if len(rec.Attrs) != 1 || rec.Attrs["channel"] != "google" {
		t.Errorf("只应保留 channel，得到 %v", rec.Attrs)
	}

	// 取值不在取值域内同样丢弃。
	ev.Attrs = map[string]string{"channel": "carrier-pigeon"}
	rec, _, ok = normalize(ev, "sub_1", "")
	if !ok {
		t.Fatal("事件本身应被接受")
	}
	if len(rec.Attrs) != 0 {
		t.Errorf("非法取值应被剥除，得到 %v", rec.Attrs)
	}
}

func TestNormalizeCommandShape(t *testing.T) {
	ev := webEvent(telemetryv1.Action_ACTION_CLI_LOCAL_FAIL)
	ev.Client = telemetryv1.Client_CLIENT_CLI
	ev.Surface = telemetryv1.Surface_SURFACE_CLI
	ev.Result = telemetryv1.Result_RESULT_FAIL

	for _, valid := range []string{"galaxy", "whoami", "device-login", "a1"} {
		ev.Attrs = map[string]string{"command": valid, "reason": "config"}
		rec, _, ok := normalize(ev, "", observability.ClientCLI)
		if !ok {
			t.Fatalf("合法命令 %q 应被接受", valid)
		}
		if rec.Attrs["command"] != valid || rec.Attrs["reason"] != "config" {
			t.Errorf("命令 %q 的属性不对：%v", valid, rec.Attrs)
		}
	}

	for _, invalid := range []string{"galaxy project save", "Galaxy", "", "galaxy/../etc", "命令"} {
		ev.Attrs = map[string]string{"command": invalid}
		rec, _, ok := normalize(ev, "", observability.ClientCLI)
		if !ok {
			t.Fatalf("事件本身应被接受（命令 %q 只是属性）", invalid)
		}
		if len(rec.Attrs) != 0 {
			t.Errorf("非法命令 %q 应被剥除，得到 %v", invalid, rec.Attrs)
		}
	}
}

func TestNormalizeBoundsDurationAndTraceID(t *testing.T) {
	ev := webEvent(telemetryv1.Action_ACTION_PUBLISH)
	ev.DurationMs = maxDurationMS + 1
	ev.TraceId = "4BF92F3577B34DA6A3CE929D0E0E4736" // 大写：不合规
	rec, _, ok := normalize(ev, "sub_1", "")
	if !ok {
		t.Fatal("应被接受")
	}
	if rec.DurationMS != 0 {
		t.Errorf("越界耗时应按未提供处理，得到 %d", rec.DurationMS)
	}
	if rec.TraceID != "" {
		t.Errorf("大写 trace_id 应被清空，得到 %q", rec.TraceID)
	}

	// 全零与非 32 位同样不合规。
	for _, bad := range []string{"00000000000000000000000000000000", "abc", ""} {
		ev.TraceId = bad
		rec, _, _ := normalize(ev, "sub_1", "")
		if rec.TraceID != "" {
			t.Errorf("非法 trace_id %q 应被清空，得到 %q", bad, rec.TraceID)
		}
	}
}

func TestNormalizeRejectsNilEvent(t *testing.T) {
	if _, reason, ok := normalize(nil, "sub_1", ""); ok || reason != reasonUnknownAction {
		t.Errorf("nil 事件应被丢弃，得到 ok=%v reason=%q", ok, reason)
	}
}
