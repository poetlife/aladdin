import { Image, Typography } from 'antd'

/**
 * 媒体呈现区的高度上限（资产面板的卡片里那个固定的高度）。
 *
 * 源码视图不吃这个上限：那一栏本来就自带滚动，硬压到一条窄带里等于把"看原图"
 * 又收回去了。
 */
export const MEDIA_HEIGHT = 132

interface AssetMediaProps {
  /** 短时直读地址。空串表示这一刻取不到，如实说明。 */
  url: string
  /** 服务端记录的内容类型；它决定用哪一种呈现，前端不按扩展名猜。 */
  mediaType: string
  /** 图片的替代文本，也是"这一份是什么"的那句话。 */
  label: string
  /** 呈现的高度上限；不给就不设上限，只受容器宽度约束。 */
  maxHeight?: number
}

/**
 * 一份资产的呈现：图片、视频、音频各用各的控件。
 *
 * 它是**查看**，不是缩略图——本模块不做转码，显示的就是原样的字节（见
 * docs/design/galaxy/asset-library.md）。**不认识的内容类型不猜**：返回 null，
 * 由调用方那句"这一份是什么"兜底，免得把一个不支持的格式硬塞进 `<img>` 里
 * 显示成一张裂开的图。
 */
export function AssetMedia({ url, mediaType, label, maxHeight }: AssetMediaProps): React.ReactNode {
  if (url === '') {
    return <Typography.Text type="secondary">无预览</Typography.Text>
  }
  const cap = maxHeight === undefined ? {} : { maxHeight }
  if (mediaType.startsWith('image/')) {
    return <Image src={url} alt={label} style={{ maxWidth: '100%', objectFit: 'contain', ...cap }} />
  }
  if (mediaType.startsWith('video/')) {
    return <video src={url} controls style={{ maxWidth: '100%', ...cap }} />
  }
  if (mediaType.startsWith('audio/')) {
    return <audio src={url} controls style={{ width: '100%' }} />
  }
  return null
}
