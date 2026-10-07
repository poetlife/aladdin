import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as identityApi from '../api/identity'
import * as rbacApi from '../api/rbac'
import { SessionProvider } from '../auth'
import { ThemeProvider } from '../theme'
import { RolesPage } from './RolesPage'

vi.mock('../api/identity', () => ({
  AuthSource: { Google: 'google', Github: 'github' },
  getAuthMethods: vi.fn(),
  login: vi.fn(),
  whoAmI: vi.fn(),
  getSessionPermissions: vi.fn(),
}))

vi.mock('../api/transport', () => ({
  onUnauthenticated: vi.fn(),
}))

vi.mock('../api/rbac', () => ({
  listRoles: vi.fn(),
  listSubjectBindings: vi.fn(),
  assignRole: vi.fn(),
  listScopes: vi.fn(),
  putScope: vi.fn(),
  deleteScope: vi.fn(),
}))

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

async function renderPage(): Promise<HTMLElement> {
  globalThis.localStorage.setItem('aladdin.token', 'tok')
  globalThis.localStorage.setItem('aladdin.scope', 'tenant/acme')
  vi.mocked(identityApi.whoAmI).mockResolvedValue({
    $typeName: 'aladdin.identity.v1.WhoAmIResponse',
    subjectId: 'u1',
    subjectType: 'user',
    defaultScope: 'tenant/acme',
  })
  vi.mocked(identityApi.getSessionPermissions).mockResolvedValue({
    $typeName: 'aladdin.identity.v1.GetSessionPermissionsResponse',
    scope: 'tenant/acme',
    // 全部 rbac 权限都持有：这条要证明「发布变更」的消失不是因为权限不够。
    permissions: ['rbac.role.read', 'rbac.role.write', 'rbac.policy.publish'],
  })

  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <ThemeProvider>
        <SessionProvider>
          <RolesPage />
        </SessionProvider>
      </ThemeProvider>,
    )
  })
  await act(async () => {})
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
  vi.clearAllMocks()
})

describe('角色定义页', () => {
  it('列出全库角色，并说明它不随管理范围过滤', async () => {
    vi.mocked(rbacApi.listRoles).mockResolvedValue({
      $typeName: 'aladdin.rbac.v1.ListRolesResponse',
      roles: [
        {
          $typeName: 'aladdin.rbac.v1.Role',
          id: 'viewer',
          displayName: '只读用户',
          permissions: ['rbac.role.read'],
          inherits: [],
          mutuallyExclusiveWith: [],
          builtin: true,
        },
      ],
    })

    const container = await renderPage()

    expect(rbacApi.listRoles).toHaveBeenCalledWith('tenant/acme')
    expect(container.textContent).toContain('viewer')
    expect(container.textContent).toContain('不随顶栏的管理范围过滤')
  })

  // 「发布变更」名实不符：服务端的 PublishPolicy 目前是空实现（失效条目恒为 0），
  // 叫"发布"实际只是重新读取。名实不符的按钮比没有按钮更糟，因此它必须不存在
  // ——即便调用方持有 rbac.policy.publish。
  it('不出现「发布变更」', async () => {
    vi.mocked(rbacApi.listRoles).mockResolvedValue({
      $typeName: 'aladdin.rbac.v1.ListRolesResponse',
      roles: [],
    })

    const container = await renderPage()

    expect(container.textContent).not.toContain('发布变更')
    expect(container.textContent).toContain('尚未定义任何角色')
  })
})
