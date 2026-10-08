package imagetype_test

import (
	"errors"
	"testing"

	"github.com/poetlife/aladdin/internal/imagetype"
)

// 这一包是"展示小图"这一类图片的**唯一**白名单，头像与技能展示图共用。它要挡住的是
// 同一件事：让一张会被浏览器直接取走的图，落在"无法携带可执行内容"的那几个格式里。

func TestNormalizeAcceptsWhitelist(t *testing.T) {
	for _, declared := range []string{
		"image/png",
		"image/jpeg",
		"image/gif",
		"IMAGE/PNG",
		" image/png; charset=binary ",
	} {
		if _, err := imagetype.Normalize(declared); err != nil {
			t.Errorf("声明的类型 %q 被拒: %v", declared, err)
		}
	}
}

// **SVG 不在白名单里**，理由与头像那条相同：它是可内嵌脚本的 XML，而这类图会被下发
// 到浏览器。
func TestNormalizeRejects(t *testing.T) {
	for _, declared := range []string{
		"image/svg+xml",
		"text/html",
		"application/octet-stream",
		"image/webp",
		"",
	} {
		if _, err := imagetype.Normalize(declared); !errors.Is(err, imagetype.ErrTypeNotAllowed) {
			t.Errorf("声明的类型 %q err = %v，期望 ErrTypeNotAllowed", declared, err)
		}
	}
}

func TestFromExtension(t *testing.T) {
	accepts := map[string]string{
		"cover.png":      "image/png",
		"assets/a.jpg":   "image/jpeg",
		"assets/a.JPEG":  "image/jpeg",
		"deep/dir/a.GIF": "image/gif",
	}
	for name, want := range accepts {
		got, ok := imagetype.FromExtension(name)
		if !ok || got != want {
			t.Errorf("FromExtension(%q) = %q, %v，期望 %q", name, got, ok, want)
		}
	}
	for _, name := range []string{"cover.webp", "cover.svg", "cover", "cover.png.txt", "a/b"} {
		if _, ok := imagetype.FromExtension(name); ok {
			t.Errorf("FromExtension(%q) 认了它不是一张图", name)
		}
	}
}

// 魔数是**字节本身**说的话，供"服务端自己取回字节"那条路核对：扩展名是调用方给的
// 一个字符串，撒谎不花成本。
func TestMatchMagic(t *testing.T) {
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, make([]byte, 8)...)
	jpeg := append([]byte{0xff, 0xd8, 0xff, 0xe0}, make([]byte, 8)...)
	gif89 := []byte("GIF89a\001\000\001\000")
	gif87 := []byte("GIF87a\001\000\001\000")

	cases := map[string]string{"png": "image/png", "jpeg": "image/jpeg"}
	for name, want := range map[string][]byte{"png": png, "jpeg": jpeg} {
		got, ok := imagetype.MatchMagic(want)
		if !ok || got != cases[name] {
			t.Errorf("MatchMagic(%s) = %q, %v", name, got, ok)
		}
	}
	for name, data := range map[string][]byte{"gif89": gif89, "gif87": gif87} {
		if got, ok := imagetype.MatchMagic(data); !ok || got != "image/gif" {
			t.Errorf("MatchMagic(%s) = %q, %v", name, got, ok)
		}
	}

	// 看着像图、字节不是——这条最要紧：一个 .png 的路径配一份 HTML。
	for _, data := range [][]byte{[]byte("<!doctype html>"), []byte("GIF"), {}, []byte("<?xml version=\"1.0\"?>")} {
		if got, ok := imagetype.MatchMagic(data); ok {
			t.Errorf("MatchMagic(%q) = %q，期望不认", data, got)
		}
	}
}

// 规则由白名单派生：两处各写一份的表现是"服务端接受了、存储侧拒绝"。
func TestRulesAreDerivedFromWhitelist(t *testing.T) {
	rules := imagetype.Rules()
	allowed := imagetype.Allowed()
	if len(rules) != len(allowed) {
		t.Fatalf("规则 %d 条，期望与白名单一样多（%d）", len(rules), len(allowed))
	}
	seen := map[string]int64{}
	for _, rule := range rules {
		seen[rule.ContentType] = rule.MaxBytes
	}
	for _, contentType := range allowed {
		if seen[contentType] != imagetype.MaxBytes {
			t.Errorf("%s 的上限 = %d，期望 %d", contentType, seen[contentType], imagetype.MaxBytes)
		}
	}
}

// Allowed 返回副本：调用方拿到的是取值，不是一个能被就地改掉的全局变量。
func TestAllowedReturnsCopy(t *testing.T) {
	first := imagetype.Allowed()
	if len(first) == 0 {
		t.Fatal("白名单为空")
	}
	first[0] = "image/svg+xml"
	if second := imagetype.Allowed(); second[0] == "image/svg+xml" {
		t.Error("改一次返回值就改掉了白名单")
	}
}
