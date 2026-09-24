package observability

import (
	"context"
	"testing"
)

func TestTraceIDRoundTrip(t *testing.T) {
	ctx := WithTraceID(context.Background(), "abc123")
	if got := TraceIDFromContext(ctx); got != "abc123" {
		t.Errorf("TraceIDFromContext = %q, want abc123", got)
	}
}

func TestEnsureTraceIDKeepsExisting(t *testing.T) {
	ctx := WithTraceID(context.Background(), "keep-me")
	if got := TraceIDFromContext(EnsureTraceID(ctx)); got != "keep-me" {
		t.Errorf("已有链路标识被覆盖为 %q", got)
	}
}

func TestEnsureTraceIDGenerates(t *testing.T) {
	ctx := EnsureTraceID(context.Background())
	first := TraceIDFromContext(ctx)
	if first == "" {
		t.Fatal("未生成链路标识")
	}
	if second := TraceIDFromContext(EnsureTraceID(ctx)); second != first {
		t.Errorf("重复调用 EnsureTraceID 生成了新标识：%q -> %q", first, second)
	}
}

func TestNewTraceIDIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id := NewTraceID()
		if id == "" {
			t.Fatal("生成了空链路标识")
		}
		if seen[id] {
			t.Fatalf("链路标识重复: %q", id)
		}
		seen[id] = true
	}
}

func TestNewLoggerRejectsInvalidLevel(t *testing.T) {
	if _, err := NewLogger(Options{Level: "verbose"}); err == nil {
		t.Error("非法日志级别应报错")
	}
	if _, err := NewLogger(Options{Level: LevelInfo}); err != nil {
		t.Errorf("合法日志级别不应报错: %v", err)
	}
}
