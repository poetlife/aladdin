import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as identityApi from '../api/identity'
import { SessionProvider } from '../auth'
import { LoginPage } from './LoginPage'

vi.mock('../api/identity', () => ({
  getAuthMethods: vi.fn(),
  login: vi.fn(),
  loginWithGoogle: vi.fn(),
  whoAmI: vi.fn(),
  getSessionPermissions: vi.fn(),
}))

vi.mock('../api/transport', () => ({
  onUnauthenticated: vi.fn(),
}))

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const GOOGLE_BUTTON = '[data-testid="google-sign-in"]'
const TOKEN_INPUT = 'input[placeholder="机器凭证或访问令牌"]'

let root: Root | null = null

async function renderLoginPage(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <MemoryRouter>
        <SessionProvider>
          <LoginPage />
        </SessionProvider>
      </MemoryRouter>,
    )
  })
  return container
}

beforeEach(() => {
  globalThis.localStorage?.clear()
})

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
})

describe('登录页的登录入口', () => {
  it('服务端未启用 Google 登录时不渲染入口', async () => {
    vi.mocked(identityApi.getAuthMethods).mockResolvedValue({
      $typeName: 'aladdin.identity.v1.GetAuthMethodsResponse',
      googleClientId: '',
    })

    const container = await renderLoginPage()

    expect(container.querySelector(GOOGLE_BUTTON)).toBeNull()
    // 未启用不等于页面不可用：令牌登录路径仍在。
    expect(container.querySelector(TOKEN_INPUT)).not.toBeNull()
  })

  it('服务端下发客户端标识时渲染 Google 入口', async () => {
    vi.mocked(identityApi.getAuthMethods).mockResolvedValue({
      $typeName: 'aladdin.identity.v1.GetAuthMethodsResponse',
      googleClientId: 'example.apps.googleusercontent.com',
    })

    const container = await renderLoginPage()

    expect(container.querySelector(GOOGLE_BUTTON)).not.toBeNull()
  })

  it('查询登录方式失败时退回只有令牌登录，而不是整页不可用', async () => {
    vi.mocked(identityApi.getAuthMethods).mockRejectedValue(new Error('boom'))

    const container = await renderLoginPage()

    expect(container.querySelector(GOOGLE_BUTTON)).toBeNull()
    expect(container.querySelector(TOKEN_INPUT)).not.toBeNull()
  })
})
