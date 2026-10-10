import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as identityApi from '../api/identity'
import { RegistrationMode } from '../gen/proto/aladdin/identity/v1/registration_pb'
import { SessionProvider } from '../auth'
import { ThemeProvider } from '../theme'
import { LoginPage } from './LoginPage'

vi.mock('../api/identity', () => ({
  AuthSource: { Google: 'google', Github: 'github' },
  getAuthOptions: vi.fn(),
  login: vi.fn(),
  whoAmI: vi.fn(),
  getSessionPermissions: vi.fn(),
}))

vi.mock('../api/transport', () => ({
  onUnauthenticated: vi.fn(),
}))

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const GOOGLE_LINK = 'a[href="/auth/google/start"]'
const GITHUB_LINK = 'a[href="/auth/github/start"]'
const TOKEN_INPUT = 'input[placeholder="机器凭证或访问令牌"]'

/** 造一个下发的渠道。LoginPage 只读 source——所有渠道都是重定向型，前端不需要客户端标识。 */
function method(source: string): identityApi.AuthMethod {
  return { $typeName: 'aladdin.identity.v1.AuthMethod', source }
}

/**
 * 造一份登录选项。缺省姿态是开放注册——它是服务端的零值，也是这一页最常遇到的
 * 取值；准入姿态各自的措辞由单独的用例覆盖。
 */
function authOptions(
  methods: identityApi.AuthMethod[],
  mode: RegistrationMode = RegistrationMode.OPEN,
): identityApi.GetAuthMethodsResponse {
  return {
    $typeName: 'aladdin.identity.v1.GetAuthMethodsResponse',
    methods,
    deviceLoginEnabled: false,
    registrationMode: mode,
  }
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
    vi.mocked(identityApi.getAuthOptions).mockResolvedValue(authOptions([]))

    const container = await renderLoginPage()

    expect(container.querySelector(GOOGLE_LINK)).toBeNull()
    expect(container.querySelector(GITHUB_LINK)).toBeNull()
    // 未启用不等于页面不可用：令牌登录路径仍在。
    expect(container.querySelector(TOKEN_INPUT)).not.toBeNull()
  })

  it('服务端下发 Google 时渲染一个指向起点端点的链接，而不是浏览器内控件', async () => {
    vi.mocked(identityApi.getAuthOptions).mockResolvedValue(authOptions([method('google')]))

    const container = await renderLoginPage()

    // 它必须是一个指向服务端起点端点的链接：换码与校验都在服务端完成，
    // 浏览器不经手任何可自证身份的凭证。
    const link = container.querySelector(GOOGLE_LINK)
    expect(link).not.toBeNull()
    expect(link?.tagName).toBe('A')
    // 页面不加载任何第三方脚本或控件：登录入口的观感完全由本站主题决定。
    expect(container.querySelector('script, iframe')).toBeNull()
    expect(container.querySelector(GITHUB_LINK)).toBeNull()
  })

  it('服务端下发 GitHub 时渲染一个整页跳转的入口，而不是 RPC 按钮', async () => {
    vi.mocked(identityApi.getAuthOptions).mockResolvedValue(authOptions([method('github')]))

    const container = await renderLoginPage()

    // 它必须是一个指向服务端起点端点的链接：GitHub 的授权码要由服务端
    // 用客户端密钥换取，浏览器给不出可用的码。
    expect(container.querySelector(GITHUB_LINK)).not.toBeNull()
    expect(container.querySelector(GOOGLE_LINK)).toBeNull()
  })

  it('查询登录方式失败时退回只有令牌登录，而不是整页不可用', async () => {
    vi.mocked(identityApi.getAuthOptions).mockRejectedValue(new Error('boom'))

    const container = await renderLoginPage()

    expect(container.querySelector(GOOGLE_LINK)).toBeNull()
    expect(container.querySelector(GITHUB_LINK)).toBeNull()
    expect(container.querySelector(TOKEN_INPUT)).not.toBeNull()
  })
})

describe('登录页的准入姿态说明', () => {
  it('需要邀请码时说清楚，而不是等人点进去才发现', async () => {
    vi.mocked(identityApi.getAuthOptions).mockResolvedValue(
      authOptions([method('google')], RegistrationMode.INVITE),
    )

    const container = await renderLoginPage()

    expect(container.textContent).toContain('邀请码')
  })

  it('不接受新账号时说清楚，且点明在册账号不受影响', async () => {
    vi.mocked(identityApi.getAuthOptions).mockResolvedValue(
      authOptions([method('google')], RegistrationMode.CLOSED),
    )

    const container = await renderLoginPage()

    expect(container.textContent).toContain('不接受新账号')
    expect(container.textContent).toContain('已经在册的账号')
  })

  it('开放注册时不写准入那句话，但仍说清新账号是零权限', async () => {
    vi.mocked(identityApi.getAuthOptions).mockResolvedValue(
      authOptions([method('google')], RegistrationMode.OPEN),
    )

    const container = await renderLoginPage()

    expect(container.textContent).not.toContain('不接受新账号')
    expect(container.textContent).not.toContain('本站需要')
    // "新账号默认什么权限都没有"这句在三种姿态下都得在：第一个接入的人最容易
    // 把它读成"登录坏了"。
    expect(container.textContent).toContain('没有任何权限')
  })

  it('读不到策略时什么都不说，而不是替服务端猜一个姿态', async () => {
    vi.mocked(identityApi.getAuthOptions).mockResolvedValue(
      authOptions([method('google')], RegistrationMode.UNSPECIFIED),
    )

    const container = await renderLoginPage()

    // 渠道入口照常在：说不了准入不等于不能登录。
    expect(container.querySelector(GOOGLE_LINK)).not.toBeNull()
    expect(container.textContent).not.toContain('不接受新账号')
    expect(container.textContent).not.toContain('本站需要')
  })
})
