package watermark

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"
)

// 码的生成与合法性。这一层是纯函数，失败原因只有"位序/码表写错"。
func TestNewCodeShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		code, err := NewCode()
		if err != nil {
			t.Fatalf("生成码: %v", err)
		}
		if len(code) != CodeLen {
			t.Fatalf("码 %q 长度 %d，期望 %d", code, len(code), CodeLen)
		}
		if !ValidCode(code) {
			t.Fatalf("生成的码 %q 自己判为不合法", code)
		}
		if seen[code] {
			t.Fatalf("码 %q 重复：随机源没有起作用", code)
		}
		seen[code] = true
		for _, r := range code {
			if strings.ContainsRune("ILOU", r) {
				t.Fatalf("码 %q 含易混字符 %q", code, r)
			}
		}
	}
}

// ValidCode 的接受与拒绝必须是同一张表：判据只有"长度 + 字符属于码表"。
func TestValidCode(t *testing.T) {
	cases := []struct {
		name string
		code string
		want bool
	}{
		{"生成出来的形状", "0123456789ABCDEF", true},
		{"码表两端", "0000000000000000", true},
		{"码表末位", "ZZZZZZZZZZZZZZZZ", true},
		{"长度少一位", "0123456789ABCDE", false},
		{"长度多一位", "0123456789ABCDEFG", false},
		{"含小写", "0123456789abcdef", false},
		{"含 I", "I123456789ABCDEF", false},
		{"含 L", "L123456789ABCDEF", false},
		{"含 O", "O123456789ABCDEF", false},
		{"含 U", "U123456789ABCDEF", false},
		{"空串", "", false},
		{"含标点", "0123456789ABCDE-", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidCode(tc.code); got != tc.want {
				t.Errorf("ValidCode(%q) = %v，期望 %v", tc.code, got, tc.want)
			}
		})
	}
}

// 码的位序必须是双射：16 个字符 ↔ 80 位，往返不丢信息。
//
// 这条一旦坏了，表现是"Tile 出的图与 Extract 解出的码不同"，而那很难归因。
func TestCodeBitsRoundTrip(t *testing.T) {
	for _, code := range []string{
		"0123456789ABCDEF",
		"ZZZZZZZZZZZZZZZZ",
		"A5C3A5C3A5C3A5C3",
	} {
		bits, err := codeToBits(code)
		if err != nil {
			t.Fatalf("展开 %q: %v", code, err)
		}
		if len(bits) != codeBits {
			t.Fatalf("%q 展开成 %d 位，期望 %d", code, len(bits), codeBits)
		}
		if got := bitsToCode(bits); got != code {
			t.Errorf("往返 %q 得到 %q", code, got)
		}
	}
}

// 帧布局：前导占 0..15、码占 16..95、CRC 占 96..111，且 CRC 只覆盖码那 80 位。
func TestFrameLayout(t *testing.T) {
	code := "0123456789ABCDEF"
	frame, err := frameBits(code)
	if err != nil {
		t.Fatalf("组帧: %v", err)
	}
	if len(frame) != FrameBlocksWide*FrameBlocksHigh {
		t.Fatalf("帧长 %d，期望 %d", len(frame), FrameBlocksWide*FrameBlocksHigh)
	}
	pre, gotCode, gotCRC := splitFrame(frame)
	if pre != preamble {
		t.Errorf("前导 %#04x，期望 %#04x", pre, preamble)
	}
	if gotCode != code {
		t.Errorf("帧里的码是 %q，期望 %q", gotCode, code)
	}
	codeBitsOut, _ := codeToBits(code)
	if gotCRC != crcOfBits(codeBitsOut) {
		t.Errorf("CRC 不自洽: %#04x", gotCRC)
	}
	// 改一位码，CRC 必须随之改变——否则 CRC 根本没覆盖到那一位。
	changed, _ := codeToBits("1123456789ABCDEF")
	if crcOfBits(changed) == gotCRC {
		t.Error("改一位码后 CRC 未变：CRC 没有覆盖全部的码位")
	}
}

// Tile 的三个硬性要求：尺寸恰为常量、带 alpha、同码同字节。
func TestTileDeterministicAndSized(t *testing.T) {
	code := "0123456789ABCDEF"
	raw1, err := Tile(code)
	if err != nil {
		t.Fatalf("生成图案: %v", err)
	}
	raw2, err := Tile(code)
	if err != nil {
		t.Fatalf("再生成一次: %v", err)
	}
	if !bytes.Equal(raw1, raw2) {
		t.Error("同一个码两次生成得到不同字节：图案不是确定性的")
	}

	img, err := png.Decode(bytes.NewReader(raw1))
	if err != nil {
		t.Fatalf("解码图案: %v", err)
	}
	if got := img.Bounds().Dx(); got != FrameWidthPx {
		t.Errorf("宽 %d，期望 %d", got, FrameWidthPx)
	}
	if got := img.Bounds().Dy(); got != FrameHeightPx {
		t.Errorf("高 %d，期望 %d", got, FrameHeightPx)
	}

	// 带 alpha 通道：每个像素的 alpha 必须是那个小值，而不是 255。
	_, _, _, a := img.At(0, 0).RGBA()
	want := uint32(alphaByte()) * 0x101
	if a != want {
		t.Errorf("alpha %d，期望 %d（tileAlpha=%v）", a, want, tileAlpha)
	}

	// 不同码必须给出不同图案，否则编码没有真的进像素。
	other, err := Tile("ZZZZZZZZZZZZZZZZ")
	if err != nil {
		t.Fatalf("生成另一张图案: %v", err)
	}
	if bytes.Equal(raw1, other) {
		t.Error("两个不同的码得到同一张图案")
	}
}

// 平铺无缝：按帧取模铺出来的图，在接缝处必须与"帧内部"完全同构。
//
// 这条不靠肉眼看图：肉眼根本看不出 0.05 alpha 的接缝，但提取的定位会被它带偏。
// 做法是比两张图——一张按帧取模铺 2×2，一张是同一个图案"本来就是 2×2 帧"的
// 地面真值。两者若逐像素相同，说明平铺在边界上没有任何额外处理。
func TestTileSeamless(t *testing.T) {
	const w, h = FrameWidthPx * 2, FrameHeightPx * 2
	tile := decodeTile(t, "A5C3A5C3A5C3A5C3")
	tb := tile.Bounds()

	// ground truth：把单帧的像素按 (x mod 帧宽, y mod 帧高) 取出来，再按 alpha
	// 合成到白底上——这就是"无缝"的定义本身。
	want := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			src := tile.At(tb.Min.X+x%tb.Dx(), tb.Min.Y+y%tb.Dy())
			want.Set(x, y, src)
		}
	}

	got := composite(newPage(w, h, whiteBackdrop), tile)

	// composite 已经按 alpha 混到白底上了，因此把 want 也混一遍再比。
	blended := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			blended.Set(x, y, blendOver(want.NRGBAAt(x, y), whiteBackdrop(x, y)))
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if g, e := got.NRGBAAt(x, y), blended.NRGBAAt(x, y); g != e {
				t.Fatalf("(%d,%d) 处平铺结果 %v，期望 %v：帧边界有额外处理", x, y, g, e)
			}
		}
	}
}
