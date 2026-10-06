package tagging_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/poetlife/aladdin/internal/tagging"
)

// 标签的归一化是**一处实现、两处消费**（galaxy 的资产与 skill 的技能）。本文件
// 测的是那条规则本身。
//
// 它必须逐字一致：两处归一化出不同的样子，表现是"这个标签看着对、就是筛不出来"
// （见 docs/design/skill/catalog.md）。

func TestNormalize(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"空集合", nil, nil},
		{"去首尾空白并统一小写", []string{"  Cover ", "HERO"}, []string{"cover", "hero"}},
		{"字典序", []string{"z", "a", "m"}, []string{"a", "m", "z"}},
		{"归一化之后去重", []string{"Cover", "cover", " COVER "}, []string{"cover"}},
		{"中文不受小写影响", []string{"出图"}, []string{"出图"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tagging.Normalize(tc.in)
			if err != nil {
				t.Fatalf("归一化 %v: %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("归一化 %v = %v，期望 %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeRejects(t *testing.T) {
	cases := []struct {
		name string
		in   []string
	}{
		{"空串", []string{""}},
		{"只有空白", []string{"   "}},
		{"含路径分隔符", []string{"a/b"}},
		{"含反斜杠", []string{`a\b`}},
		{"含控制字符", []string{"a\nb"}},
		{"超过单项长度上限", []string{strings.Repeat("字", tagging.MaxRunesPerTag+1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tagging.Normalize(tc.in); !errors.Is(err, tagging.ErrInvalid) {
				t.Errorf("归一化 %v err = %v，期望 ErrInvalid", tc.in, err)
			}
		})
	}
}

// 数量上限在**去重之后**算：归一化之后恰好 MaxTags 个通过，多一个被拒。
func TestNormalizeCountLimitIsAfterDedup(t *testing.T) {
	tags := make([]string, 0, tagging.MaxTags+1)
	for i := 0; i <= tagging.MaxTags; i++ {
		tags = append(tags, string(rune('a'+i)))
	}
	if _, err := tagging.Normalize(tags[:tagging.MaxTags]); err != nil {
		t.Errorf("恰好 %d 个标签被拒: %v", tagging.MaxTags, err)
	}
	if _, err := tagging.Normalize(tags); !errors.Is(err, tagging.ErrTooMany) {
		t.Errorf("超过 %d 个标签 err = %v，期望 ErrTooMany", tagging.MaxTags, err)
	}

	dup := append([]string{"a", "A", "  A "}, tags[:tagging.MaxTags-1]...)
	if _, err := tagging.Normalize(dup); err != nil {
		t.Errorf("去重之后未超限却被拒: %v", err)
	}
}

// 长度按**字符数**算而不是字节数：一个字重几个字节的中文标签不该在十几个字上被拒。
func TestNormalizeLengthCountsRunes(t *testing.T) {
	tag := strings.Repeat("中", tagging.MaxRunesPerTag)
	if len(tag) <= tagging.MaxRunesPerTag {
		t.Fatalf("用例前提不成立：%d 字节的标签不让按字符数算这件事看不出来", len(tag))
	}
	if _, err := tagging.Normalize([]string{tag}); err != nil {
		t.Errorf("%d 个字的标签被拒: %v", tagging.MaxRunesPerTag, err)
	}
}
