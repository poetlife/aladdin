import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { PreviewFrame } from './PreviewFrame'

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

async function renderPreview(url: string): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(<PreviewFrame url={url} />)
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
    const container = await renderPreview('https://pub.example.com/g/p/tok/prj_x/index.html')

    const sandbox = iframe(container).getAttribute('sandbox') ?? ''
    expect(sandbox).not.toContain('allow-same-origin')
    // 脚本仍要能跑（内容里可能有用户自己的脚本），因此 allow-scripts 必须在。
    expect(sandbox).toContain('allow-scripts')
  })

  // 预览加载的是**一条地址**而不是塞进来的一份 HTML：页面因此有自己的源与目录，
  // 页内的相对地址与站点绝对地址都由浏览器自己解析（见 authoring.md 的"预览"）。
  // 地址由服务端给出，前端原样使用，不拼、不改写。
  it('服务端给的地址原样进 src', async () => {
    const url = 'https://pub.example.com/g/p/tok/prj_x/index.html'

    const container = await renderPreview(url)

    const element = iframe(container)
    expect(element.getAttribute('src')).toBe(url)
    // 塞一份 HTML 的那条路已经不存在了。
    expect(element.getAttribute('srcdoc')).toBeNull()
  })
})
