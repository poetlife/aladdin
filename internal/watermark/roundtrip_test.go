package watermark

import (
	"errors"
	"image"
	"image/color"
	"testing"
	"time"
)

// 往返用例的共用底座。每个用例只动一个变量（底色、裁切偏移、倍率、重编码），
// 这样失败时能直接归因到哪一环，而不是"某张图解不出"。

// rendered 是"一张带水印的页面"：底色 + 平铺的图案，还没经过任何变换。
func rendered(t testingT, code string, bg backdrop, w, h int) image.Image {
	t.Helper()
	return composite(newPage(w, h, bg), decodeTile(t, code))
}

// 所有底色都要过一遍，且**每一种底色都要与每一种变换组合**。
//
// 只测纯白会掩盖两类问题：深色底上黑格几乎不可见（只剩白格那半个极性），
// 渐变底上高通那一步若写错，低频会被当成信号。
var backdrops = []struct {
	name string
	bg   backdrop
}{
	{"纯白", whiteBackdrop},
	{"深色", darkBackdrop},
	{"渐变加文字块", gradientBackdrop},
}

// transform 是对页面施加的一次变换。
type transform struct {
	name  string
	apply func(testingT, image.Image) image.Image
}

func transforms() []transform {
	return []transform{
		{"原尺寸", func(_ testingT, img image.Image) image.Image { return img }},
		{"裁切偏移(37,29)", func(_ testingT, img image.Image) image.Image {
			return crop(img, 37, 29, pageW-37-11, pageH-29-7)
		}},
		{"放大2倍", func(_ testingT, img image.Image) image.Image { return scaleNearest(img, 2) }},
		{"放大3倍", func(_ testingT, img image.Image) image.Image { return scaleNearest(img, 3) }},
		{"缩小0.5倍", func(_ testingT, img image.Image) image.Image { return scaleHalf(img) }},
		// 125% 与 150% 在现实中比 1.5 更常见（Windows 与 macOS 的缩放档位），
		// 它们是非整数倍率，因此单独列出。
		{"放大1.5倍", func(_ testingT, img image.Image) image.Image { return scaleNearest1(img, 1.5) }},
		{"放大1.25倍", func(_ testingT, img image.Image) image.Image { return scaleNearest1(img, 1.25) }},
		{"缩小0.75倍", func(_ testingT, img image.Image) image.Image { return boxScale(img, 0.75) }},
		// 1.75（Windows 175%）是浏览器真实会产生、且 4×1.75=7 为整数格的档位。
		// 最近邻与双线性都要测：前者保留像素真值只错位，后者把交界像素混成中间值，
		// 同一倍率下更难。
		{"放大1.75倍最近邻", func(_ testingT, img image.Image) image.Image { return scaleNearest1(img, 1.75) }},
		{"放大1.75倍双线性", func(_ testingT, img image.Image) image.Image { return bilinear(img, 1.75) }},
		{"JPEG质量85", func(t testingT, img image.Image) image.Image { return jpegRoundTrip(t, img, 85) }},
		{"放大2倍后JPEG", func(t testingT, img image.Image) image.Image {
			return jpegRoundTrip(t, scaleNearest(img, 2), 85)
		}},
	}
}

// TestRoundTrip 是验收的主体：每种底色 × 每种变换都必须解回原码。
func TestRoundTrip(t *testing.T) {
	codes := []string{
		"0123456789ABCDEF",
		"ZYXWVTSRQPNMKJH0",
	}
	for _, code := range codes {
		for _, bd := range backdrops {
			for _, tf := range transforms() {
				t.Run(code+"/"+bd.name+"/"+tf.name, func(t *testing.T) {
					page := rendered(t, code, bd.bg, pageW, pageH)
					shot := tf.apply(t, page)
					r, ok := analyze(shot)
					if !ok {
						t.Fatalf("解不出水印（%s/%s）", bd.name, tf.name)
					}
					if r.code != code {
						t.Fatalf("解出 %q，期望 %q", r.code, code)
					}
					t.Logf("相关=%.3f 显著位=%.2f", r.score, r.strength)
				})
			}
		}
	}
}

// TestScalesOutsideTableNotFound 把"候选倍率表就是能力边界"这件事钉住。
//
// 为什么这条负例值得写：倍率表看起来只是一串数字，很诱人随手加一档"顺便支持
// 110%"。但入表是有硬判据的——CellPx×scale 必须是整数（一格恰好整数个像素），
// 否则格子周期不再恒定，一帧之内边界漂移可达 25 像素，同一残余类的副本互相错位，
// 相关性掉到 0.68 左右（噪声约 0.25），过不了前导。
//
// 断言的是 ErrNotFound 而**不是"任意错误"**：解不出与"解出一个错码"必须分清楚。
// 后者说明定位找到了一个自洽的假帧，那是比解不出严重得多的问题。
func TestScalesOutsideTableNotFound(t *testing.T) {
	const code = "0123456789ABCDEF"
	pages := map[string]image.Image{
		"纯白": rendered(t, code, whiteBackdrop, pageW, pageH),
		"渐变": rendered(t, code, gradientBackdrop, pageW, pageH),
	}
	outside := []struct {
		name   string
		factor float64
		apply  func(image.Image, float64) image.Image
	}{
		{"1.1倍最近邻", 1.1, func(img image.Image, f float64) image.Image { return scaleNearest1(img, f) }},
		{"1.1倍双线性", 1.1, func(img image.Image, f float64) image.Image { return bilinear(img, f) }},
	}
	for _, tc := range outside {
		for pageName, page := range pages {
			t.Run(tc.name+"/"+pageName, func(t *testing.T) {
				got, err := Extract(tc.apply(page, tc.factor))
				if err == nil {
					t.Fatalf("表外倍率 %v 解出了码 %q：要么它其实能解（那就该入表），"+
						"要么提取在假帧上误报（更严重）", tc.factor, got)
				}
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("错误是 %v，期望 ErrNotFound", err)
				}
			})
		}
	}
}

// 无水印的图必须返回 ErrNotFound，且**不能返回一个假码**。
//
// 三种负例各自针对一层：纯白不给任何信号；渐变有水印的量级但没有它的周期性；
// 随机噪点每个格子的幅度与真水印同量级，只是符号随机——它是最难拒的。
func TestNoWatermarkNotFound(t *testing.T) {
	cases := []struct {
		name string
		img  image.Image
	}{
		{"纯白", newPage(pageW, pageH, whiteBackdrop)},
		{"深色", newPage(pageW, pageH, darkBackdrop)},
		{"渐变加文字块", newPage(pageW, pageH, gradientBackdrop)},
		{"随机噪点", newPage(pageW, pageH, noiseBackdrop(7))},
		{"满屏棋盘格", checkerPage(pageW, pageH, 4)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, err := Extract(tc.img)
			if err == nil {
				t.Fatalf("无水印图解出了假码 %q", code)
			}
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("错误是 %v，期望 ErrNotFound", err)
			}
		})
	}
}

// checkerPage 是"最像水印"的负例：4 像素棋盘格，频域上与水印重叠得最厉害。
func checkerPage(w, h, cell int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := uint8(255)
			if (x/cell+y/cell)%2 == 0 {
				v = 0
			}
			img.SetNRGBA(x, y, color.NRGBA{R: v, G: v, B: v, A: 255})
		}
	}
	return img
}

// 两个不同的码不能互相解错：这是"码真的进了像素"的最强证据。
//
// 与 TestRoundTrip 的区别是它同时生两张图、各解一次，并且断言**解出的必须是
// 自己那一张**。只断言"解出了某个合法码"会漏掉整帧错位的实现——那种实现两个码
// 都能解出东西，只是互相串了。
func TestTwoCodesDoNotCrossDecode(t *testing.T) {
	codeA, err := NewCode()
	if err != nil {
		t.Fatalf("生成码 A: %v", err)
	}
	codeB, err := NewCode()
	if err != nil {
		t.Fatalf("生成码 B: %v", err)
	}
	if codeA == codeB {
		t.Fatal("两个随机码相同，无法构成用例")
	}

	for _, bd := range backdrops {
		t.Run(bd.name, func(t *testing.T) {
			shotA := crop(rendered(t, codeA, bd.bg, pageW, pageH), 13, 7, pageW-40, pageH-30)
			shotB := crop(rendered(t, codeB, bd.bg, pageW, pageH), 13, 7, pageW-40, pageH-30)

			gotA, err := Extract(shotA)
			if err != nil {
				t.Fatalf("解 A: %v", err)
			}
			gotB, err := Extract(shotB)
			if err != nil {
				t.Fatalf("解 B: %v", err)
			}
			if gotA != codeA {
				t.Errorf("A 图解出 %q，期望 %q", gotA, codeA)
			}
			if gotB != codeB {
				t.Errorf("B 图解出 %q，期望 %q", gotB, codeB)
			}
		})
	}
}

// 裁切得只剩"一帧多一点"时仍要能解——这是"截图只截了一角"的现实情形。
func TestSmallCropStillDecodes(t *testing.T) {
	const code = "0123456789ABCDEF"
	page := rendered(t, code, gradientBackdrop, pageW, pageH)
	// 一帧 256×112，留 320×160：只比一帧大一圈，重复次数降到个位数。
	shot := crop(page, 211, 173, 320, 160)
	if got, err := Extract(shot); err != nil {
		t.Fatalf("小幅裁切后解不出: %v", err)
	} else if got != code {
		t.Fatalf("解出 %q，期望 %q", got, code)
	}
}

// 性能：1600×1000 的提取要在 2 秒内。软断言（长跑时跳过），但阈值取得保守到
// 不会因为机器慢而偶发失败——真实耗时通常比这低一个数量级。
func TestExtractPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过耗时用例")
	}
	const code = "0123456789ABCDEF"
	page := rendered(t, code, gradientBackdrop, pageW, pageH)

	// 先确认这确实是一张能解的图，否则下面的计时毫无意义。
	if _, ok := analyze(page); !ok {
		t.Fatal("基准用例本身解不出，计时无意义")
	}

	start := time.Now()
	if _, err := Extract(page); err != nil {
		t.Fatalf("解不出: %v", err)
	}
	elapsed := time.Since(start)
	t.Logf("1600×1000 提取耗时 %v", elapsed)
	// 阈值取 2s：需求给的上限。实际大约低一个数量级，留足机器差异的余量。
	if elapsed > 2*time.Second {
		t.Errorf("耗时 %v 超过 2s 上限", elapsed)
	}
}
