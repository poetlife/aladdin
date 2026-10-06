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
 * 把高度约束挪到弹窗自己身上还不够，**滚动条画在哪一侧也是要管的**。antd 把整个
 * 弹窗的内边距挂在**容器**上，于是容器里任何一层去滚，滚动条都落在它自己的内边距里
 * 侧：一处既不贴着弹窗外缘、也不属于任何内容的悬浮柱子。所以头、内容、尾三块一起
 * 用负的横向外边距铺到弹窗边缘，再各自把内边距加回来；滚动层的内边距由它里面那层
 * `div` 承担——它自己必须留白为零，否则滚动条又缩回去了。
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
  const horizontal = token.paddingContentHorizontalLG

  // 头、内容、尾都**横向铺满弹窗**，各自再把自己的内边距加回来。
  //
  // 三块必须一起铺：只让内容区铺出去，它就会比上面那一条标题窄 24px，滚动条从标题
  // 右缘外面"冒出来"——那道台阶看着像渲染坏了，而它其实是两条边界对不上。一起铺
  // 之后三块的左右边界重合，滚动条是这条边界的一部分，不再是插进来的东西。
  //
  // 内边距**不能加在内容区自己身上**：滚动条画在滚动容器的内边距里侧，加回去就又
  // 成了悬在里面的一根柱子。所以内容区的内边距由它里面那一层承担。
  const bleed: React.CSSProperties = { marginInline: `calc(-1 * ${horizontal}px)` }

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
        },
        // 头尾不参与滚动，内边距直接加在它们身上。
        header: { flex: 'none', ...bleed, paddingInline: horizontal },
        // `minHeight: 0` 不能少：flex 子项的默认最小高度是它的内容高度，少了这一条，
        // 下面这个 `flex: 1` 在内容超长时根本不收缩，滚动也就落不到这里。
        body: { flex: '1 1 auto', minHeight: 0, overflowY: 'auto', ...bleed },
        footer: { flex: 'none', ...bleed, paddingInline: horizontal },
      }}
    >
      {/* 横向内边距由这一层补回来，滚动条才贴得到弹窗内缘。 */}
      <div style={{ paddingInline: horizontal }}>{children}</div>
    </Modal>
  )
}
