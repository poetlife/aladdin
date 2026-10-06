import { afterEach, describe, expect, it, vi } from 'vitest'

import { mountGoogleButton, type GoogleButtonOptions } from './google-identity'

interface RenderOptions {
  theme: string
  size: string
}

/**
 * 装一个假的 GIS 全局，并让它像真身那样把按钮画成 `[role="button"]`。
 *
 * `google-identity.ts` 在全局已有 `google` 时直接复用、不插脚本，于是测试
 * 不必碰网络：塞进去一个替身，`mountGoogleButton` 就会立刻走到 `renderButton`。
 */
function installFakeGis(): { initialize: ReturnType<typeof vi.fn>; renderButton: ReturnType<typeof vi.fn> } {
  const initialize = vi.fn()
  const renderButton = vi.fn((parent: HTMLElement, options: RenderOptions) => {
    const button = document.createElement('div')
    button.setAttribute('role', 'button')
    button.dataset.theme = options.theme
    parent.appendChild(button)
  })
  ;(globalThis as { google?: unknown }).google = {
    accounts: { id: { initialize, renderButton } },
  }
  return { initialize, renderButton }
}

/** 取最近一次 renderButton 的选项。 */
function lastRenderOptions(renderButton: ReturnType<typeof vi.fn>): RenderOptions {
  const call = renderButton.mock.calls.at(-1)
  if (call === undefined) {
    throw new Error('renderButton 未被调用')
  }
  return call[1] as RenderOptions
}

function options(overrides: Partial<GoogleButtonOptions> = {}): GoogleButtonOptions {
  return {
    clientId: 'client',
    onCredential: () => {},
    scheme: 'light',
    size: 'large',
    borderRadius: 8,
    fontSize: 16,
    ...overrides,
  }
}

afterEach(() => {
  delete (globalThis as { google?: unknown }).google
})

describe('Google 登录按钮的配色', () => {
  it('亮色下用 outline 主题', async () => {
    const { renderButton } = installFakeGis()

    await mountGoogleButton(document.createElement('div'), options({ clientId: 'client-a', scheme: 'light' }))

    expect(lastRenderOptions(renderButton).theme).toBe('outline')
  })

  it('暗色下用 outline_dark 主题', async () => {
    const { renderButton } = installFakeGis()

    await mountGoogleButton(document.createElement('div'), options({ clientId: 'client-b', scheme: 'dark' }))

    expect(lastRenderOptions(renderButton).theme).toBe('outline_dark')
  })

  it('尺寸按传入的档位渲染，不由这里写死', async () => {
    const { renderButton } = installFakeGis()

    await mountGoogleButton(document.createElement('div'), options({ clientId: 'client-c', size: 'medium' }))

    expect(lastRenderOptions(renderButton).size).toBe('medium')
  })
})

describe('Google 登录按钮与站点观感对齐', () => {
  it('盖上传入的圆角与字号，并裁掉内部高亮层', async () => {
    installFakeGis()
    const parent = document.createElement('div')

    await mountGoogleButton(parent, options({ borderRadius: 8, fontSize: 16 }))

    const button = parent.querySelector<HTMLElement>('[role="button"]')
    expect(button?.style.borderRadius).toBe('8px')
    expect(button?.style.fontSize).toBe('16px')
    expect(button?.style.overflow).toBe('hidden')
  })

  it('GIS 没画出按钮时不抛错：一次第三方结构调整不该让登录页坏掉', async () => {
    const initialize = vi.fn()
    ;(globalThis as { google?: unknown }).google = {
      accounts: { id: { initialize, renderButton: vi.fn() } },
    }

    await expect(mountGoogleButton(document.createElement('div'), options())).resolves.toBeUndefined()
  })
})

describe('Google 登录按钮的挂载撤销', () => {
  it('挂载被撤销时不画按钮：避免重挂时叠出两个', async () => {
    const { renderButton } = installFakeGis()
    const controller = new AbortController()
    controller.abort()

    await mountGoogleButton(document.createElement('div'), options({ signal: controller.signal }))

    expect(renderButton).not.toHaveBeenCalled()
  })
})
