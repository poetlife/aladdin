import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it } from 'vitest'

import { LoadingHint, LOADING_TEXT } from './LoadingHint'

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

async function renderHint(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(<LoadingHint />)
  })
  return container
}

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
})

describe('加载提示', () => {
  // **"loading 在边上"就是这么来的。** 一颗 `display: block` 的转圈会贴住所在容器的
  // 左缘；位置由内容决定的地方（整页）更只剩一行高。jsdom 不做排版，因此"居中"只能
  // 按容器上的那两条断言：这是个 flex 容器，且横向、纵向都居中。
  it('横向与纵向都居中', async () => {
    const container = await renderHint()

    const box = container.firstElementChild
    expect(box, '没有渲染出容器').not.toBeNull()
    expect(box?.className, '不是 flex 容器').toContain('ant-flex')
    expect(box?.className, '横向居中不在了').toContain('ant-flex-justify-center')
    expect(box?.className, '纵向居中不在了').toContain('ant-flex-align-center')
  })

  // 一颗孤零零的转圈与一次加载失败长得一样。读者要能分出"再等等"与"不会再来了"。
  it('说清在等什么', async () => {
    const container = await renderHint()

    expect(container.textContent).toContain(LOADING_TEXT)
  })
})
