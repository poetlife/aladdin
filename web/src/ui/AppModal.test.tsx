import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { AppModal } from './AppModal'

// 这一条守的是**滚动落在谁身上**。jsdom 没有布局，量不出"滚动条画在哪儿"，但
// 高度约束是内联样式，落在哪个元素上是查得出来的——那正是这个组件存在的全部内容
// （见 docs/design/uiux/README.md 的"弹窗"）。

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

async function renderModal(open = true): Promise<void> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <AppModal title="对话框" open={open} onCancel={() => {}}>
        <div style={{ height: 2000 }}>很长很长</div>
      </AppModal>,
    )
  })
}

beforeEach(() => {
  document.body.replaceChildren()
})

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
})

describe('AppModal', () => {
  it('高度约束在弹窗自己身上，滚动落在内容区', async () => {
    await renderModal()

    const dialog = document.body.querySelector<HTMLElement>('.ant-modal-container')
    expect(dialog, '没有渲染出弹窗').not.toBeNull()
    expect(dialog?.style.maxHeight).not.toBe('')
    expect(dialog?.style.display).toBe('flex')
    expect(dialog?.style.flexDirection).toBe('column')

    // 滚动层必须**铺到容器内边距之外**，滚动条才贴得到弹窗内缘；否则它画在那圈
    // 内边距里侧，成了一根悬在弹窗里的柱子。
    //
    // 铺的方式是负外边距，而**不是**把容器的内边距清零：容器的内边距决定头尾的横向
    // 位置，清零就得在别处再补一遍——补出来的那一份与滚动层不是同一条边界，于是又
    // 回到"从标题右缘外面冒出一根柱子"。
    expect(dialog?.style.padding).toBe('')

    const body = document.body.querySelector<HTMLElement>('.ant-modal-body')
    expect(body?.getAttribute('style')).toContain('margin-inline')
    expect(body?.style.overflowY).toBe('auto')
    // 少了 min-height:0，这个 flex:1 在内容超长时根本不收缩——滚动也就落不到
    // 这里，整屏那层又开始滚。这一条是那个坑的钉子。
    expect(body?.style.minHeight).toBe('0px')
  })

  it('标题与按钮不参与滚动', async () => {
    await renderModal()

    const header = document.body.querySelector<HTMLElement>('.ant-modal-header')
    const footer = document.body.querySelector<HTMLElement>('.ant-modal-footer')
    expect(header?.style.flex).toContain('0')
    expect(footer?.style.flex).toContain('0')
  })

  it('关闭时不渲染内容', async () => {
    await renderModal(false)
    expect(document.body.querySelector('.ant-modal-body')).toBeNull()
  })
})
