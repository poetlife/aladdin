package idgen_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/poetlife/aladdin/internal/idgen"
)

// 这一类标识既是主键、又是地址的一部分，因此"不可猜"是它的功能要求，不是风格
// 偏好（见 docs/design/galaxy/README.md 的"发布即公开"）。

func TestNewIsPrefixedAndUnguessable(t *testing.T) {
	got, err := idgen.New("prj_")
	if err != nil {
		t.Fatalf("分配标识: %v", err)
	}
	if !strings.HasPrefix(got, "prj_") {
		t.Errorf("标识 %q 没有前缀", got)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(got, "prj_"))
	if err != nil {
		t.Fatalf("随机部分不是 base64url: %v", err)
	}
	if len(raw) != idgen.EntropyBytes {
		t.Errorf("随机部分 %d 字节，期望 %d", len(raw), idgen.EntropyBytes)
	}
}

// 两次分配不相等，且标识只含 URL 非保留字符——它会出现在地址与命令行参数里。
func TestNewIsStableInShapeAndVaries(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		got, err := idgen.New("skl_")
		if err != nil {
			t.Fatalf("分配标识: %v", err)
		}
		if seen[got] {
			t.Fatalf("标识 %q 重复", got)
		}
		seen[got] = true
		for _, r := range strings.TrimPrefix(got, "skl_") {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			default:
				t.Fatalf("标识 %q 含非 URL 安全字符 %q", got, r)
			}
		}
	}
}

// 前缀由调用方给：空前缀合法（预览凭证就是一个没有可读前缀的标识）。
func TestNewAcceptsEmptyPrefix(t *testing.T) {
	got, err := idgen.New("")
	if err != nil {
		t.Fatalf("分配标识: %v", err)
	}
	if got == "" {
		t.Error("空前缀分配出了空标识")
	}
}
