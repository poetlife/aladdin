import { create } from '@bufbuild/protobuf'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as galaxyApi from '../../api/galaxy'
import {
  ResolveSharedPageResponseSchema,
  WatermarkLayerSchema,
} from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { LOADING_TEXT } from '../../ui/LoadingHint'
import { PublishedPage } from './PublishedPage'

vi.mock('../../api/galaxy', () => ({
  resolveSharedPage: vi.fn(),
}))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

async function renderAt(pathname: string): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <MemoryRouter initialEntries={[pathname]}>
        <PublishedPage />
      </MemoryRouter>,
    )
  })
  return container
}

beforeEach(() => {
  vi.clearAllMocks()
})

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
})

describe('主站壳', () => {
  // **首屏不留空白。** 壳要问服务端"这条分享地址对应哪一条内容地址"，这一问往返期间
  // 页面上什么都没有——访客看到的就是一段不知道是在加载还是坏了的空白。
  it('解析期间显示加载态，不留空白', async () => {
    vi.mocked(galaxyApi.resolveSharedPage).mockReturnValue(new Promise<never>(() => {}))

    const container = await renderAt('/g/p1')

    expect(container.textContent).toContain(LOADING_TEXT)
  })

  // 壳只做一件事：把**整条分享路径**交给服务端解析，再把给出的内容地址放进 iframe。
  // 传整条路径而不是（工程，槽，路径）三元组——槽（`docs` 那一段）的判定只有服务端
  // 一处，前端不做第二份形状解析。
  it('按整条路径解析，并把内容地址放进沙箱 iframe', async () => {
    const contentUrl = 'https://pub.example.com/g/p1/docs/guide'
    vi.mocked(galaxyApi.resolveSharedPage).mockResolvedValue(
      create(ResolveSharedPageResponseSchema, { contentUrl }),
    )

    const container = await renderAt('/g/p1/docs/guide')

    expect(galaxyApi.resolveSharedPage).toHaveBeenCalledWith('/g/p1/docs/guide')
    const iframe = container.querySelector('iframe')
    expect(iframe, '没有渲染出 iframe').not.toBeNull()
    expect(iframe?.getAttribute('src')).toBe(contentUrl)
    // 隔离与预览同级：内容落在不透明源上，读不到主站的 cookie 与本地存储。
    const sandbox = iframe?.getAttribute('sandbox') ?? ''
    expect(sandbox).toContain('allow-scripts')
    expect(sandbox).not.toContain('allow-same-origin')
  })

  // **水印叠在壳上、不进行内。** 明水印是给人看的威慑，盲水印是给命令行读的取证；
  // 两者都在 iframe **之外**的一层上（见 docs/design/galaxy/watermark.md 的"铺在哪"）。
  // 页面里的层叠绕不到它，`site` 槽的产物也因此一个字节都没变。
  it('水印叠在 iframe 之上，不在页内', async () => {
    const contentUrl = 'https://pub.example.com/g/p1'
    vi.mocked(galaxyApi.resolveSharedPage).mockResolvedValue(
      create(ResolveSharedPageResponseSchema, {
        contentUrl,
        watermark: create(WatermarkLayerSchema, {
          displayText: 'aladdin · 2026-10-18 · ABCD1234',
          pattern: new Uint8Array([0x89, 0x50, 0x4e, 0x47]),
        }),
      }),
    )

    const container = await renderAt('/g/p1')

    const overlay = container.querySelector('[data-testid="watermark-overlay"]')
    expect(overlay, '没有渲染出水印层').not.toBeNull()
    // 它在 iframe 之外：不在那一帧里，而是它的兄弟。
    const iframe = container.querySelector('iframe')
    expect(iframe).not.toBeNull()
    expect(overlay?.contains(iframe)).toBe(false)
    // 明水印那一行字出现在页面上，但它不是内容的一部分。
    expect(overlay?.textContent).toContain('ABCD1234')
    expect(overlay?.querySelector('[data-testid="watermark-pattern"]')).not.toBeNull()
    // **不接指针**：水印盖在正文上，点击、滚动、选中都该落到内容上。
    expect((overlay as HTMLElement).style.pointerEvents).toBe('none')
    // **不进无障碍树**：它是标记，不是内容。
    expect(overlay?.getAttribute('aria-hidden')).toBe('true')
  })

  // 没有水印时这一层不渲染：一个空的绝对定位层只会多出一个挡住指针的风险，
  // 而它一个像素的信息都没有。
  it('解析结果里没有水印时不渲染水印层', async () => {
    vi.mocked(galaxyApi.resolveSharedPage).mockResolvedValue(
      create(ResolveSharedPageResponseSchema, {
        contentUrl: 'https://pub.example.com/g/p1',
      }),
    )

    const container = await renderAt('/g/p1')

    expect(container.querySelector('[data-testid="watermark-overlay"]')).toBeNull()
  })

  // 空结果**是统一的否定结论**（未发布 / 已撤回 / 槽未启用 / 工程不存在 /
  // 标识没被猜中 / 路径不在集合），不是错误：壳据此渲染"页面不存在"，不起 iframe。
  it('空结果渲染"页面不存在"，不起 iframe', async () => {
    vi.mocked(galaxyApi.resolveSharedPage).mockResolvedValue(
      create(ResolveSharedPageResponseSchema, { contentUrl: '' }),
    )

    const container = await renderAt('/g/p1')

    expect(container.textContent).toContain('页面不存在')
    expect(container.querySelector('iframe')).toBeNull()
  })

  // 取不到解析结果与"这一页不存在"是两件事：前者可重试，后者是结论。
  it('解析失败给出错误与重试', async () => {
    vi.mocked(galaxyApi.resolveSharedPage).mockRejectedValue(new Error('网络不可达'))

    const container = await renderAt('/g/p1')

    expect(container.textContent).toContain('网络不可达')
    expect(container.querySelector('iframe')).toBeNull()
    // antd 在按钮文字的汉字之间插空格，因此按去空格后的文本比。
    expect(container.textContent?.replace(/\s/g, '')).toContain('重试')
  })
})
