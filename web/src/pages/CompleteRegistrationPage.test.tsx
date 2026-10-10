import { Code, ConnectError } from '@connectrpc/connect'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as identityApi from '../api/identity'
import * as registrationApi from '../api/registration'
import { SessionProvider } from '../auth'
import { ThemeProvider } from '../theme'
import { CompleteRegistrationPage } from './CompleteRegistrationPage'

vi.mock('../api/registration', () => ({
  completeRegistration: vi.fn(),
}))

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

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const CODE_INPUT = 'input[placeholder="XXXX-XXXX-XXXX-XXXX"]'

let root: Root | null = null

async function renderPage(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <ThemeProvider>
        <MemoryRouter initialEntries={['/register']}>
          <SessionProvider>
            <Routes>
              <Route path="/register" element={<CompleteRegistrationPage />} />
              <Route path="/" element={<div data-testid="home" />} />
            </Routes>
          </SessionProvider>
        </MemoryRouter>
      </ThemeProvider>,
    )
  })
  return container
}

/** 填码并提交。 */
async function submit(container: HTMLElement, code: string): Promise<void> {
  const input = container.querySelector<HTMLInputElement>(CODE_INPUT)
  if (input === null) {
    throw new Error('没找到邀请码输入框')
  }
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
    setter?.call(input, code)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
  await act(async () => {
    container.querySelector('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  })
}

beforeEach(() => {
  globalThis.localStorage?.clear()
  vi.mocked(identityApi.whoAmI).mockResolvedValue({
    $typeName: 'aladdin.identity.v1.WhoAmIResponse',
    subjectId: 'usr_new',
    subjectType: 'user',
    defaultScope: '',
  })
  vi.mocked(identityApi.getSessionPermissions).mockResolvedValue({
    $typeName: 'aladdin.identity.v1.GetSessionPermissionsResponse',
    scope: '',
    permissions: [],
  })
})

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
  vi.resetAllMocks()
})

describe('填邀请码那一页', () => {
  it('把邀请码交给服务端，拿到会话后落到首页', async () => {
    vi.mocked(registrationApi.completeRegistration).mockResolvedValue({
      $typeName: 'aladdin.identity.v1.CompleteRegistrationResponse',
      accessToken: 'a-new-session',
      expiresAt: '2026-10-17T00:00:00Z',
    })

    const container = await renderPage()
    await submit(container, 'ABCD-EFGH-JKMN-PQRS')

    // 请求里**只有码**：要登记哪个渠道身份只由服务端在回调时记下的那份一次性
    // 凭据决定，这一页没有主体、也没有渠道身份可以给。
    expect(registrationApi.completeRegistration).toHaveBeenCalledWith('ABCD-EFGH-JKMN-PQRS')
    expect(globalThis.localStorage?.getItem('aladdin.token')).toBe('a-new-session')
    expect(container.querySelector('[data-testid="home"]')).not.toBeNull()
  })

  it('码不对时说清楚是码的问题，并留在这一页让人改', async () => {
    vi.mocked(registrationApi.completeRegistration).mockRejectedValue(
      new ConnectError('邀请码无效或已用尽，请向管理员索取一个新的', Code.FailedPrecondition),
    )

    const container = await renderPage()
    await submit(container, 'WRONG-CODE-XXXX-YYYY')

    expect(container.textContent).toContain('邀请码无效')
    expect(globalThis.localStorage?.getItem('aladdin.token')).toBeNull()
    // 不跳走：码多半只是打错了，重新登录一遍是多余的摩擦。
    expect(container.querySelector(CODE_INPUT)).not.toBeNull()
  })

  it('这次注册已过期时提示重新登录', async () => {
    vi.mocked(registrationApi.completeRegistration).mockRejectedValue(
      new ConnectError('这次注册已经过期或已经完成，请重新登录一次', Code.FailedPrecondition),
    )

    const container = await renderPage()
    await submit(container, 'ABCD-EFGH-JKMN-PQRS')

    expect(container.textContent).toContain('重新登录')
  })
})
