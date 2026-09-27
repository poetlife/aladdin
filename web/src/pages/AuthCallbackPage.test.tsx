import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as identityApi from '../api/identity'
import { SessionProvider } from '../auth'
import { AuthCallbackPage } from './AuthCallbackPage'

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

  it('带失败标记时给出提示，且不写入任何凭证', async () => {
    const container = await renderCallbackPage('#error=github_login_failed')

    expect(container.textContent).toContain('登录未完成')
    expect(globalThis.localStorage?.getItem('aladdin.token')).toBeNull()
    expect(container.querySelector('[data-testid="home"]')).toBeNull()
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

  it('地址里什么都没有时明确说明这不是回调地址，而不是永远转圈', async () => {
    const container = await renderCallbackPage('')

    expect(container.textContent).toContain('不是登录回调')
    expect(globalThis.localStorage?.getItem('aladdin.token')).toBeNull()
  })
})
