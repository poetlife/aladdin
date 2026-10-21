package watermark

import (
	"image"
)

// 这里的三个缩放助手模拟"浏览器与操作系统的缩放"：非整数倍率，最近邻/盒式/双线性。
// 它们与 testpage_test 里的整数倍放大、2×2 盒式缩小互补——后者覆盖 DPR 与
// 缩略图的干净情形，这里覆盖 110%/125%/150%/175% 这类落在半像素上的情形。
//
// 放在单独的文件里而不是塞进 testpage_test：后者是"合成页面"的底座，这里只关心
// 重采样本身，两者变化的原因不同。

// scaleNearest1 是非整数倍率的最近邻放大。
//
// 非整数倍率下每个源像素被复制的次数不再相等（1.25 倍时是 1 或 2 次），因此
// 格子边界会落在像素中间——这正是真实缩放最讨厌的地方，也是这一档必须单独测的理由。
func scaleNearest1(src image.Image, factor float64) *image.NRGBA {
	b := src.Bounds()
	w := int(float64(b.Dx()) * factor)
	h := int(float64(b.Dy()) * factor)
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out.Set(x, y, src.At(b.Min.X+int(float64(x)/factor), b.Min.Y+int(float64(y)/factor)))
		}
	}
	return out
}

// boxScale 是任意倍率的盒式重采样。
func boxScale(src image.Image, factor float64) *image.NRGBA {
	b := src.Bounds()
	w := int(float64(b.Dx()) * factor)
	h := int(float64(b.Dy()) * factor)
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	step := 1.0 / factor
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			x0 := int(float64(x) * step)
			y0 := int(float64(y) * step)
			x1 := int(float64(x+1) * step)
			y1 := int(float64(y+1) * step)
			if x1 <= x0 {
				x1 = x0 + 1
			}
			if y1 <= y0 {
				y1 = y0 + 1
			}
			var r, g, bl, a, n uint32
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					cr, cg, cb, ca := src.At(b.Min.X+xx, b.Min.Y+yy).RGBA()
					r += cr
					g += cg
					bl += cb
					a += ca
					n++
				}
			}
			out.Set(x, y, colorNRGBA(r/n>>8, g/n>>8, bl/n>>8, a/n>>8))
		}
	}
	return out
}

// bilinear 是双线性重采样，浏览器与操作系统的默认缩放方式。
//
// 与最近邻的差别正是这里需要的对照：最近邻会原样复制像素（格边界虽有错位但
// 每个像素值仍是"真值"），双线性则把交界处的两个像素**混合**出一个中间值。
// 对水印这种靠一个格子里 4×4 个像素同色来累积信号的图案，混合等于把边缘那圈
// 像素的极性摊薄——所以同一个非整数倍率下，双线性通常比最近邻更难解。
// 两条都测，才知道"解不出"到底是倍率的锅还是重采样方式的锅。
func bilinear(src image.Image, factor float64) *image.NRGBA {
	b := src.Bounds()
	w := int(float64(b.Dx()) * factor)
	h := int(float64(b.Dy()) * factor)
	out := image.NewNRGBA(image.Rect(0, 0, w, h))

	// 源像素坐标的"反变换"：输出像素中心映射回源图，再取左右上下的邻居。
	// 减 0.5 加 0.5 是像素中心的约定——少了它，缩放会整体偏移半像素，
	// 那不是我们想测的东西。
	at := func(xx, yy int) (float64, float64, float64) {
		xx = clampInt(xx, b.Min.X, b.Max.X-1)
		yy = clampInt(yy, b.Min.Y, b.Max.Y-1)
		r, g, bl, _ := src.At(xx, yy).RGBA()
		return float64(r >> 8), float64(g >> 8), float64(bl >> 8)
	}
	for y := 0; y < h; y++ {
		fy := (float64(y)+0.5)/factor - 0.5
		y0 := int(mathFloor(fy))
		wy := fy - float64(y0)
		for x := 0; x < w; x++ {
			fx := (float64(x)+0.5)/factor - 0.5
			x0 := int(mathFloor(fx))
			wx := fx - float64(x0)

			r00, g00, b00 := at(b.Min.X+x0, b.Min.Y+y0)
			r10, g10, b10 := at(b.Min.X+x0+1, b.Min.Y+y0)
			r01, g01, b01 := at(b.Min.X+x0, b.Min.Y+y0+1)
			r11, g11, b11 := at(b.Min.X+x0+1, b.Min.Y+y0+1)

			mix := func(a00, a10, a01, a11 float64) uint32 {
				top := a00*(1-wx) + a10*wx
				bot := a01*(1-wx) + a11*wx
				return clamp8(int(top*(1-wy) + bot*wy + 0.5))
			}
			out.Set(x, y, colorNRGBA(
				mix(r00, r10, r01, r11),
				mix(g00, g10, g01, g11),
				mix(b00, b10, b01, b11),
				255))
		}
	}
	return out
}

// mathFloor 是 floor 的薄封装：结果只用来取整到邻居索引，负值方向也要往下取，
// 因此不能用 Go 的整数除法（它向零截断）。
func mathFloor(v float64) float64 {
	f := float64(int(v))
	if v < 0 && f != v {
		f--
	}
	return f
}

// clampInt 把索引夹回 [lo,hi]。
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// clamp8 把越界值夹回 0..255。
func clamp8(v int) uint32 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint32(v)
}
