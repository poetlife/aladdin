import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as identityApi from '../api/identity'
import { SessionProvider } from '../auth'
import { ThemeProvider } from '../theme'
import { LoginPage } from './LoginPage'

vi.mock('../api/identity', () => ({
  AuthSource: { Google: 'google', Github: 'github' },
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
const GITHUB_LINK = 'a[href="/auth/github/start"]'
const TOKEN_INPUT = 'input[placeholder="机器凭证或访问令牌"]'

/** 造一个下发的渠道。LoginPage 只读 source 与 clientId。 */
function method(source: string, clientId: string): identityApi.AuthMethod {
  return { $typeName: 'aladdin.identity.v1.AuthMethod', source, clientId }
}

let root: Root | null = null

async function renderLoginPage(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <ThemeProvider>
        <MemoryRouter>
          <SessionProvider>
            <LoginPage />
          </SessionProvider>
        </MemoryRouter>
      </ThemeProvider>,
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
  it('服务端未下发任何渠道时不渲染渠道入口', async () => {
    vi.mocked(identityApi.getAuthMethods).mockResolvedValue([])

    const container = await renderLoginPage()

    expect(container.querySelector(GOOGLE_BUTTON)).toBeNull()
    expect(container.querySelector(GITHUB_LINK)).toBeNull()
    // 未启用不等于页面不可用：令牌登录路径仍在。
    expect(container.querySelector(TOKEN_INPUT)).not.toBeNull()
  })

  it('服务端下发客户端标识时渲染 Google 入口', async () => {
    vi.mocked(identityApi.getAuthMethods).mockResolvedValue([
      method('google', 'example.apps.googleusercontent.com'),
    ])

    const container = await renderLoginPage()

    expect(container.querySelector(GOOGLE_BUTTON)).not.toBeNull()
    expect(container.querySelector(GITHUB_LINK)).toBeNull()
  })

  it('服务端下发 GitHub 时渲染一个整页跳转的入口，而不是 RPC 按钮', async () => {
    vi.mocked(identityApi.getAuthMethods).mockResolvedValue([method('github', 'Iv1.example')])

    const container = await renderLoginPage()

    // 它必须是一个指向服务端起点端点的链接：GitHub 的授权码要由服务端
    // 用客户端密钥换取，浏览器给不出可用的码。
    expect(container.querySelector(GITHUB_LINK)).not.toBeNull()
    expect(container.querySelector(GOOGLE_BUTTON)).toBeNull()
  })

  it('查询登录方式失败时退回只有令牌登录，而不是整页不可用', async () => {
    vi.mocked(identityApi.getAuthMethods).mockRejectedValue(new Error('boom'))

    const container = await renderLoginPage()

    expect(container.querySelector(GOOGLE_BUTTON)).toBeNull()
    expect(container.querySelector(GITHUB_LINK)).toBeNull()
    expect(container.querySelector(TOKEN_INPUT)).not.toBeNull()
  })
})
