import { create } from '@bufbuild/protobuf'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { AssetSchema } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import type { Asset } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { PreviewFrame } from './PreviewFrame'

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

async function renderPreview(content: string, assets: readonly Asset[] = []): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(<PreviewFrame content={content} assets={assets} />)
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
    // 脚本仍要能跑（正文里可能内联脚本），因此 allow-scripts 必须在。
    expect(sandbox).toContain('allow-scripts')
  })

  it('正文原样进 srcDoc，不包裹也不裁剪', async () => {
    const html = '<!doctype html>\n<html><body><h1>你好</h1></body></html>'

    const container = await renderPreview(html)

    expect(iframe(container).getAttribute('srcdoc')).toBe(html)
  })

  it('占位符换成资产地址；取不到地址的占位符原样留着', async () => {
    const asset = create(AssetSchema, {
      id: 'a1b2c3',
      url: 'https://cos.example/signed/a1b2c3',
      mediaType: 'image/png',
      filename: 'pic.png',
      sizeBytes: 1n,
    })

    const container = await renderPreview(
      '<img src="asset://a1b2c3"><img src="asset://missing">',
      [asset],
    )

    const srcDoc = iframe(container).getAttribute('srcdoc') ?? ''
    expect(srcDoc).toContain('https://cos.example/signed/a1b2c3')
    // 取不到地址就不改写——预览不裁剪，坏引用原样显示为坏的。
    expect(srcDoc).toContain('asset://missing')
  })
})
