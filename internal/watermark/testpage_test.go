package watermark

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
)

// 这个文件是测试用的"合成页面"：把 Tile 的输出像浏览器那样盖到页面内容上，
// 再施加各种变换。它是所有往返用例的共用底座。
//
// 为什么要自己合成而不是准备几张图：截图链路上的变量（底色、裁切偏移、倍率、
// 重编码）要能**分别**控制，否则一个用例失败时无法归因到哪一环。合成让每个
// 用例只动一个变量。
//
// 页面内容刻意不止纯白：纯白是水印最容易存活的底色（黑格把 255 压到 242，
// 差值方向单一），只测纯白会掩盖"深色底"与"渐变底"上的问题——后者才是真实
// 页面的常态。

// pageSize 是一张"截图"的尺寸。选 1600×1000 是因为它同时是性能用例的尺寸，
// 也足够容纳 6×8 个完整帧。
const (
	pageW = 1600
	pageH = 1000
)

// backdrop 是页面底色的生成函数。
type backdrop func(x, y int) color.NRGBA

// whiteBackdrop 是纯白页面。
func whiteBackdrop(x, y int) color.NRGBA {
	return color.NRGBA{R: 255, G: 255, B: 255, A: 255}
}

// darkBackdrop 是深色主题的页面。
//
// 深灰而不是纯黑：纯黑上"白格变亮"的绝对增量与"黑格"无法区分（黑格与底色同值），
// 于是只剩一半的信号。深灰更接近真实深色主题，也让两个极性都还留有余量。
func darkBackdrop(x, y int) color.NRGBA {
	return color.NRGBA{R: 28, G: 30, B: 34, A: 255}
}

// gradientBackdrop 是"渐变 + 文字状色块"，模拟真实页面最麻烦的那种内容：
// 低频的大面积亮度起伏，加上若干高频的深色条文。
//
// 渐变压测试的正是高通那一步——底色从 250 一路降到 120，跨度远大于水印的
// 十几个码值。文字状色块则制造"局部高频"，它和水印在频域上是有重叠的，因此
// 这个用例比纯渐变更能说明问题。
func gradientBackdrop(x, y int) color.NRGBA {
	// 对角渐变：横向 250→140，纵向再叠一点起伏。
	base := 250 - (x*110)/(pageW-1) - (y*40)/(pageH-1)
	// 周期性的"文字行"：每 24 行里有一行是深色块。
	if y%24 < 9 {
		// 断断续续的深色段，像一行字。
		if (x/7+y/24)%3 != 2 {
			base -= 120
		}
	}
	if base < 20 {
		base = 20
	}
	return color.NRGBA{R: uint8(base), G: uint8(base), B: uint8(base), A: 255}
}

// newPage 按底色生成一张不透明页面。
func newPage(w, h int, bg backdrop) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, bg(x, y))
		}
	}
	return img
}

// testTile 直接取 Tile 编码前的图案，避免 round-trip 矩阵反复跑昂贵的 PNG 压缩。
func testTile(t testingT, code string) image.Image {
	t.Helper()
	img, err := tileImage(code)
	if err != nil {
		t.Fatalf("生成图案 %q: %v", code, err)
	}
	return img
}

// testingT 是 *testing.T 与 *testing.B 共用的最小接口。
type testingT interface {
	Helper()
	Fatalf(format string, args ...any)
}

// blendOver 是一次 source-over 合成：把带 alpha 的图案像素压到底色上。
//
// 单独拎出来是为了让测试能把它当作"无缝的定义"来复用——测试要与生产代码走
// **同一条**混合公式，否则两边各自四舍五入一档就会让比对失败在无关的精度上。
func blendOver(src color.NRGBA, dst color.NRGBA) color.NRGBA {
	a := uint32(src.A)
	inv := 255 - a
	return color.NRGBA{
		R: uint8((uint32(src.R)*a + uint32(dst.R)*inv) / 255),
		G: uint8((uint32(src.G)*a + uint32(dst.G)*inv) / 255),
		B: uint8((uint32(src.B)*a + uint32(dst.B)*inv) / 255),
		A: 255,
	}
}

// composite 按浏览器合成背景图的方式把图案重复铺到页面上。
//
// 铺法就是 background-repeat：像素 (x,y) 取图案的 (x mod 幅宽, y mod 幅高)，
// 然后按 alpha 混合。**不做任何边界处理**——这与 Tile 的"帧尺寸是块尺寸整数倍"
// 合在一起，就是"平铺无缝"的全部内容：如果帧边缘有接缝，这里的平铺会把它重复
// 成千上万次，任何接缝都会在提取的残余类统计里变成规律性噪声，用例立刻失败。
func composite(page *image.NRGBA, tile image.Image) *image.NRGBA {
	b := page.Bounds()
	tb := tile.Bounds()
	out := image.NewNRGBA(b)
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			src := color.NRGBAModel.Convert(tile.At(tb.Min.X+x%tb.Dx(), tb.Min.Y+y%tb.Dy())).(color.NRGBA)
			out.SetNRGBA(x, y, blendOver(src, page.NRGBAAt(x, y)))
		}
	}
	return out
}

// crop 从 (ox,oy) 开始截一块，模拟"截图只截了页面的一部分"。
func crop(src image.Image, ox, oy, w, h int) *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out.Set(x, y, src.At(src.Bounds().Min.X+ox+x, src.Bounds().Min.Y+oy+y))
		}
	}
	return out
}

// scaleNearest 是最近的整数倍放大，模拟 image-rendering: pixelated 的 DPR 放大。
//
// 必须是最近邻而不是双线性：浏览器在 pixelated 下就是把每个像素复制成 n×n 的
// 方块，这正是"整数倍率下按格取均值"最干净的情形。
func scaleNearest(src image.Image, factor int) *image.NRGBA {
	b := src.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx()*factor, b.Dy()*factor))
	for y := 0; y < out.Bounds().Dy(); y++ {
		for x := 0; x < out.Bounds().Dx(); x++ {
			out.Set(x, y, src.At(b.Min.X+x/factor, b.Min.Y+y/factor))
		}
	}
	return out
}

// scaleHalf 是 2×2 盒式平均的等比缩小，模拟缩略图。
//
// 用盒式而不是双线性：真实的截图缩略图两者都有，而盒式是**更坏**的那一种
// （它把 4 个像素里水印的极性差直接平均掉一半），因此拿它当验收标准更保守。
func scaleHalf(src image.Image) *image.NRGBA {
	b := src.Bounds()
	w, h := b.Dx()/2, b.Dy()/2
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, bl, a uint32
			for dy := 0; dy < 2; dy++ {
				for dx := 0; dx < 2; dx++ {
					cr, cg, cb, ca := src.At(b.Min.X+x*2+dx, b.Min.Y+y*2+dy).RGBA()
					r += cr
					g += cg
					bl += cb
					a += ca
				}
			}
			out.Set(x, y, color.NRGBA{
				R: uint8(r / 4 >> 8), G: uint8(g / 4 >> 8),
				B: uint8(bl / 4 >> 8), A: uint8(a / 4 >> 8),
			})
		}
	}
	return out
}

// jpegRoundTrip 按给定质量重编码一遍再解回来，模拟聊天软件与工单系统对截图的
// 二次压缩。返回的通常是 *image.YCbCr，正好走到提取里的 Y 平面快路径。
func jpegRoundTrip(t testingT, src image.Image, quality int) image.Image {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("JPEG 编码: %v", err)
	}
	img, err := jpeg.Decode(&buf)
	if err != nil {
		t.Fatalf("JPEG 解码: %v", err)
	}
	return img
}

// noiseBackdrop 生成"随机噪点照片"：没有任何结构的高频内容。
//
// 它是负例里最难的一种——每个格子都在随机取值，块相关的幅度和真实水印同量级，
// 只是符号随机。能拒掉它才算"没有假码"，纯白与渐变都太容易了。
func noiseBackdrop(seed int64) backdrop {
	rng := rand.New(rand.NewSource(seed))
	return func(x, y int) color.NRGBA {
		v := uint8(rng.Intn(256))
		return color.NRGBA{R: v, G: v, B: v, A: 255}
	}
}

// colorNRGBA 是 color.NRGBA 的字面构造，省掉重复的类型名与 uint8 转换。
func colorNRGBA(r, g, b, a uint32) color.NRGBA {
	return color.NRGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: uint8(a)}
}
