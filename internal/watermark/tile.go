package watermark

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
)

// tileAlpha 是格子的不透明度，取值区间由"两件事同时成立"卡出来：
//
//   - 小到不影响阅读：0.05 在纯白底上是 242/255 的灰，与正文的对比度远低于
//     正文自身与底色的对比度，正常距离看不出；深色底上同理。
//   - 大到经得起 JPEG 与缩略图：再往下（例如 0.02）时，量化表会把这些平台
//     直接抹平，0.5 倍缩小后的 4 像素格子只剩一两个像素的平均，信号就落到噪声
//     底下了。测试里对 JPEG 85 与 0.5 倍的用例是在这个值上通过的。
//
// 它不随底色变化：**变的是底色，不变的必须是差值方向**，所以 alpha 只能是一个
// 全局常数。想"按底色自适应"的做法会让提取端失去唯一的先验。
const tileAlpha = 0.05

// maskSalt 是打散掩码的种子。选 30 是因为在 mix32 下它给出的 4×4 掩码既有
// 8 个黑格、又满足"每行每列恰好 2 黑"——前者保证单块里黑白各半（反相时整体
// 亮度变化对称），后者保证块内不会出现一条竖线或横线那种比格子更显眼的结构。
const maskSalt uint32 = 30

// mix32 是一个确定性的 32 位整数混合函数（murmur3 的 fmix32）。
//
// 用它而不是查表：查表要维护 16 个常量，而这里只需要"同一坐标永远同一个值、
// 相邻坐标不相关"这一条性质；fmix32 的雪崩性正好给出后者，且含进位乘法让它
// 在肉眼尺度上足够不规则。函数形式写死在注释里是因为**换掉它就换掉了所有历史
// 图案的外观**——虽然提取不受影响（提取只读帧位，掩码在生成侧就已经消化进
// 像素里了），但用户会看到水印从一种纹理变成另一种。
func mix32(x uint32) uint32 {
	x += 0x9e3779b9
	x ^= x >> 16
	x *= 0x85ebca6b
	x ^= x >> 13
	x *= 0xc2b2ae35
	x ^= x >> 16
	return x
}

// cellInk 返回某一位的格子在块内坐标 (lx,ly) 处该画黑还是画白。
//
// 数据位只决定**整块是否反相**：bit 为 1 时翻转掩码。因此观察者看到的是同一片
// 纹理的两种极性，而不是"有的块是黑块、有的块是白块"——后者会在页面上一格一格
// 地闪，是最容易被一眼看出来的失败方式。
func cellInk(bit, lx, ly int) bool {
	// 块内坐标落在 [0, BlockCells×BlockCells) 上，转换不会溢出。
	mask := mix32(maskSalt+uint32(ly*BlockCells+lx)) & 1 //nolint:gosec // 取值域由块尺寸限定
	return (bit&1)^int(mask) == 1
}

// Tile 生成该水印码的平铺图案，返回 PNG 字节。
//
// 尺寸恰为 FrameWidthPx × FrameHeightPx、带 alpha 通道、同码同字节：浏览器把它
// 当 repeating background，因此**帧边缘的像素必须能与对面边缘咬合**——这由几何
// 保证（帧宽高都是块尺寸的整数倍，一格 4×4 像素也不会跨帧），所以这里不给边界
// 做任何特殊处理。加一条描边或留一圈空白都会在平铺网格上留下规则的接缝，那比
// 水印本身显眼得多。
//
// 颜色是纯黑/纯白加同一个 alpha，不按底色混合：图案本身不知道页面长什么样，
// 混合发生在浏览器合成的时候（这正是"前端渲染"的含义）。
func Tile(code string) ([]byte, error) {
	bits, err := frameBits(code)
	if err != nil {
		return nil, err
	}

	img := image.NewNRGBA(image.Rect(0, 0, FrameWidthPx, FrameHeightPx))
	for by := 0; by < FrameBlocksHigh; by++ {
		for bx := 0; bx < FrameBlocksWide; bx++ {
			bit := int(bits[by*FrameBlocksWide+bx])
			for cy := 0; cy < BlockCells; cy++ {
				for cx := 0; cx < BlockCells; cx++ {
					// 块内的一格对应 CellPx×CellPx 个像素：格的取值在块内
					// (cx,cy) 上，像素块的位置由格子坐标算出来。
					var c color.NRGBA
					if cellInk(bit, cx, cy) {
						c = color.NRGBA{R: 0, G: 0, B: 0, A: alphaByte()}
					} else {
						c = color.NRGBA{R: 255, G: 255, B: 255, A: alphaByte()}
					}
					for py := 0; py < CellPx; py++ {
						for px := 0; px < CellPx; px++ {
							x := (bx*BlockCells + cx) * CellPx
							y := (by*BlockCells + cy) * CellPx
							img.SetNRGBA(x+px, y+py, c)
						}
					}
				}
			}
		}
	}

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("编码水印图案失败: %w", err)
	}
	return buf.Bytes(), nil
}

// alphaByte 把 tileAlpha 换成 PNG 的 8 位 alpha。
//
// 四舍五入而不是截断：0.05×255 = 12.75，截断得到 12（0.047），四舍五入得到 13
// （0.051）。差这一档在 JPEG 里是"刚好被量化掉"与"刚好活下来"的分界，因此这里
// 明确取更接近目标的那一个。
func alphaByte() uint8 {
	return uint8(math.Round(tileAlpha * 255))
}
