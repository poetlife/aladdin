import { Modal, theme } from 'antd'
import type { ModalProps } from 'antd'

/**
 * 弹窗（**唯一入口**）。
 *
 * 它只做一件事，但每个弹窗都必须做：**内容比窗口高时，滚的是内容区**。
 *
 * antd 的默认做法是让 `.ant-modal-wrap`（整屏固定层）自己滚，于是：
 *
 *   - 滚动条画在**窗口右缘**，与弹窗本身没有任何视觉关系；
 *   - 标题与按钮跟着一起滚走，看着像弹窗在往上滑，而不是里面的内容在滚。
 *
 * 两件事合起来就是"这个弹窗的滚动条坏了"。
 *
 * 把高度约束挪到弹窗自己身上还不够，**还得把内边距挪个位置**：antd 把整个弹窗的
 * 内边距挂在**容器**上，因此只要容器里的某一层去滚，滚动条就画在那圈内边距的里侧
 * ——一根悬在弹窗里的深色柱子，离弹窗外缘 24px，既不贴着边也不属于任何东西。
 * 所以这里让容器与滚动层都不留内边距，内边距由里面一层普通 `div` 承担：滚动条贴
 * 着弹窗内缘，与页面其余地方的滚动条长得一样。
 *
 * **它存在的理由是这件事必须无法被忘记。** 此前它在工作台的两个面板上被单独解过
 * 一次（一个本地常量），而"单独解一次"的下场就是别的弹窗继续踩同一个坑：靠每个
 * 弹窗自己写一遍 `styles`，迟早有人漏掉。所以这里把它收成一个组件，并且**不再从
 * antd 直接引 `Modal`**（见 docs/ssot-registry.md 与 docs/design/uiux/README.md
 * 的"弹窗"）。
 *
 * `styles` 由本组件独占，因此从入参里去掉：两个地方都能设高度，就等于两个地方都能
 * 把它设错。
 */
export function AppModal({ className, rootClassName, children, ...rest }: Omit<ModalProps, 'styles'>): React.ReactNode {
  const { token } = theme.useToken()
  // 内边距的取值全部来自 token，不写死像素：换主题算法时它们跟着变，而"弹窗的
  // 内边距与卡片的一致"这件事不需要有人记得。
  const horizontal = token.paddingContentHorizontalLG
  const vertical = token.paddingMD

  return (
    <Modal
      {...rest}
      className={className}
      rootClassName={rootClassName}
      styles={{
        // 视口减去顶部偏移（antd 自己的 top=100px）与底部留白（padding-bottom=24px），
        // 再留一点余量：**宁可比能给的高度小一点，也不要大到又撑出整屏滚动**。
        container: {
          maxHeight: 'calc(100dvh - 160px)',
          display: 'flex',
          flexDirection: 'column',
          padding: 0,
        },
        // 头尾的内边距由这里补回来（原来由容器给），它们不参与滚动。
        header: { flex: 'none', padding: `${vertical}px ${horizontal}px 0` },
        // `minHeight: 0` 不能少：flex 子项的默认最小高度是它的内容高度，少了这一条，
        // 下面这个 `flex: 1` 在内容超长时根本不收缩，滚动也就落不到这里。
        body: { flex: '1 1 auto', minHeight: 0, overflowY: 'auto' },
        footer: { flex: 'none', padding: `0 ${horizontal}px ${vertical}px` },
      }}
    >
      {/* 这一层承担原来挂在容器上的内边距。放在滚动层**里面**，滚动条才贴得到边。 */}
      <div style={{ padding: `${vertical}px ${horizontal}px` }}>{children}</div>
    </Modal>
  )
}
