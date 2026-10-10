import { Code, ConnectError } from '@connectrpc/connect'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as identityApi from '../api/identity'
import { SessionProvider } from '../auth'
import { AuthCallbackPage } from './AuthCallbackPage'

vi.mock('../api/identity', () => ({
  AuthSource: { Google: 'google', Github: 'github' },
  completeIdentityBinding: vi.fn(),
  getAuthMethods: vi.fn(),
  login: vi.fn(),
  whoAmI: vi.fn(),
  getSessionPermissions: vi.fn(),
}))

vi.mock('../api/transport', () => ({
  onUnauthenticated: vi.fn(),
}))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

/**
 * 渲染回调页并把地址重置成给定的 hash。
 *
 * 地址上的 hash 用的是 jsdom 的 location 而不是 MemoryRouter 的——被测代码
 * 读的正是前者：打开这个页面的是一次**真实的浏览器跳转**，不是一次前端导航。
 */
async function renderCallbackPage(hash: string): Promise<HTMLElement> {
  globalThis.history.replaceState(null, '', '/login/callback' + hash)

  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <MemoryRouter initialEntries={['/login/callback']}>
        <SessionProvider>
          <Routes>
            <Route path="/login/callback" element={<AuthCallbackPage />} />
            <Route path="/" element={<div data-testid="home" />} />
            <Route path="/profile" element={<div data-testid="profile" />} />
          </Routes>
        </SessionProvider>
      </MemoryRouter>,
    )
  })
  return container
}

beforeEach(() => {
  globalThis.localStorage?.clear()
  vi.mocked(identityApi.whoAmI).mockResolvedValue({
    $typeName: 'aladdin.identity.v1.WhoAmIResponse',
    subjectId: 'subject-1',
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
  globalThis.history.replaceState(null, '', '/')
})

describe('重定向登录的回调页', () => {
  it('把 fragment 里的凭证落盘、抹掉地址里的 fragment、转到首页', async () => {
    const container = await renderCallbackPage('#token=a-session-token')

    expect(globalThis.localStorage?.getItem('aladdin.token')).toBe('a-session-token')
    // fragment 里装着可直接使用的凭证，不能让它留在地址栏与浏览器历史里。
    expect(globalThis.location.hash).toBe('')
    expect(container.querySelector('[data-testid="home"]')).not.toBeNull()
  })

  // 失败标记是"渠道前缀 + 用途后缀"，回调页只看后缀：接入新渠道时不必回来补
  // 分支。四种取值都必须被认出来，且提示里不透露失败发生在哪一步、属于哪个渠道
  // （两类失败各给一句同样的话）。
  it.each([
    ['github_login_failed', '登录未完成'],
    ['google_login_failed', '登录未完成'],
  ])('登录失败标记 %s 给出通用登录提示，且不写入任何凭证', async (marker, text) => {
    const container = await renderCallbackPage(`#error=${marker}`)

    expect(container.textContent).toContain(text)
    expect(globalThis.localStorage?.getItem('aladdin.token')).toBeNull()
    expect(container.querySelector('[data-testid="home"]')).toBeNull()
  })

  it.each([
    ['github_bind_failed', '绑定未完成'],
    ['google_bind_failed', '绑定未完成'],
  ])('绑定失败标记 %s 给出绑定提示，而不是登录提示', async (marker, text) => {
    const container = await renderCallbackPage(`#error=${marker}`)

    expect(container.textContent).toContain(text)
    expect(container.textContent).not.toContain('登录未完成')
    expect(container.querySelector('[data-testid="profile"]')).toBeNull()
  })

  // "不接受新账号"与"登录未完成"分开：前者不是操作失误，而是一条本该公开的站点
  // 事实（登录页已经从渠道清单里拿到了同一个取值）。混成一句话会让使用者去反复
  // 重试一件无论试几次都不会成功的事。
  it.each([
    ['github_registration_closed'],
    ['google_registration_closed'],
  ])('准入标记 %s 说清"不接受新账号"，而不是"登录未完成"', async (marker) => {
    const container = await renderCallbackPage(`#error=${marker}`)

    expect(container.textContent).toContain('不接受新账号')
    expect(container.textContent).not.toContain('登录未完成')
    expect(globalThis.localStorage?.getItem('aladdin.token')).toBeNull()
  })

  it('服务端不认这份凭证时给出提示，而不是静默回到登录页', async () => {
    // 凭证随跳转交回，但用不了（例如已失效）。会话层把失败收敛成"未登录"，
    // 本页必须自己确认结果，否则用户只会被弹回登录页且看不到任何解释。
    vi.mocked(identityApi.whoAmI).mockRejectedValue(new Error('凭证已失效，请重新登录'))

    const container = await renderCallbackPage('#token=a-stale-token')

    expect(container.textContent).toContain('凭证已失效')
    expect(container.querySelector('[data-testid="home"]')).toBeNull()
    // 用不了的凭证不该留在本地存储里。
    expect(globalThis.localStorage?.getItem('aladdin.token')).toBeNull()
  })

  it('把绑定回跳的 source 交给已认证兑换，并转到个人资料页', async () => {
    vi.mocked(identityApi.completeIdentityBinding).mockResolvedValue({
      $typeName: 'aladdin.identity.v1.CompleteIdentityBindingResponse',
      identities: [],
      reclaimed: false,
    })

    const container = await renderCallbackPage('#binding=github')

    expect(identityApi.completeIdentityBinding).toHaveBeenCalledWith('github')
    expect(globalThis.location.hash).toBe('')
    expect(container.querySelector('[data-testid="profile"]')).not.toBeNull()
    // 绑定不改动本地凭证：它只把身份并到当前主体上。
    expect(globalThis.localStorage?.getItem('aladdin.token')).toBeNull()
  })

  it('绑定兑换时会话已失效，提示重新登录而不是摊开未认证', async () => {
    vi.mocked(identityApi.completeIdentityBinding).mockRejectedValue(
      new ConnectError('未认证', Code.Unauthenticated),
    )

    const container = await renderCallbackPage('#binding=github')

    expect(container.textContent).toContain('登录状态已失效')
    expect(container.querySelector('[data-testid="profile"]')).toBeNull()
  })

  it('地址里什么都没有时明确说明这不是回调地址，而不是永远转圈', async () => {
    const container = await renderCallbackPage('')

    expect(container.textContent).toContain('不是登录回调')
    expect(globalThis.localStorage?.getItem('aladdin.token')).toBeNull()
  })
})
