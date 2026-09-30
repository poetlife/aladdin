package telemetry

import (
	"testing"
	"time"

	telemetryv1 "github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1"
)

// 三个窗口都以 now 为锚，区间是半开的 [from, to)。
func TestWindowRange(t *testing.T) {
	// 固定在一个东八区时区上，让"本地零点"有一个不同于 UTC 的答案——
	// 用 UTC 测会让"有没有按时区取零点"这件事测不出来。
	location := time.FixedZone("UTC+8", 8*60*60)
	now := time.Date(2026, 9, 30, 21, 30, 0, 0, location)

	cases := []struct {
		name string
		in   telemetryv1.TimeWindow
		from time.Time
	}{
		{
			name: "今日从本地零点起",
			in:   telemetryv1.TimeWindow_TIME_WINDOW_TODAY,
			from: time.Date(2026, 9, 30, 0, 0, 0, 0, location),
		},
		{
			name: "近 7 天是滚动的 7×24 小时",
			in:   telemetryv1.TimeWindow_TIME_WINDOW_LAST_7_DAYS,
			from: now.Add(-7 * 24 * time.Hour),
		},
		{
			name: "近 30 天是滚动的 30×24 小时",
			in:   telemetryv1.TimeWindow_TIME_WINDOW_LAST_30_DAYS,
			from: now.Add(-30 * 24 * time.Hour),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from, to, ok := WindowRange(tc.in, now)
			if !ok {
				t.Fatalf("窗口 %v 应合法", tc.in)
			}
			if !from.Equal(tc.from) {
				t.Errorf("from = %v，期望 %v", from, tc.from)
			}
			if !to.Equal(now) {
				t.Errorf("to = %v，期望 now（%v）", to, now)
			}
			if !from.Before(to) {
				t.Errorf("区间应是 [from, to) 且 from < to，得到 from=%v to=%v", from, to)
			}
		})
	}
}

// 未指定或越界的窗口不是"回落到默认值"，而是一个明确的拒绝——
// 静默回落会让一个拼错的取值看起来像生效了。
func TestWindowRangeRejectsUnknown(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	for _, in := range []telemetryv1.TimeWindow{
		telemetryv1.TimeWindow_TIME_WINDOW_UNSPECIFIED,
		telemetryv1.TimeWindow(99),
	} {
		if _, _, ok := WindowRange(in, now); ok {
			t.Errorf("窗口 %v 应被拒绝", in)
		}
	}
}

// 最长窗口必须落在保留期之内：否则"近 30 天"永远看不到它最早那一段。
func TestLongestWindowFitsRetention(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	from, _, ok := WindowRange(telemetryv1.TimeWindow_TIME_WINDOW_LAST_30_DAYS, now)
	if !ok {
		t.Fatal("近 30 天应是合法窗口")
	}
	// 回收线：早于 now-Retention 的行会被删掉。
	cutoff := now.Add(-Retention)
	if from.Before(cutoff) {
		t.Errorf("最长窗口的起点 %v 早于回收线 %v：窗口最早的一段会被回收掉", from, cutoff)
	}
}
