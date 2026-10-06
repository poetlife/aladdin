import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it } from 'vitest'

import { FRAME_CHANNEL, FRAME_CHANNEL_VERSION, FRAME_IMAGE_PREVIEW_TYPE } from '../galaxy/frame-channel'
import { FrameBridgePage } from './FrameBridgePage'

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

async function renderPage(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(<FrameBridgePage />)
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

describe('页面与外壳的通道介绍页', () => {
  // **协议只有一处定义，这一页从那里取。** 手抄一份的表现是：协议改了而这一页
  // 不声不响地继续说旧的——它是对外契约，说错话比看不了更糟。
  it('正文里的协议取值来自实现里那一份', async () => {
    const text = (await renderPage()).textContent ?? ''

    expect(text).toContain(`"channel": "${FRAME_CHANNEL}"`)
    expect(text).toContain(`"version": ${FRAME_CHANNEL_VERSION}`)
    expect(text).toContain(`"type": "${FRAME_IMAGE_PREVIEW_TYPE}"`)
  })

  // 这一章要教的第一件事：点图开大图是平台做的，作者一行都不用写。
  it('先说清它不需要作者做任何事', async () => {
    const text = (await renderPage()).textContent ?? ''

    expect(text).toContain('![甲图](a.png)')
    expect(text).toContain('不需要写任何东西')
  })

  // 两侧各判什么是这条通道的边界所在。少说一条，作者就会以为它比实际更能干。
  it('讲清两侧各判什么', async () => {
    const text = (await renderPage()).textContent ?? ''

    // 外壳只认自己那一帧（不透明源的来源标识认不出是谁发的）。
    expect(text).toContain('外壳只认自己那一帧')
    // 外壳只渲染不执行——别把"请执行某件事"塞进载荷。
    expect(text).toContain('外壳只渲染，不执行')
    // 载荷体量不由发消息那一侧单方面决定。
    expect(text).toContain('不由发消息那一侧说了算')
    // 页面只认信封，认不出的类型一律忽略。
    expect(text).toContain('页面只认信封')
  })

  // 三个人最容易误解的地方：以为站点槽也有、以为直连发布域也能弹。
  it('说清这条通道不管什么', async () => {
    const text = (await renderPage()).textContent ?? ''

    expect(text).toContain('只有文档槽的页面有它')
    expect(text).toContain('没有外壳就没有大图')
    expect(text).toContain('直接打开')
  })

  // 发布域与图片地址都是部署实例的值，写进仓库就违反"仓库不含实例值"
  //（见 docs/deploy.md 的可验证性表）。这一页通篇在讲地址，是最容易顺手写死一个
  // 域名的地方——示例里一律用 <…> 占位（与 GalaxyPage 同一条约束）。
  it('不出现任何绝对地址', async () => {
    const container = await renderPage()

    expect(container.textContent ?? '').not.toMatch(/https?:\/\//)
  })
})
