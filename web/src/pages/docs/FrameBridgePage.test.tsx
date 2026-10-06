import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it } from 'vitest'

import { FRAME_CHANNEL, FRAME_CHANNEL_VERSION, FRAME_IMAGE_PREVIEW_TYPE } from '../galaxy/frame-channel'
import { FrameBridgePage } from './FrameBridgePage'

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

/** 一句话的长度上限。这一页是接口参考，不是介绍页（见 docs/design/web/docs-area.md）。 */
const MAX_SENTENCE = 60

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

/**
 * 取出正文里的句子。
 *
 * **代码块与表格整块排除。** 二者都是数据，不是散文：示例 JSON 里没有句号，按句号
 * 切会把整段当成一句；表格单元格之间也没有标点，相邻单元格会连成一句。"一句一件事"
 * 这条要求管的是正文。
 */
function sentences(container: HTMLElement): string[] {
  const clone = container.cloneNode(true) as HTMLElement
  for (const node of Array.from(clone.querySelectorAll('pre, table'))) {
    node.remove()
  }
  return (clone.textContent ?? '')
    .split(/[。！？]/)
    .map((sentence) => sentence.trim())
    .filter((sentence) => sentence !== '')
}

describe('页面与外壳的通道参考页', () => {
  // **协议只有一处定义，这一页从那里取。** 手抄一份的表现是：协议改了而这一页
  // 不声不响地继续说旧的——它是对外契约，说错话比看不了更糟。
  it('正文里的协议取值来自实现里那一份', async () => {
    const text = (await renderPage()).textContent ?? ''

    expect(text).toContain(`"channel": "${FRAME_CHANNEL}"`)
    expect(text).toContain(`"version": ${FRAME_CHANNEL_VERSION}`)
    expect(text).toContain(`"type": "${FRAME_IMAGE_PREVIEW_TYPE}"`)
  })

  // 字段与取值是这一页的主体，四行缺一不可。
  it('列全消息的四个字段', async () => {
    const text = (await renderPage()).textContent ?? ''

    for (const field of ['channel', 'version', 'type', 'payload']) {
      expect(text, `字段表里少了 ${field}`).toContain(field)
    }
  })

  // 方向只有两个，而"外壳 → 文档页"在版本 1 里是空的——这一条不写，作者会以为
  // 自己能收到外壳的消息。
  it('列出两个方向，并写明反向未定义', async () => {
    const text = (await renderPage()).textContent ?? ''

    expect(text).toContain('文档页 → 外壳')
    expect(text).toContain('外壳 → 文档页')
    expect(text).toContain('未定义')
  })

  // 五条处理规则就是这条通道的边界。少一条，作者就会以为它比实际更能干。
  it('写全五条处理规则', async () => {
    const text = (await renderPage()).textContent ?? ''

    expect(text).toContain('外壳只处理它嵌入的那个页面发来的消息')
    expect(text).toContain('外壳不运行 payload 中的内容')
    expect(text).toContain('外壳限制图片数量与地址长度')
    expect(text).toContain('收发双方丢弃未知的 type')
    expect(text).toContain('收发双方丢弃通道名或版本不匹配的消息')
  })

  // 三处最容易误解的地方：以为站点槽也有、以为直连发布域也能弹、以为消息能带代码。
  it('写清适用范围', async () => {
    const text = (await renderPage()).textContent ?? ''

    expect(text).toContain('本通道只用于 docs 槽的页面')
    expect(text).toContain('直接打开发布域地址时没有外壳')
    expect(text).toContain('site 槽的页面没有本通道')
    expect(text).toContain('消息只包含数据，不包含代码')
  })

  // 发布域是部署实例的值，写进仓库就违反"仓库不含实例值"（见 docs/deploy.md
  // 的可验证性表）。示例里一律用 <…> 占位（与 GalaxyPage 同一条约束）。
  it('不出现任何绝对地址', async () => {
    const container = await renderPage()

    expect(container.textContent ?? '').not.toMatch(/https?:\/\//)
  })

  // **这一页是接口参考，不是介绍页。** 它曾经写成一篇带背景与理由的说明，读起来
  // 像设计文档。长句正是那种写法的痕迹，因此把"一句别太长"钉在这里——它在这页
  // 上是可判定的，也是唯一能挡住它漂回说明文的办法。
  it('正文里没有长句', async () => {
    const long = sentences(await renderPage()).filter((sentence) => sentence.length > MAX_SENTENCE)

    expect(long, `这些句子超过 ${MAX_SENTENCE} 字：\n${long.join('\n')}`).toEqual([])
  })
})
