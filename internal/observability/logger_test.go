package observability

import "testing"

func TestNewLoggerRejectsInvalidLevel(t *testing.T) {
	if _, err := NewLogger(Options{Level: "verbose"}); err == nil {
		t.Error("非法日志级别应报错")
	}
	if _, err := NewLogger(Options{Level: LevelInfo}); err != nil {
		t.Errorf("合法日志级别不应报错: %v", err)
	}
}
