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

  // **水印要压在加载遮罩之上。** 遮罩最长的寿命是加载那十秒，而盲水印要被截的是
  // 内容：一块被遮罩盖住的地方截下来就是一片纯色，提不出任何东西。实测的后果很
  // 具体——加载中途的截图只有下半有图案、上半是纯色，整张图因此解不出。
  //
  // 判据是**层叠顺序**而不是"看起来对"：水印层不带 z-index，遮罩层也不带，
  // 靠 DOM 顺序决定（水印在后）。这里钉住这条顺序，改动它必须是有意的。
  it('水印压在加载遮罩之上，而不是被它盖住', async () => {
    vi.mocked(galaxyApi.resolveSharedPage).mockResolvedValue(
      create(ResolveSharedPageResponseSchema, {
        contentUrl: 'https://pub.example.com/g/p1',
        watermark: create(WatermarkLayerSchema, {
          displayText: 'aladdin · 2026-10-18 · ABCD1234',
          pattern: new Uint8Array([0x89, 0x50, 0x4e, 0x47]),
        }),
      }),
    )

    const container = await renderAt('/g/p1')

    // 加载遮罩此刻在场（iframe 还没 load）。
    const overlay = container.querySelector('[data-testid="watermark-overlay"]') as HTMLElement | null
    expect(overlay, '没有渲染出水印层').not.toBeNull()
    // 水印层不自己抬层级：抬了就会把遮罩顶下去，或者反过来被遮罩顶下去，
    // 两种都由 DOM 顺序统一决定。
    expect(overlay?.style.zIndex).toBe('1')
  })

  // **盲水印缺席是常态**：它由工程自己的开关控制、默认关闭。关着时服务端只下发
  // `displayText`，壳上就只剩明水印那行字——不该因为"没有图案"而整层都不渲染
  // （那样连明水印也没了），也不该报错。
  it('只有明水印、没有图案时仍渲染那一行字', async () => {
    vi.mocked(galaxyApi.resolveSharedPage).mockResolvedValue(
      create(ResolveSharedPageResponseSchema, {
        contentUrl: 'https://pub.example.com/g/p1',
        watermark: create(WatermarkLayerSchema, {
          displayText: 'aladdin · 2026-10-18 · ABCD1234',
        }),
      }),
    )

    const container = await renderAt('/g/p1')

    const overlay = container.querySelector('[data-testid="watermark-overlay"]')
    expect(overlay, '没有渲染出水印层：明水印不该受盲水印开关影响').not.toBeNull()
    expect(overlay?.textContent).toContain('ABCD1234')
    // 没有图案时那一层不出现。
    expect(overlay?.querySelector('[data-testid="watermark-pattern"]')).toBeNull()
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
