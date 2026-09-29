import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { PreviewFrame } from './PreviewFrame'

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

async function renderPreview(html: string): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(<PreviewFrame html={html} />)
  })
  return container
}

function iframe(container: HTMLElement): HTMLIFrameElement {
  const element = container.querySelector('iframe')
  expect(element, '没有渲染出 iframe').not.toBeNull()
  return element as HTMLIFrameElement
}

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
  vi.restoreAllMocks()
})

describe('预览沙箱', () => {
  // spec 明确要求的核查项：预览落在不透明源上，沙箱属性里不得出现 allow-same-origin。
  it('沙箱属性里没有 allow-same-origin', async () => {
    const container = await renderPreview('<!doctype html><h1>hi</h1>')

    const sandbox = iframe(container).getAttribute('sandbox') ?? ''
    expect(sandbox).not.toContain('allow-same-origin')
    // 脚本仍要能跑（内容里可能有用户自己的脚本），因此 allow-scripts 必须在。
    expect(sandbox).toContain('allow-scripts')
  })

  // 渲染在服务端（与发布共用同一段实现），因此这里把服务端给的那份 HTML
  // **原样**放进 srcDoc：前端不再做任何改写，也就不可能与发布态漂移。
  it('服务端渲染的结果原样进 srcDoc，前端不改写', async () => {
    const html = '<!doctype html>\n<html><body><img src="/g/prj_x/logo.png"></body></html>'

    const container = await renderPreview(html)

    expect(iframe(container).getAttribute('srcdoc')).toBe(html)
  })
})
