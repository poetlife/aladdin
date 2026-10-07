import { Flex, Spin } from 'antd'

/**
 * 等待时说的那一句话（**唯一一份**）。
 *
 * 它对谁都一样，因为对读者来说这些等待是同一件事："我要的这一页还没到。" 分成
 * 「正在加载内容」「正在加载页面」「正在获取数据」几句只会让人以为它们不一样。
 * 导出它是给测试用的——断言"有加载态"时不必把这句话再抄一遍。
 */
export const LOADING_TEXT = '正在加载…'

interface LoadingHintProps {
  /**
   * 所在那一块最矮多高。撑满的容器（一整屏的壳、一整帧的预览）给不给都一样——
   * 那时它由 `height: 100%` 决定；**高度由内容决定的地方（整页）必须给**，否则
   * 这个框只有一行高，看起来就是"贴在顶上的一颗转圈"。
   */
  minHeight?: number | string
}

/**
 * 一块还没有内容可显示的地方，在等的时候长什么样（**唯一入口**）。
 *
 * 它只做两件事，但每一处都得做：**居中**，以及**说一句在等什么**。
 *
 * - **不居中的表现是"loading 在边上"。** 一颗 `display: block` 的转圈会贴住所在
 *   容器的左缘；位置由内容决定的地方（整页）更是只剩一行高，于是一整块空白上只有
 *   左上角一颗小点——看起来既不像在加载，也不像坏了。
 * - **没有那句话时，一颗孤零零的转圈和一次加载失败长得一样。** 读者分不出"再等等"
 *   与"不会再来了"，而这两种情形该做的事完全不同（见 docs/design/uiux/README.md
 *   的"空态、加载与失败"）。
 *
 * **它存在的理由是这件事必须无法被忘记。** 此前三处各写各的：分享壳与预览各写了
 * 一遍（两处都带那句话、都居中），会话判定那一处只剩一颗没居中的转圈——同一件事写
 * 三遍，就一定会漂移成三种。所以这里把它收成一个组件，并且**那句话只有这一份**：
 * 调用方要换说法就是换一个概念，不该是各写各的。
 *
 * 管**长什么样**，不管**等多久**：什么时候从加载态转成"还没出来，要不要重试"是各自
 * 的时机问题（见 web/src/pages/galaxy/SandboxFrame.tsx 与
 * docs/design/galaxy/publication.md 的"主站壳"）。
 */
export function LoadingHint({ minHeight = 160 }: LoadingHintProps): React.ReactNode {
  return (
    <Flex align="center" justify="center" style={{ width: '100%', height: '100%', minHeight }}>
      <Spin size="large" description={LOADING_TEXT} />
    </Flex>
  )
}
