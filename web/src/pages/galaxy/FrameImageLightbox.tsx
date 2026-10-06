import { useEffect, useState } from 'react'
import { Image } from 'antd'

import type { FrameImagePreview } from './frame-channel'

interface FrameImageLightboxProps {
  /** 这一次要看的图与起点；`null` 表示没有正在看的东西，什么都不渲染。 */
  request: FrameImagePreview | null
  /** 关掉时调用（Esc、点遮罩、点关闭都走这一处）。 */
  onClose: () => void
}

/**
 * 宿主侧的图片预览：文档页里点了图，遮罩与大图由**宿主**渲染在宿主自己的页面上。
 *
 * 因此它不是往 iframe 里画一层遮罩：那样只能盖住内容那一块，一出内容区就点不到，
 * 本来就不是一次全局预览。这条分工见 docs/design/galaxy/site-model.md 的
 * "平台接入桥"。
 *
 * **当前下标由这一处受控。** 预览组打开时会把内部下标重置为 0，而"用户点的是第几张"
 * 只有我们知道，因此起点必须由外面给。受控之后翻页也不会失灵——`onChange` 把新下标
 * 写回同一份状态，受控值与内部动作因此是同向的，不是互相抵消。图按**文档顺序**排，
 * 于是计数、上一张 / 下一张的可用状态都是现成的，不需要另做折算。
 */
export function FrameImageLightbox({ request, onClose }: FrameImageLightboxProps): React.ReactNode {
  const [current, setCurrent] = useState(0)
  const index = request?.index ?? 0

  // 每一条请求都从被点的那一张开始——包括同一页里点了另一张图。
  useEffect(() => {
    setCurrent(index)
  }, [request, index])

  if (request === null) {
    return null
  }

  return (
    <Image.PreviewGroup
      items={request.images.map((image) => ({ src: image.src, alt: image.alt }))}
      preview={{
        open: true,
        current,
        onChange: setCurrent,
        onOpenChange: (open) => {
          if (!open) {
            onClose()
          }
        },
      }}
    />
  )
}
