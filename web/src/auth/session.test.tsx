import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as identityApi from '../api/identity'
import { SessionProvider, useSession } from './session'
import type { SessionState } from './session'

vi.mock('../api/identity', () => ({
  whoAmI: vi.fn(),
  getSessionPermissions: vi.fn(),
  login: vi.fn(),
  loginWithGoogle: vi.fn(),
  getAuthMethods: vi.fn(),
}))

vi.mock('../api/transport', () => ({
  onUnauthenticated: vi.fn(),
}))

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const TOKEN_KEY = 'aladdin.token'
const SCOPE_KEY = 'aladdin.scope'

let latest: SessionState | null = null
let root: Root | null = null

function Probe() {
  latest = useSession()
  return null
}

async function mountSession(): Promise<void> {
  const container = document.createElement('div')
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <SessionProvider>
        <Probe />
      </SessionProvider>,
    )
  })
}

beforeEach(() => {
  latest = null
  root = null
  globalThis.localStorage?.clear()
  vi.mocked(identityApi.whoAmI).mockResolvedValue({
    $typeName: 'aladdin.identity.v1.WhoAmIResponse',
    subjectId: 'u1',
    subjectType: 'user',
    defaultScope: 'tenant/acme',
  })
})

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  vi.clearAllMocks()
})

describe('会话的作用域', () => {
  it('本地没有作用域时采纳服务端解析出的作用域', async () => {
    globalThis.localStorage.setItem(TOKEN_KEY, 'tok')
    // 服务端在作用域留空时会回落到凭证的默认作用域，并把实际使用的那个回传。
    vi.mocked(identityApi.getSessionPermissions).mockResolvedValue({
      $typeName: 'aladdin.identity.v1.GetSessionPermissionsResponse',
      scope: 'tenant/acme',
      permissions: ['rbac.role.read'],
    })

    await mountSession()

    // 这条断言锁住的是一个真实踩过的坑：
    // 前端若丢掉服务端回传的作用域，后续按作用域过滤的接口（如 ListRoles）
    // 会带着空作用域（等于全局）发出，于是"菜单渲染出来了、点进去 403"。
    expect(latest?.scope).toBe('tenant/acme')
    expect(globalThis.localStorage.getItem(SCOPE_KEY)).toBe('tenant/acme')
  })

  it('用户显式设置过作用域时不覆盖', async () => {
    globalThis.localStorage.setItem(TOKEN_KEY, 'tok')
    globalThis.localStorage.setItem(SCOPE_KEY, 'tenant/other')
    vi.mocked(identityApi.getSessionPermissions).mockResolvedValue({
      $typeName: 'aladdin.identity.v1.GetSessionPermissionsResponse',
      scope: 'tenant/other',
      permissions: [],
    })

    await mountSession()

    expect(latest?.scope).toBe('tenant/other')
    // 显式作用域应当原样传给服务端，而不是被替换成凭证默认作用域。
    expect(identityApi.getSessionPermissions).toHaveBeenCalledWith('tenant/other')
  })

  it('权限码集合来自服务端，前端不做推导', async () => {
    globalThis.localStorage.setItem(TOKEN_KEY, 'tok')
    vi.mocked(identityApi.getSessionPermissions).mockResolvedValue({
      $typeName: 'aladdin.identity.v1.GetSessionPermissionsResponse',
      scope: 'tenant/acme',
      permissions: ['rbac.role.read', 'rbac.subject.read'],
    })

    await mountSession()

    expect(latest?.permissions.toArray()).toEqual(['rbac.role.read', 'rbac.subject.read'])
    expect(latest?.status).toBe('authenticated')
  })

  it('无凭证时进入匿名态且不请求接口', async () => {
    await mountSession()

    expect(latest?.status).toBe('anonymous')
    expect(identityApi.whoAmI).not.toHaveBeenCalled()
  })
})
