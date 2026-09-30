package telemetry

import (
	"time"

	telemetryv1 "github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1"
)

// WindowRange 把一个读侧时间窗折算成半开区间 [from, to)。
//
// 它是**窗口语义的唯一实现处**：接口层不自己算时间，存储层不认识时间窗，两边都
// 只消费这里给出的两个时刻。否则"今日从哪一刻算起"会在几处各写一份，而它们的
// 分歧表现为"页面上换个窗口数字就对不上"——一个没有报错、只让人犯嘀咕的偏差。
//
// 三个窗口都以 now 为锚：TODAY 是服务器本地时区的当日零点，另两个是滚动的 7 / 30
// 个 24 小时。因此**只有 TODAY 与服务器时区有关**，另两个在任何时区下都一样。
//
// 第二项为假表示这不是一个合法窗口（未指定或越界），调用方据此拒绝请求而不是
// 猜一个默认窗口——静默回落到某个窗口会让一个拼错的取值看起来像是生效了。
func WindowRange(w telemetryv1.TimeWindow, now time.Time) (from, to time.Time, ok bool) {
	switch w {
	case telemetryv1.TimeWindow_TIME_WINDOW_TODAY:
		year, month, day := now.Date()
		return time.Date(year, month, day, 0, 0, 0, 0, now.Location()), now, true
	case telemetryv1.TimeWindow_TIME_WINDOW_LAST_7_DAYS:
		return now.Add(-7 * 24 * time.Hour), now, true
	case telemetryv1.TimeWindow_TIME_WINDOW_LAST_30_DAYS:
		return now.Add(-30 * 24 * time.Hour), now, true
	default:
		return time.Time{}, time.Time{}, false
	}
}
