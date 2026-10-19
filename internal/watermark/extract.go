package watermark

import (
	"image"
	"math"
	"sort"
)

// 提取是一条"粗筛 → 精验"的流水线：先按候选倍率 × 格子相位把图重采样成每格一个
// 均值，用前导位做相关找出最像帧的几个假设，再逐个验前导、验统计门、验 CRC。
//
// 为什么必须搜完这几层：截图里没有任何一项是已知的。页面按帧平铺，截图相对帧
// 原点的偏移未知；DPR 与缩略图让像素与格子的对应关系未知；格子如何组成块也随前
// 两者一起漂移。这是一个"未知缩放 + 未知平移"的配准问题，而水印本身**没有内容
// 特征**可用——它就是一层极淡的噪声，特征点与频域峰值都抓不住它。
//
// 为什么用前导而不是 CRC 当搜索目标：CRC 只该用来**拒**。拿它当搜索目标等于在
// 2^80 的空间里找一棵树；前导给出的 16 位是一个又便宜又够用的筛子，且它天然
// 对"相位错位"敏感（见 preamble 的注释）。
//
// 搜索量由一条恒等式兜住：某倍率下候选相位数是 cellPx²，每个相位只需看
// W·H/cellPx² 个格子，相乘恰好是 W·H。因此五个倍率的总代价是 5·W·H 量级，
// 而不是"倍率越多越慢"。这条恒等式成立的前提是**按格聚合而不是逐像素扫描**，
// 所以下面每一步都先落到"格"这一层。

// candidateScales 是候选倍率表。
//
// 覆盖的是**浏览器与操作系统真实会产生的缩放档位**，而不是一组数学上好看的数：
// 0.5 是缩略图；0.75/1.25/1.5/1.75 是 Windows 与 macOS 的 125%/150%/175% 档；
// 1/2/3 是 DPR 1/2/3；2.5 是 250% 档。
//
// **入表有一条硬判据：CellPx×scale 必须是整数**（4s ∈ ℤ），也就是一格恰好占整数
// 个像素。这不是审美偏好，而是这套按格取均值的提取方式的适用条件：它假定整张图
// 的格子网格周期恒定。2.5 档之所以成立（4×2.5=10）而 1.1 档不成立（4×1.1=4.4），
// 差的就是这一条。
//
// 1.1（Chrome 110% 页面缩放）曾经在表里，实测**全线解不出**，12 个子用例（两种
// 重采样 × 三种底色 × 两个码）无一通过，因此被移除。原因是几何性的、不是参数没
// 调好：格子周期 4.4 像素意味着每格边界在 0、0.4、0.8、0.2 之间轮转，跑满 16 格
// 就漂移 6.4 像素、跑满一帧 64 格漂移 25.6 像素。于是"块内第几格"与"它在像素上
// 的位置"不再固定，同一残余类里的副本互相错位，相关值最多到 0.68（噪声约 0.25），
// 永远过不了前导这关。要救它得让提取端对每个块重新配准，那是另一个量级的改造。
//
// 这条边界现在由 TestScalesOutsideTableNotFound 用负例钉住：档位表就是能力边界，
// 将来有人随手加一档，会立刻看到它到底能不能解。
var candidateScales = []float64{0.5, 0.75, 1, 1.25, 1.5, 1.75, 2, 2.5, 3}

// 亮度权重，ITU-R BT.601 的整数形式。
//
// 只取亮度而不是三通道各自相关：水印是黑白两色叠在任意底色上，对三个通道的影响
// 同向等量；而 JPEG 与缩放对色度的破坏远大于亮度。只看亮度等于白拿了一次信噪比
// 提升，还把每像素的取数从三次降到一次。
const (
	lumaR   = 299
	lumaG   = 587
	lumaB   = 114
	lumaDiv = 1000
)

// maxAnalysisPixels 是分析区域的上限。
//
// 代价与像素数成正比，而"更多重复"对信噪比的边际收益是开方的：一张 1600×1000 的
// 截图里每位已经有几十份副本，4K 截图多出来的那几倍重复换不来多少可靠性，却会让
// 耗时线性增长。超出上限时取中心区域——水印是平铺的，中心与边缘同样有信号。
//
// 上限取得比 1600×1000 大：小于这个尺寸的截图**一个像素都不裁**。裁掉边缘看起来
// 无害，实际会吃掉最宝贵的那点余量——"只截到一帧多一点"的图，能用来对齐的块本来
// 就只剩寥寥几行，再削一圈就可能连前导都凑不齐。
const maxAnalysisPixels = 4_000_000

// strongBitZ 与 strongBitRatio 是"这个帧是真的"的统计门。
//
// CRC 只给 1/65536 的拒绝强度，前面还有"前导必须逐位对上"另一道 1/65536，两者
// 独立，合起来已经把误报压到一次搜索结果里不会出现的量级。这道门加的是第三个、
// 与前两者**正交**的判据：真帧的每一位都是几十份副本的平均，均值的标准误远小于
// 均值；噪声里某一位偶然显著是常事，但让绝大多数位同时显著几乎不可能。
//
// 它同时是"信号强度"的指示器：调试时看的就是这个比例，比 CRC 的 0/1 结果有用得多。
const (
	strongBitZ     = 2.0
	strongBitRatio = 0.6
)

// result 是提取的内部结果。
//
// 带分数只是为了测试能报出"余量有多大"。对外只暴露 Extract 的两个出口，是因为
// 调用方没有理由根据相关系数做判断——那会诱使上层写出"分数低于 0.3 就重试"这类
// 规则，而那些规则没有稳定语义。
type result struct {
	code     string
	score    float64 // 命中的前导相关值，0..1
	strength float64 // 显著位占比，0..1
}

// Extract 从一张截图里解出码。解不出时返回 ErrNotFound。
//
// 「不是本平台的图」「裁得太小」「变换太强」不区分：调用方要做的动作完全相同。
// 更细的错误只会诱使上层对某一种情况做特殊处理，而那些情况本身没有稳定边界。
func Extract(img image.Image) (string, error) {
	if r, ok := analyze(img); ok {
		return r.code, nil
	}
	return "", ErrNotFound
}

// analyze 跑完整条流水线，返回结果与是否命中。
func analyze(img image.Image) (result, bool) {
	if img == nil {
		return result{}, false
	}
	plane, w, h := lumaPlane(img)
	if w < FrameWidthPx/2 || h < FrameHeightPx/2 {
		// 连 0.5 倍的一帧都放不下，最低倍率的候选也不必试。
		return result{}, false
	}
	plane, w, h = cropPlane(plane, w, h, maxAnalysisPixels)

	a := &analyzer{integral: buildIntegral(plane, w, h), w: w, h: h}
	a.allocate()

	// 每个倍率各留几条，再合并排序后逐个验。**不能只在全局留前 N 条**：某个
	// 不匹配的倍率会在十几个相位上给出"看起来也还行"的分数，把真正匹配的那一档
	// 挤出去。按倍率分组，等于给每一档一张独立的门票，代价只是多几次便宜的验证。
	var all []candidate
	for si, scale := range candidateScales {
		cellPx := int(math.Round(CellPx * scale))
		if cellPx < 1 {
			continue
		}
		best := newCandidateList(topPerScale)
		a.scanScale(si, cellPx, best)
		all = append(all, best.ranked...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].score > all[j].score })

	for _, c := range all {
		if r, ok := a.decode(c); ok {
			return r, true
		}
	}
	return result{}, false
}

// candidate 是一次"看起来像帧"的假设：倍率、格子相位、块相位、帧相位与极性。
type candidate struct {
	scaleIdx int
	cellPx   int
	ox, oy   int
	qy, qx   int
	by, bx   int
	polarity float64
	score    float64
	// coverage 是块相位 (qx,qy) 能凑出的完整残余类占比，用来在分数**打平**时
	// 决胜。为什么需要：一张刚好一帧大小的截图里，只有恰好对齐的那个相位能凑满
	// 16×7 个残差类，其余相位会缺一行或一列，因而根本解不出——但它们的相关值
	// 可以一模一样高（前导只覆盖一行，横向对齐不受纵向缺行影响）。没有这条
	// 决胜规则时，谁能进候选表纯粹取决于遍历顺序。
	coverage float64
}

// topPerScale 是每个倍率下保留的假设个数。
//
// 比"需要的 1 个"多，是因为同一份真信号在相邻相位上得分也不低（格子相位差一像素
// 时边界像素被划到邻格，前导相关仍然部分成立）。多留几个让后面的前导/CRC 去挑，
// 比在这里把排序做精确便宜得多。
const topPerScale = 4

// candidateList 是一个最多 topCandidates 项、按分数降序的小表。
//
// 只有 16 项，线性插入就够，不值得为它引入堆或者 sort 的反射开销——offer 在相位
// 循环的最内层被调用几十万次。
type candidateList struct {
	max    int
	ranked []candidate
}

func newCandidateList(max int) *candidateList {
	return &candidateList{max: max, ranked: make([]candidate, 0, max)}
}

// betterThan 是候选之间的全序：分数优先，分数近似相等时看覆盖度。
//
// 「近似相等」用 1e-3 的窗口而不是精确相等：相关值是浮点计算结果，同一份信号在
// 不同相位上的差异可以小到 1e-9，用 == 判定会让决胜规则形同虚设。
func (c candidate) betterThan(o candidate) bool {
	if math.Abs(c.score-o.score) > 1e-3 {
		return c.score > o.score
	}
	return c.coverage > o.coverage
}

func (l *candidateList) offer(c candidate) {
	if len(l.ranked) < l.max {
		l.ranked = append(l.ranked, c)
		for i := len(l.ranked) - 1; i > 0 && l.ranked[i].betterThan(l.ranked[i-1]); i-- {
			l.ranked[i], l.ranked[i-1] = l.ranked[i-1], l.ranked[i]
		}
		return
	}
	last := l.max - 1
	if !c.betterThan(l.ranked[last]) {
		return
	}
	l.ranked[last] = c
	for i := last; i > 0 && l.ranked[i].betterThan(l.ranked[i-1]); i-- {
		l.ranked[i], l.ranked[i-1] = l.ranked[i-1], l.ranked[i]
	}
}

// lumaPlane 把图摊成一张亮度平面。
//
// 为最常见的几类图走快路径：JPEG 解出来是 *image.YCbCr，它的 Y 平面**就是**亮度，
// 直接取比逐像素调 At() 快一个数量级；截图链路上最多的恰好是它和 NRGBA/Gray。
// 其余类型统一走 At()，正确性一致。
func lumaPlane(src image.Image) ([]float32, int, int) {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	plane := make([]float32, w*h)

	switch m := src.(type) {
	case *image.Gray:
		for y := 0; y < h; y++ {
			row := m.Pix[(y+b.Min.Y-m.Rect.Min.Y)*m.Stride+(b.Min.X-m.Rect.Min.X):]
			for x := 0; x < w; x++ {
				plane[y*w+x] = float32(row[x])
			}
		}
		return plane, w, h
	case *image.YCbCr:
		for y := 0; y < h; y++ {
			row := m.Y[(y+b.Min.Y-m.Rect.Min.Y)*m.YStride+(b.Min.X-m.Rect.Min.X):]
			for x := 0; x < w; x++ {
				plane[y*w+x] = float32(row[x])
			}
		}
		return plane, w, h
	case *image.NRGBA:
		for y := 0; y < h; y++ {
			row := m.Pix[(y+b.Min.Y-m.Rect.Min.Y)*m.Stride+(b.Min.X-m.Rect.Min.X)*4:]
			for x := 0; x < w; x++ {
				plane[y*w+x] = float32((lumaR*int(row[x*4]) + lumaG*int(row[x*4+1]) + lumaB*int(row[x*4+2])) / lumaDiv)
			}
		}
		return plane, w, h
	}

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			plane[y*w+x] = float32((lumaR*int(r>>8) + lumaG*int(g>>8) + lumaB*int(bl>>8)) / lumaDiv)
		}
	}
	return plane, w, h
}

// cropPlane 在像素过多时取中心区域，返回新的平面与尺寸。
func cropPlane(plane []float32, w, h, maxPixels int) ([]float32, int, int) {
	if w*h <= maxPixels {
		return plane, w, h
	}
	scale := math.Sqrt(float64(maxPixels) / float64(w*h))
	nw := int(float64(w) * scale)
	nh := int(float64(h) * scale)
	if nw < 1 || nh < 1 {
		return plane, w, h
	}
	x0, y0 := (w-nw)/2, (h-nh)/2
	out := make([]float32, nw*nh)
	for y := 0; y < nh; y++ {
		copy(out[y*nw:(y+1)*nw], plane[(y0+y)*w+x0:(y0+y)*w+x0+nw])
	}
	return out, nw, nh
}

// buildIntegral 建求和面积表（积分图），之后任一矩形和是四次查表。
//
// 用 int64 而不是 float：亮度是 0..255 的整数，整张图的和也装得下，而且**没有
// 舍入误差**。这里省下的那点舍入恰好在减法里最要命——矩形和常常是两个大数相减
// 得到几十，浮点误差会把水印那十几个码值的对比度淹掉。
func buildIntegral(plane []float32, w, h int) []int64 {
	stride := w + 1
	ii := make([]int64, stride*(h+1))
	for y := 0; y < h; y++ {
		var rowSum int64
		for x := 0; x < w; x++ {
			rowSum += int64(plane[y*w+x])
			ii[(y+1)*stride+x+1] = ii[y*stride+x+1] + rowSum
		}
	}
	return ii
}

// analyzer 持有一次提取的全部缓冲区。
//
// 缓冲区在相位循环外分配一次、循环里反复用：264 个相位 × 每次上兆的临时分配会让
// GC 成为主要耗时，而在 2 秒的预算里，GC 是最不该出现的变量。
type analyzer struct {
	integral []int64
	w, h     int
	means    []float32
	hp       []float32
	prefix   []float64
	agg      []slot
}

// slot 是"同一残余类"的块相关值统计。
//
// 同时留和与平方和，是为了在解码时算出标准误——它支撑"这个帧是真的"那道统计门，
// 也是把"某一位确实偏这边"与"这一位噪声很大"区分开的唯一办法。
type slot struct {
	n     int
	sum   float64
	sumsq float64
}

func (s *slot) mean() float64 {
	if s.n == 0 {
		return 0
	}
	return s.sum / float64(s.n)
}

func (a *analyzer) allocate() {
	// 格子最小是 0.5 倍下的 2 像素，因此格子数上限是 (w/2)×(h/2)。
	maxCells := (a.w/2 + 2) * (a.h/2 + 2)
	a.means = make([]float32, maxCells)
	a.hp = make([]float32, maxCells)
	a.prefix = make([]float64, maxCells)
	a.agg = make([]slot, BlockCells*BlockCells*FrameBlocksHigh*FrameBlocksWide)
}

// scanScale 枚举一个倍率下的全部格子相位（cellPx² 个）。
func (a *analyzer) scanScale(scaleIdx, cellPx int, best *candidateList) {
	for oy := 0; oy < cellPx; oy++ {
		for ox := 0; ox < cellPx; ox++ {
			cols, rows := a.cellMeans(cellPx, ox, oy)
			if cols == 0 {
				continue
			}
			a.highPass(cols, rows)
			a.aggregate(cols, rows)
			a.scorePhases(scaleIdx, cellPx, ox, oy, cols, rows, best)
		}
	}
}

// cellMeans 按候选网格把亮度图聚合成"每格一个均值"，返回列数与行数。
//
// 网格起点是 (ox,oy)、格边长 cellPx 像素。每格是一次积分图查询，因此一个相位的
// 代价是"格子数"而不是"像素数"——这正是前面那条恒等式成立的地方。
//
// 不足一帧时返回 0：调用方据此跳过该相位，避免在下面所有循环里重复判边界。
func (a *analyzer) cellMeans(cellPx, ox, oy int) (cols, rows int) {
	cols = (a.w - ox) / cellPx
	rows = (a.h - oy) / cellPx
	if cols < cellsWide || rows < cellsHigh {
		return 0, 0
	}
	stride := a.w + 1
	area := float32(cellPx * cellPx)
	for i := 0; i < rows; i++ {
		y0 := oy + i*cellPx
		y1 := y0 + cellPx
		top := y1 * stride
		bot := y0 * stride
		base := i * cols
		for j := 0; j < cols; j++ {
			x0 := ox + j*cellPx
			x1 := x0 + cellPx
			sum := a.integral[top+x1] - a.integral[bot+x1] -
				a.integral[top+x0] + a.integral[bot+x0]
			a.means[base+j] = float32(sum) / area
		}
	}
	return cols, rows
}

// highPass 减去每格周围 5×5 格的均值。
//
// 为什么必须先做这一步：页面内容是任意起伏的（渐变、大块色块、照片），局部底色的
// 亮度可能比水印强两个数量级。水印在格尺度上是高频，内容在格尺度上是低频——减去
// 邻域均值把低频项拿掉，剩下的才可比。
//
// 窗口取 5 格而不是 3 格：块是 4 格宽，3 格窗口会整窗落在一个块内，等于把块的
// 极性也一起减掉；5 格窗口必然跨到邻块，而邻块极性与本块互相独立，均值不会把
// 本块的图案抵消掉。
//
// 边界用截断窗口（不补零）：补零会在图的四边造出一条假的强弱带，它在残余类统计
// 里会表现为"某些位永远显著"，对统计门是实打实的污染。
func (a *analyzer) highPass(cols, rows int) {
	stride := cols + 1
	pre := a.prefix
	// 前缀和的第一行与每行的第一列必须是 0（"空矩形"的和），但这两个位置由
	// 下面的循环**写不到**——它只写 pre[(i+1)*stride+j+1]。缓冲区是跨相位复用的，
	// 上一轮的 stride 又与这一轮不同，因此不显式清零就会读到上一轮的残留。
	// 这曾是一个真实的缺陷：图稍大一点（前缀和跨过一轮不同宽度的相位）就解不出，
	// 而小图恰好因为残留落在零区而通过。
	for j := 0; j <= cols; j++ {
		pre[j] = 0
	}
	for i := 1; i <= rows; i++ {
		pre[i*stride] = 0
	}
	for i := 0; i < rows; i++ {
		var rowSum float64
		base := i * cols
		for j := 0; j < cols; j++ {
			rowSum += float64(a.means[base+j])
			pre[(i+1)*stride+j+1] = pre[i*stride+j+1] + rowSum
		}
	}
	for i := 0; i < rows; i++ {
		y0 := max(0, i-2)
		y1 := min(rows, i+3)
		base := i * cols
		for j := 0; j < cols; j++ {
			x0 := max(0, j-2)
			x1 := min(cols, j+3)
			sum := pre[y1*stride+x1] - pre[y0*stride+x1] -
				pre[y1*stride+x0] + pre[y0*stride+x0]
			n := float64((y1 - y0) * (x1 - x0))
			a.hp[base+j] = a.means[base+j] - float32(sum/n)
		}
	}
}

// maskSign 是块内每格的期望亮度符号（相对底色）。
//
// +1 表示"这一格应当偏亮"，-1 表示"应当偏暗"。它只依赖块内坐标，因此所有块共用
// 同一张 4×4 表；数据位只决定整块是否取反。存成符号而不是 0/1，是为了让下面的
// 点积直接读出"这一位与掩码对齐得有多好"，省掉一次逐格判断。
var maskSign [BlockCells][BlockCells]float32

func init() {
	for ly := 0; ly < BlockCells; ly++ {
		for lx := 0; lx < BlockCells; lx++ {
			if mix32(maskSalt+uint32(ly*BlockCells+lx))&1 == 1 {
				// 掩码为 1 的格画黑，相对底色偏暗。
				maskSign[ly][lx] = -1
			} else {
				maskSign[ly][lx] = 1
			}
		}
	}
}

// aggregate 为每个块算一个相关值，并按"块行 mod 7、块列 mod 16"归进残余类。
//
// 为什么按残余类归而不是按帧归：帧在图上重复了不知多少次、起点也不知道在哪，但
// **同一残余类一定是同一位**。于是"先把同一位的所有副本平均，再判这一位"就顺理
// 成章，而这一步同时拿到了重复带来的信噪比——几十份平均之后，JPEG 的量化噪声
// 基本不剩。
//
// 归类的另一半好处是它把"帧在哪儿"从搜索里拿掉了：块列每 16 个循环一次，所以帧的
// 横向相位只剩 16 种循环位移；纵向同理只剩 7 种。
func (a *analyzer) aggregate(cols, rows int) {
	a.resetAgg()
	for i := 0; i+BlockCells <= rows; i++ {
		qy := i & (BlockCells - 1)
		rowRes := ((i - qy) / BlockCells) % FrameBlocksHigh
		base := i * cols
		for j := 0; j+BlockCells <= cols; j++ {
			m := blockCorr(a.hp, base+j, cols)
			qx := j & (BlockCells - 1)
			colRes := ((j - qx) / BlockCells) % FrameBlocksWide
			idx := ((qy*BlockCells+qx)*FrameBlocksHigh+rowRes)*FrameBlocksWide + colRes
			s := &a.agg[idx]
			s.n++
			s.sum += float64(m)
			s.sumsq += float64(m) * float64(m)
		}
	}
}

// resetAgg 清空残余类统计。
//
// 用 clear 而不是逐项赋零：这是每个相位都要走的一遍 7168 项清零，编译器会把
// clear 优化成一次 memset，而逐项赋值在数组上容易留下循环不消的版本。
func (a *analyzer) resetAgg() {
	clear(a.agg)
}

// blockCorr 是一个块与掩码的点积：正数表示"这块是 bit 0 的极性"，负数表示 bit 1。
//
// 点积而不是"逐格比对有多少格同号"：点积在噪声下是线性叠加，几十份平均之后信号
// 与噪声按同一个根号涨，但噪声相互抵消，因此信噪比按 sqrt(n) 涨——这正是我们要的。
// 逐格投票会先丢掉幅度信息，弱信号在投票里就已经被噪声淹掉了。
func blockCorr(hp []float32, base, cols int) float32 {
	return hp[base]*maskSign[0][0] + hp[base+1]*maskSign[0][1] +
		hp[base+2]*maskSign[0][2] + hp[base+3]*maskSign[0][3] +
		hp[base+cols]*maskSign[1][0] + hp[base+cols+1]*maskSign[1][1] +
		hp[base+cols+2]*maskSign[1][2] + hp[base+cols+3]*maskSign[1][3] +
		hp[base+2*cols]*maskSign[2][0] + hp[base+2*cols+1]*maskSign[2][1] +
		hp[base+2*cols+2]*maskSign[2][2] + hp[base+2*cols+3]*maskSign[2][3] +
		hp[base+3*cols]*maskSign[3][0] + hp[base+3*cols+1]*maskSign[3][1] +
		hp[base+3*cols+2]*maskSign[3][2] + hp[base+3*cols+3]*maskSign[3][3]
}

// preambleE 是前导位对应的期望符号：bit 0 → +1，bit 1 → -1。
//
// 与 blockCorr 的符号约定对齐：m 为正代表 bit 0。两者若不同向，相关值会整体变负，
// 虽然极性搜索能纠正，但"最高分"会失去直观含义。
var preambleE = func() (e [FrameBlocksWide]float64) {
	for i := 0; i < FrameBlocksWide; i++ {
		if preamble>>(15-uint(i))&1 == 1 {
			e[i] = -1
		} else {
			e[i] = 1
		}
	}
	return e
}()

// scorePhases 对每个块相位、每个纵向帧相位，求横向相位与极性的最佳组合。
//
// 横向只搜 16 种**循环位移**，而不是 16 个独立未知量：前导横跨整整一个块行
// （16 位 = 一帧的全部列），因此任意 16 个连续块列读出来都是前导的一次循环位移。
// 这条性质来自 FrameBlocksWide 恰好等于前导宽度与码宽——它不是巧合，而是选
// 16×7 这个排布的理由之一。
//
// 归一化用 L2 范数而不是均值：内容的局部亮度起伏会让某些相位的均值整体偏大，
// 用均值归一化等于偏爱"更亮"的相位。余弦相似度对整体增益不敏感，正好只量形状。
func (a *analyzer) scorePhases(scaleIdx, cellPx, ox, oy, cols, rows int, best *candidateList) {
	var mcol [FrameBlocksWide]float64
	for qy := 0; qy < BlockCells; qy++ {
		for qx := 0; qx < BlockCells; qx++ {
			base := (qy*BlockCells + qx) * FrameBlocksHigh * FrameBlocksWide
			for by := 0; by < FrameBlocksHigh; by++ {
				rowBase := base + by*FrameBlocksWide
				var norm float64
				for d := 0; d < FrameBlocksWide; d++ {
					m := a.agg[rowBase+d].mean()
					mcol[d] = m
					norm += m * m
				}
				if norm == 0 {
					continue
				}
				norm = math.Sqrt(norm) * math.Sqrt(FrameBlocksWide)
				for bx := 0; bx < FrameBlocksWide; bx++ {
					var corr float64
					for cc := 0; cc < FrameBlocksWide; cc++ {
						corr += preambleE[cc] * mcol[(bx+cc)&(FrameBlocksWide-1)]
					}
					score := math.Abs(corr) / norm
					if score <= 0 {
						continue
					}
					pol := 1.0
					if corr < 0 {
						pol = -1
					}
					best.offer(candidate{
						scaleIdx: scaleIdx, cellPx: cellPx,
						ox: ox, oy: oy, qy: qy, qx: qx,
						by: by, bx: bx, polarity: pol, score: score,
						coverage: coverageOf(cols, rows, qx, qy),
					})
				}
			}
		}
	}
}

// coverageOf 是块相位 (qx,qy) 能凑出的完整残差类占比（1 表示 16×7 全齐）。
//
// 只用来在分数打平时决胜，不参与打分：它衡量的是"这个相位能不能读出完整的一帧"，
// 与"读出来的东西像不像前导"是两个正交的问题。
func coverageOf(cols, rows, qx, qy int) float64 {
	blockCols := 0
	if cols > qx {
		blockCols = (cols - qx) / BlockCells
	}
	blockRows := 0
	if rows > qy {
		blockRows = (rows - qy) / BlockCells
	}
	blockCols = min(blockCols, FrameBlocksWide)
	blockRows = min(blockRows, FrameBlocksHigh)
	return float64(blockCols*blockRows) / float64(FrameBlocksWide*FrameBlocksHigh)
}

// decode 验证一个假设：读出 112 位，验前导、验统计门、验 CRC。
//
// 只重算这个假设所在的那一个块相位（其余 15 个与之无关），因此一次验证的代价只有
// 整轮扫描的一个零头。三道关按"便宜的在前"排：前导是纯整数比较，统计门要算方差，
// CRC 要重打包 80 位。
func (a *analyzer) decode(c candidate) (result, bool) {
	cols, rows := a.cellMeans(c.cellPx, c.ox, c.oy)
	if cols == 0 {
		return result{}, false
	}
	a.highPass(cols, rows)

	a.resetAgg()
	for i := c.qy; i+BlockCells <= rows; i += BlockCells {
		rowRes := (i / BlockCells) % FrameBlocksHigh
		for j := c.qx; j+BlockCells <= cols; j += BlockCells {
			m := blockCorr(a.hp, i*cols+j, cols)
			colRes := (j / BlockCells) % FrameBlocksWide
			s := &a.agg[rowRes*FrameBlocksWide+colRes]
			s.n++
			s.sum += float64(m)
			s.sumsq += float64(m) * float64(m)
		}
	}

	var bits [FrameBlocksWide * FrameBlocksHigh]byte
	strong, total := 0, 0
	for r := 0; r < FrameBlocksHigh; r++ {
		rowRes := (c.by + r) % FrameBlocksHigh
		for col := 0; col < FrameBlocksWide; col++ {
			colRes := (c.bx + col) & (FrameBlocksWide - 1)
			s := &a.agg[rowRes*FrameBlocksWide+colRes]
			if s.n == 0 {
				return result{}, false
			}
			m := s.mean()
			// 标准误用"和与平方和"直接算，省一趟重扫。
			variance := s.sumsq/float64(s.n) - m*m
			se := math.Sqrt(math.Max(0, variance)) / math.Sqrt(float64(s.n))
			if se == 0 {
				// 所有副本完全一致（合成图里常见）：均值非零即显著。
				if m != 0 {
					strong++
				}
			} else if math.Abs(m)/se >= strongBitZ {
				strong++
			}
			total++
			if m*c.polarity < 0 {
				bits[r*FrameBlocksWide+col] = 1
			}
		}
	}

	pre, code, gotCRC := splitFrame(bits[:])
	if pre != preamble {
		return result{}, false
	}
	wantBits, err := codeToBits(code)
	if err != nil {
		return result{}, false
	}
	if gotCRC != crcOfBits(wantBits) {
		return result{}, false
	}
	if float64(strong) < strongBitRatio*float64(total) {
		return result{}, false
	}
	return result{code: code, score: c.score, strength: float64(strong) / float64(total)}, true
}
