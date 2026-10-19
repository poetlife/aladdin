import type { WatermarkLayer } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'

/**
 * 铺在已发布站点上的水印：**明水印那一层文字 + 盲水印那一层平铺图案**。
 *
 * 两件事叠在同一个位置上，但它们的取舍完全不同（见
 * docs/design/galaxy/watermark.md）：
 *
 * - **明水印**是威慑：让看到的人（与截图的人）当场知道这一份带标识。因此它必须
 *   看得见，又不能压过正文——斜排、疏排、低不透明度。
 * - **盲水印**是取证：不可见、可提取。图案是服务端随解析响应下发的一张 PNG，按
 *   一格的 CSS 像素数铺满这一层；它之所以由服务端生成，是因为前端再算一遍就是
 *   同一个算法的第二份实现，而它的漂移只有在拿到一张真截图时才会被发现。
 *
 * **这一层只在壳上。** 它不进 iframe、不进用户内容，因此 `site` 槽的产物仍然逐字
 * 相同（见 docs/design/galaxy/publication.md）。代价是直接打开发布域时没有水印，
 * 以及按 iframe 节点截图取不到盲水印——两条都在 spec 里如实记着。
 *
 * 三条硬性约束，缺一条这一层就会开始添麻烦：
 *
 * 1. **不接指针**。它盖在正文上，一旦吃掉了点击，页面就"看起来能点、点了没反应"。
 * 2. **不进无障碍树**。它是标记，不是内容；读屏软件念一遍访客自己的溯源码只是噪声。
 * 3. **不参与布局**。绝对定位铺满、不占文档流，否则页面会被挤出一条空白。
 */
interface WatermarkOverlayProps {
  /** 服务端在这次解析里签发的水印。没有它（或图案缺席）时这一层不渲染。 */
  watermark: WatermarkLayer
}

/**
 * 明水印那一行字重复多少遍、怎么排。
 *
 * 取值是"疏到不碍事、密到裁一刀也带一点"的那一档：斜向排布让它在正文上不像一条
 * 实线，重复铺满让它一屏里到处都在。
 */
const DISPLAY_TILES = 12

/**
 * 铺在已发布站点上的那一层水印：先盲水印（底），再明水印（上）。
 *
 * 顺序不可颠倒：明水印是看得见的那层字，压在图案上不影响图案的取样；反过来会让
 * 文字笔画穿过图案的格子，在那一片区域里破坏提取。
 */
export function WatermarkOverlay({ watermark }: WatermarkOverlayProps): React.ReactNode {
  const text = watermark.displayText
  const pattern = watermark.pattern
  // 没有文字也没有图案时整层不渲染：一个空的绝对定位层会挡住指针（万一将来有人
  // 改了下面那条 pointer-events），而它一个像素的信息都没有。
  if (text === '' && pattern.length === 0) {
    return null
  }

  return (
    <div
      aria-hidden="true"
      data-testid="watermark-overlay"
      style={{
        position: 'absolute',
        inset: 0,
        // 不接指针：水印盖在正文上，点击、滚动、选中都该落到内容上。
        pointerEvents: 'none',
        // 不参与布局，也不把宿主页面撑出一条空白。
        overflow: 'hidden',
        // 那一帧自己有圆角（见 SandboxFrame），这一层要跟着裁，否则四角会露出
        // 一圈水印——看起来就是"框画歪了"。
        borderRadius: 'inherit',
        // 自己成一层，避免与内容抢层叠上下文里的位置。
        zIndex: 1,
      }}
    >
      {pattern.length > 0 && <BlindWatermarkLayer image={patternBytesToURL(pattern)} />}
      {text !== '' && <MarkText text={text} />}
    </div>
  )
}

/**
 * 盲水印那一层：一张按**原尺寸**平铺的图案。
 *
 * 图案的几何只有一处定义——生成它的 internal/watermark（256×112 CSS 像素、一格
 * 4 像素）。这里既不放缩也不设 `background-size`：拉一格就等于换了一套格子尺寸，
 * 而提取侧假设的是生成时那一套，两边对不上的表现是"页面上明明有图案，却解不出码"。
 */
function BlindWatermarkLayer({ image }: { image: string }): React.ReactNode {
  return (
    <div
      data-testid="watermark-pattern"
      style={{
        position: 'absolute',
        inset: 0,
        backgroundImage: `url(${image})`,
        // 平铺：图案的宽高都是块尺寸的整数倍，因此接缝处天然咬合。
        backgroundRepeat: 'repeat',
        // 图案本身已经带 alpha（见 internal/watermark 的格子取值），这里不再叠
        // 一层 opacity——多一层会让"深浅两极"一起变淡，而它们正是被提取的那部分。
        opacity: 0.9,
      }}
    />
  )
}

/** 明水印那一行字，斜向疏排。 */
function MarkText({ text }: { text: string }): React.ReactNode {
  return (
    <div
      data-testid="watermark-text"
      style={{
        position: 'absolute',
        inset: 0,
        display: 'flex',
        flexWrap: 'wrap',
        gap: 24,
        alignContent: 'space-around',
        justifyContent: 'space-around',
        transform: 'rotate(-24deg) scale(1.2)',
        // 不提字重、不加描边：它在正文上，克制是这条呈现的唯一要求（见
        // docs/design/uiux/README.md 的"信息层级"——水印不是内容，不该争层级）。
        color: 'currentColor',
        fontSize: 13,
        lineHeight: 1,
        whiteSpace: 'nowrap',
        // 白色页与深色页都要看得见：取一个中间档，靠正文自己的对比度兜住。
        opacity: 0.08,
        userSelect: 'none',
      }}
    >
      {Array.from({ length: DISPLAY_TILES }, (_, index) => (
        <span key={index}>{text}</span>
      ))}
    </div>
  )
}

/**
 * 把服务端下发的 PNG 字节折成一张能直接当背景用的地址。
 *
 * 用 `data:` 而不是一条取字节的路径：图案随解析响应一起下发，**不新增一条地址、
 * 也不做变体**（见 docs/design/galaxy/watermark.md），因此这里没有可拼的 URL。
 * 这一层是宿主自己的样式，发布域的 `img-src` 管不到它。
 */
function patternBytesToURL(bytes: Uint8Array): string {
  let binary = ''
  const chunk = 0x8000
  for (let offset = 0; offset < bytes.length; offset += chunk) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + chunk))
  }
  return `data:image/png;base64,${btoa(binary)}`
}
