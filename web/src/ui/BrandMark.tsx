import markDark from '../brand/mark-dark.svg'
import markLight from '../brand/mark.svg'
import { useTheme } from '../theme'

/**
 * 品牌标识。
 *
 * 两份 SVG 由 `docs/design/web/brand/generate-icons.sh` 从品牌源文件生成（见
 * [那个目录的说明](../../../docs/design/web/brand/README.md)），形状因此只有一处定义——
 * 这里不内联路径数据：内联一份就等于图标和页签图标各画各的，而两处画歪一点
 * 是并排看才发现的。
 *
 * 明暗跟着主题走，与 favicon 那条 `prefers-color-scheme` 是**同一个判断的两种问法**：
 * 页签图标由浏览器按系统偏好挑，界面里这一处由用户选的（或跟随系统的）主题挑。
 * 两者可能不一致——用户把站点设成暗色、系统还是亮色时就是这样，而那时界面里
 * 应当是暗色版：它回答的是"这一页长什么样"。
 */
export function BrandMark({
  size = 20,
  label = '',
}: {
  /** 边长（像素）。标识是正方形，宽高同一个值。 */
  size?: number
  /**
   * 无障碍名。留空表示旁边已经有文字写着这是哪个站，这一份是装饰——
   * 读屏软件不该把同一个名字念两遍。
   */
  label?: string
}): React.ReactNode {
  const { resolved } = useTheme()

  return (
    <img
      src={resolved === 'dark' ? markDark : markLight}
      // 显式给出固有尺寸：不给的话浏览器要等图加载完才知道它多大，品牌行会先按
      // 0 宽排一遍再跳一下。
      width={size}
      height={size}
      alt={label}
      style={{ display: 'block', flexShrink: 0 }}
    />
  )
}
