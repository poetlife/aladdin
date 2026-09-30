package gormstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/database/migrate"
	"github.com/poetlife/aladdin/internal/telemetry"
)

// 两个存储实现共用同一套用例。
//
// 这是"同一份契约只有一套用例"的落点：给 SQL 实现单独写一套，两套就会各自
// 漂移，而漂移的表现形式是"换后端之后行为变了"——最难在事后归因的一类问题。
//
// 用例放在 gormstore 包内而不是 internal/telemetry：后者若导入 gormstore
// 会形成导入环（gormstore 依赖 telemetry）。

// storeCase 是一个存储实现的构造方式。
type storeCase struct {
	name string
	open func(t *testing.T) telemetry.Store
}

func storeCases(t *testing.T) []storeCase {
	t.Helper()
	return []storeCase{
		{
			name: "内存实现",
			open: func(*testing.T) telemetry.Store { return telemetry.NewMemoryStore() },
		},
		{
			name: "关系库实现",
			open: func(t *testing.T) telemetry.Store { return newTestStore(t) },
		},
	}
}

// forEachStore 把同一段用例跑在两个实现上。
func forEachStore(t *testing.T, run func(t *testing.T, store telemetry.Store)) {
	t.Helper()
	for _, sc := range storeCases(t) {
		t.Run(sc.name, func(t *testing.T) {
			run(t, sc.open(t))
		})
	}
}

// newTestStore 打开一个临时目录里的库并迁移，返回关系库实现。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := database.Open(config.DatabaseConfig{
		Driver: string(database.DialectSQLite),
		DSN:    filepath.Join(t.TempDir(), "telemetry.db"),
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := migrate.Run(context.Background(), db, zap.NewNop()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return New(db)
}

// entry 造一条事件，时间取自基准时刻偏移若干分钟。
//
// 时间取整秒：两组实现对"亚秒精度"的表示不同（内存是精确的 time.Time，
// 库里是格式化的字符串），而这条差异不是本模块要守的契约。契约要守的是
// 区间端点与排序，那些在整秒上就能完整覆盖。
func entry(base time.Time, offset time.Duration, action string, result string) telemetry.Entry {
	return telemetry.Entry{
		Record: telemetry.Record{
			Client:  "web",
			Surface: "web.editor",
			Action:  action,
			Result:  result,
		},
		OccurredAt: base.Add(offset).UTC().Truncate(time.Second),
	}
}

// wideRange 是一个把用例数据全包住的时间区间。
//
// 用它而不是"再来一个魔法时间"：明细用例关心的是排序与条数，不是端点；端点
// 由专门的那条用例覆盖（TestContractStatsHalfOpenRange 与 TestContractRecentWindow）。
func wideRange() (time.Time, time.Time) {
	return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
}

// 计数按 (client, action, result) 分组，且只返回出现过的组合。
func TestContractStatsGroups(t *testing.T) {
	forEachStore(t, func(t *testing.T, store telemetry.Store) {
		ctx := context.Background()
		base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

		if err := store.Append(ctx, []telemetry.Entry{
			entry(base, 0, "draft.save", "ok"),
			entry(base, time.Minute, "draft.save", "ok"),
			entry(base, 2*time.Minute, "draft.save", "blocked"),
			entry(base, 3*time.Minute, "publish", "fail"),
		}); err != nil {
			t.Fatalf("写入失败: %v", err)
		}

		stats, err := store.Stats(ctx, base.Add(-time.Hour), base.Add(time.Hour))
		if err != nil {
			t.Fatalf("统计失败: %v", err)
		}
		want := []telemetry.Stat{
			{Client: "web", Action: "draft.save", Result: "blocked", Count: 1},
			{Client: "web", Action: "draft.save", Result: "ok", Count: 2},
			{Client: "web", Action: "publish", Result: "fail", Count: 1},
		}
		if len(stats) != len(want) {
			t.Fatalf("统计行数 = %d，期望 %d：%+v", len(stats), len(want), stats)
		}
		for i := range want {
			if stats[i] != want[i] {
				t.Errorf("第 %d 行 = %+v，期望 %+v", i, stats[i], want[i])
			}
		}
	})
}

// 区间是半开的 [from, to)：端点上的事件属于哪一边，两组实现必须一致。
func TestContractStatsHalfOpenRange(t *testing.T) {
	forEachStore(t, func(t *testing.T, store telemetry.Store) {
		ctx := context.Background()
		from := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
		to := from.Add(10 * time.Minute)

		if err := store.Append(ctx, []telemetry.Entry{
			entry(from, -time.Second, "draft.save", "ok"),   // 区间外（早于 from）
			entry(from, 0, "draft.save", "ok"),              // from 是闭端，在内
			entry(from, 10*time.Minute, "draft.save", "ok"), // to 是开端，在外
		}); err != nil {
			t.Fatalf("写入失败: %v", err)
		}

		stats, err := store.Stats(ctx, from, to)
		if err != nil {
			t.Fatalf("统计失败: %v", err)
		}
		if len(stats) != 1 || stats[0].Count != 1 {
			t.Fatalf("半开区间统计 = %+v，期望恰好 1 条（from 端包含、to 端排除）", stats)
		}
	})
}

// 空窗口是一个合法的空答案，不是错误。
func TestContractStatsEmptyWindow(t *testing.T) {
	forEachStore(t, func(t *testing.T, store telemetry.Store) {
		from := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
		stats, err := store.Stats(context.Background(), from, from.Add(time.Hour))
		if err != nil {
			t.Fatalf("统计失败: %v", err)
		}
		if len(stats) != 0 {
			t.Errorf("空窗口应得到 0 行，得到 %+v", stats)
		}
	})
}

// 明细从新到旧；同一时间戳的多条按写入次序倒序（与 SQL 的 id DESC 对齐）。
func TestContractRecentOrderAndLimit(t *testing.T) {
	forEachStore(t, func(t *testing.T, store telemetry.Store) {
		ctx := context.Background()
		base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

		// 前三条同一时间戳，写入次序即它们的新旧兜底顺序。
		if err := store.Append(ctx, []telemetry.Entry{
			entry(base, 0, "assets.open", "ok"),
			entry(base, 0, "versions.open", "ok"),
			entry(base, 0, "preview.toggle", "ok"),
			entry(base, 5*time.Minute, "publish", "ok"),
		}); err != nil {
			t.Fatalf("写入失败: %v", err)
		}

		wideFrom, wideTo := wideRange()
		recent, err := store.Recent(ctx, wideFrom, wideTo, 10)
		if err != nil {
			t.Fatalf("读取明细失败: %v", err)
		}
		got := make([]string, 0, len(recent))
		for _, e := range recent {
			got = append(got, e.Action)
		}
		want := []string{"publish", "preview.toggle", "versions.open", "assets.open"}
		if len(got) != len(want) {
			t.Fatalf("明细条数 = %d，期望 %d：%v", len(got), len(want), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("第 %d 条 = %q，期望 %q（全部：%v）", i, got[i], want[i], got)
			}
		}

		limited, err := store.Recent(ctx, wideFrom, wideTo, 2)
		if err != nil {
			t.Fatalf("限量读取失败: %v", err)
		}
		if len(limited) != 2 || limited[0].Action != "publish" {
			t.Fatalf("limit=2 得到 %+v，期望最新两条", limited)
		}
	})
}

// 明细要带回全部已脱敏字段，读侧不能比写侧看到得少。
func TestContractRecentRoundTrip(t *testing.T) {
	forEachStore(t, func(t *testing.T, store telemetry.Store) {
		ctx := context.Background()
		base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

		in := telemetry.Entry{
			Record: telemetry.Record{
				Client:     "cli",
				Surface:    "cli",
				Action:     "cli.local_fail",
				Result:     "fail",
				DurationMS: 42,
				TraceID:    "4bf92f3577b34da6a3ce929d0e0e4736",
				SubjectID:  "subject/abc",
				Attrs:      map[string]string{"command": "galaxy", "reason": "config"},
			},
			OccurredAt: base,
		}
		if err := store.Append(ctx, []telemetry.Entry{in}); err != nil {
			t.Fatalf("写入失败: %v", err)
		}

		wideFrom, wideTo := wideRange()
		recent, err := store.Recent(ctx, wideFrom, wideTo, 1)
		if err != nil {
			t.Fatalf("读取明细失败: %v", err)
		}
		if len(recent) != 1 {
			t.Fatalf("期望 1 条，得到 %d", len(recent))
		}
		got := recent[0]
		if got.Client != in.Client || got.Surface != in.Surface || got.Action != in.Action ||
			got.Result != in.Result || got.DurationMS != in.DurationMS ||
			got.TraceID != in.TraceID || got.SubjectID != in.SubjectID {
			t.Fatalf("标量与写入不一致：%+v", got)
		}
		if !got.OccurredAt.Equal(in.OccurredAt) {
			t.Errorf("时间 = %v，期望 %v", got.OccurredAt, in.OccurredAt)
		}
		if len(got.Attrs) != len(in.Attrs) {
			t.Fatalf("属性 = %+v，期望 %+v", got.Attrs, in.Attrs)
		}
		for k, v := range in.Attrs {
			if got.Attrs[k] != v {
				t.Errorf("属性 %q = %q，期望 %q", k, got.Attrs[k], v)
			}
		}
	})
}

// 回收只删严格早于 cutoff 的行，并如实报出条数。
func TestContractDeleteBefore(t *testing.T) {
	forEachStore(t, func(t *testing.T, store telemetry.Store) {
		ctx := context.Background()
		cutoff := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

		if err := store.Append(ctx, []telemetry.Entry{
			entry(cutoff, -time.Minute, "draft.save", "ok"),
			entry(cutoff, 0, "publish", "ok"), // cutoff 上的行留着
			entry(cutoff, time.Minute, "unpublish", "ok"),
		}); err != nil {
			t.Fatalf("写入失败: %v", err)
		}

		removed, err := store.DeleteBefore(ctx, cutoff)
		if err != nil {
			t.Fatalf("回收失败: %v", err)
		}
		if removed != 1 {
			t.Fatalf("回收条数 = %d，期望 1（只删严格早于 cutoff 的那条）", removed)
		}

		wideFrom, wideTo := wideRange()
		recent, err := store.Recent(ctx, wideFrom, wideTo, 10)
		if err != nil {
			t.Fatalf("读取明细失败: %v", err)
		}
		if len(recent) != 2 {
			t.Fatalf("回收后应为 2 条，得到 %d", len(recent))
		}
	})
}

// 明细与计数共用同一套区间语义：窗口外的事件不该出现在明细里。
func TestContractRecentWindow(t *testing.T) {
	forEachStore(t, func(t *testing.T, store telemetry.Store) {
		ctx := context.Background()
		from := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
		to := from.Add(time.Hour)

		if err := store.Append(ctx, []telemetry.Entry{
			entry(from, -time.Minute, "draft.save", "ok"), // 早于窗口
			entry(from, time.Minute, "publish", "ok"),     // 窗口内
			entry(to, time.Minute, "unpublish", "ok"),     // 晚于窗口
		}); err != nil {
			t.Fatalf("写入失败: %v", err)
		}

		recent, err := store.Recent(ctx, from, to, 10)
		if err != nil {
			t.Fatalf("读取明细失败: %v", err)
		}
		if len(recent) != 1 || recent[0].Action != "publish" {
			t.Fatalf("窗口内明细 = %+v，期望恰好 publish 一条", recent)
		}
	})
}
