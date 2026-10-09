#!/usr/bin/env bash
#
# 由品牌源文件生成站点要用的图标。
#
# 跑这个脚本的场景只有一种：**aladdin-mark.svg 的形状改了**。产出的文件是提交进仓库
# 的（web/public/ 下），不参与构建或 CI——favicon 是浏览器在首屏之前就要的东西，
# 没有让流水线在发版时渲染一遍的理由。
#
# 因此它是维护者在自己机器上跑的一次性工具，不是构建步骤：它依赖 macOS 的 sips
# （渲染 SVG）与 python3（拼 ICO 容器）。换机器重跑得到的像素可能有微小差异，
# 那不是问题——源是 SVG，栅格产物只是它在若干尺寸上的快照。
#
# 用法：docs/design/web/brand/generate-icons.sh
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../../../.." && pwd)"
out="$root/web/public"
assets="$root/web/src/brand"

# 应用图标的圆角半径与边距，都按 64 的坐标系给（源文件的 viewBox）。
#
# 两者是**这一处**的值：方形图标长什么样（圆角多大、图形留多少白）只在这个脚本里
# 定义一次，favicon、主屏图标、托盘图标因此不会各自长成不同的胖瘦。
radius=14
inset=0.82

need() { command -v "$1" >/dev/null 2>&1 || { echo "缺少 $1" >&2; exit 1; }; }
need sips
need python3

mkdir -p "$assets"

# tile_svg <浅色|暗色> <边长> 写出一个方形应用图标的 SVG。
#
# 底与图形都从 aladdin-mark*.svg 取：图形那一段是**原样搬过来的**（连 defs 一起），
# 因此不存在"这里再描一遍路径"的可能——两处路径一旦漂移，表现是图标和侧边栏标识
# 长得不一样，而那要等到有人并排看才发现。
tile_svg() {
	local variant="$1" size="$2" src bg
	case "$variant" in
	light) src="$here/aladdin-mark.svg"; bg='#FFFFFF' ;;
	dark) src="$here/aladdin-mark-dark.svg"; bg='#111114' ;;
	*) echo "未知变体：$variant" >&2; exit 1 ;;
	esac

	python3 - "$src" "$size" "$bg" "$radius" "$inset" <<'PY'
import re
import sys

src, size, bg, radius, inset = sys.argv[1:6]
size, inset = float(size), float(inset)
svg = open(src, encoding="utf-8").read()

# 取 <svg> 的内容，去掉注释与 <title>：前者是给读源文件的人看的，后者是给
# 无障碍树看的——两者都已在**源文件**里表达过，搬进产物只会重复。
inner = svg[svg.index(">") + 1 : svg.rindex("</svg>")]
inner = re.sub(r"<!--.*?-->", "", inner, flags=re.S)
inner = re.sub(r"<title>.*?</title>", "", inner, flags=re.S).strip()

# <defs> 必须**留在顶层**：下面的 <g> 带缩放，而 defs 放在里面时其中的
# userSpaceOnUse 渐变会跟着被缩放，于是渐变的坐标与图形的坐标不再是一套——
# 表现是渲染出来只剩一片空白（渐变取的取样区落在图形外面）。
defs = "".join(re.findall(r"<defs>.*?</defs>", inner, flags=re.S))
inner = re.sub(r"<defs>.*?</defs>", "", inner, flags=re.S).strip()

# 图形按 viewBox 的 64 格居中缩放。**下面这些坐标全是 viewBox 单位**，与导出尺寸
# 无关：width/height 只决定栅格化的像素数，图形一律在这个 64×64 的坐标系里描述。
# 混进导出尺寸的表现是——一个尺寸对、其余尺寸整个图形跑到画布外面（空白）。
#
# 缩放的锚点取图形的视觉中心 (31, 30.5) 而不是框心 (32, 32)：这个形状是不对称的，
# 按框心缩会让它看起来偏右下。
inner = f'<g transform="translate(32 32) scale({inset:g}) translate(-31 -30.5)">{inner}</g>'

print(
    f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" '
    f'width="{size:g}" height="{size:g}">'
    f'{defs}<rect width="64" height="64" rx="{radius}" fill="{bg}"/>{inner}</svg>'
)
PY
}

# render <浅色|暗色> <边长> <输出路径>
render() {
	tile_svg "$1" "$2" >"${TMPDIR:-/tmp}/aladdin-tile.svg"
	sips -s format png "${TMPDIR:-/tmp}/aladdin-tile.svg" --out "$3" >/dev/null
}

echo ">>> 应用图标（SVG，浏览器优先取它）"
tile_svg light 64 >"$out/favicon.svg"
tile_svg dark 64 >"$out/favicon-dark.svg"

echo ">>> 前端运行时用的标识（侧边栏品牌区）"
# 侧边栏那一处**不带底**：它已经站在界面的底色上，再套一层方形底就成了一个
# 贴错地方的图标。因此这两份是源文件的副本，只是加一行"这是生成的"——
# 组件 import 它们，于是"标识长什么样"在仓库里仍然只有一处定义。
#
# 复制而不是让组件去 import 品牌目录里的源文件：那个目录在 web/ 之外，构建工具
# 跨根引用它要额外放开一圈（dev 的 fs.allow、构建时的产物路径），而多放开的那一圈
# 只要有人动过，表现就是"本地好好的、打包出来图没了"。
for v in light dark; do
	case "$v" in light) src="$here/aladdin-mark.svg" dest="$assets/mark.svg" ;; dark) src="$here/aladdin-mark-dark.svg" dest="$assets/mark-dark.svg" ;; esac
	{
		echo "<!--"
		echo "  由 docs/design/web/brand/generate-icons.sh 生成，请勿手工修改。"
		echo "  形状的唯一来源是 docs/design/web/brand/$(basename "$src")。"
		echo "-->"
		cat "$src"
	} >"$dest"
done

echo ">>> 栅格图标"
# 大图各渲一次；16 与 32 直接按目标尺寸渲染而不是缩小大图——小尺寸下曲线的落点
# 差半个像素就换一副样子，让渲染器在目标尺寸上自己解比事后重采样准。
render light 512 "$out/icon-512.png"
render light 192 "$out/icon-192.png"
render light 180 "$out/apple-touch-icon.png"
render light 32 "$out/.favicon-32.png"
render light 16 "$out/.favicon-16.png"

echo ">>> favicon.ico（16 与 32 两档，PNG 压缩的条目）"
# ICO 只是几个尺寸的容器：头部一张目录，后面跟着各档的字节。用 PNG 装条目是
# Vista 之后的通行做法，所有还在被使用的浏览器都认。
python3 - "$out/.favicon-16.png" "$out/.favicon-32.png" "$out/favicon.ico" <<'PY'
import struct
import sys

*images, dest = sys.argv[1:]
blobs = [open(p, "rb").read() for p in images]

header = struct.pack("<HHH", 0, 1, len(blobs))  # 保留位、类型 1（图标）、档数
offset = len(header) + 16 * len(blobs)

entries, data = b"", b""
for path, blob in zip(images, blobs):
    side = int(path.rsplit("-", 1)[1].split(".")[0])
    # 256 在这一个字节里写 0（放不下）；本脚本只做 16 与 32，留着是为了下次
    # 加档时不会静默写出一个非法值。
    entries += struct.pack("<BBBBHHII", side % 256, side % 256, 0, 0, 1, 32, len(blob), offset)
    data += blob
    offset += len(blob)

open(dest, "wb").write(header + entries + data)
PY

rm -f "$out/.favicon-16.png" "$out/.favicon-32.png"

echo ">>> 已产出："
ls -1 "$out"/favicon.svg "$out"/favicon-dark.svg "$out"/favicon.ico \
	"$out"/apple-touch-icon.png "$out"/icon-192.png "$out"/icon-512.png
