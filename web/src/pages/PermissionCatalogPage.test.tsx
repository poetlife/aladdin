import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as identityApi from '../api/identity'
import * as rbacApi from '../api/rbac'
import { Code, ConnectError } from '../api/errors'
import { SessionProvider } from '../auth'
import type { Role } from '../gen/proto/aladdin/rbac/v1/rbac_pb'
import { ThemeProvider } from '../theme'
import { PermissionCatalogPage } from './PermissionCatalogPage'

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

vi.mock('../api/rbac', () => ({
  listRoles: vi.fn(),
  listSubjectBindings: vi.fn(),
  assignRole: vi.fn(),
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
    permissions: ['rbac.role.read'],
  })

  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <ThemeProvider>
        <SessionProvider>
          <PermissionCatalogPage />
        </SessionProvider>
      </ThemeProvider>,
    )
  })
  await act(async () => {})
  return container
}

function role(id: string, displayName: string, permissions: string[]): Role {
  return {
    $typeName: 'aladdin.rbac.v1.Role',
    id,
    displayName,
    permissions,
    inherits: [],
    mutuallyExclusiveWith: [],
    builtin: true,
  }
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

describe('权限码目录页', () => {
  it('码与说明都渲染出来（来自生成的目录，不需要请求）', async () => {
    vi.mocked(rbacApi.listRoles).mockResolvedValue({
      $typeName: 'aladdin.rbac.v1.ListRolesResponse',
      roles: [],
    })

    const container = await renderPage()

    // 码与说明来自 web/src/gen/permission-catalog.ts（唯一信源是 catalog.yaml）。
    expect(container.textContent).toContain('rbac.role.read')
    expect(container.textContent).toContain('读取角色定义与角色列表')
    expect(container.textContent).toContain('galaxy.project.publish')
  })

  // 角色那一列只是返回数据上的**字面**成员测试：通配与继承的展开是服务端的活，
  // 前端不重复实现（见 docs/design/rbac/frontend-permissions.md）。
  it('角色列只列直接声明该码的角色', async () => {
    vi.mocked(rbacApi.listRoles).mockResolvedValue({
      $typeName: 'aladdin.rbac.v1.ListRolesResponse',
      roles: [role('viewer', '只读用户', ['rbac.role.read']), role('system.admin', '系统管理员', ['*'])],
    })

    const container = await renderPage()

    expect(container.textContent).toContain('只读用户')
    expect(container.textContent).toContain('系统管理员')
    // 边界要写在界面上：声明「直接声明」而不是「只有这些角色」。
    expect(container.textContent).toContain('直接声明')
    expect(container.textContent).toContain('不在这里展开')
  })

  // 角色列表读不到时，目录本身照常可读——把一次读取失败说成"没有角色持有它"
  // 是个错误结论，因此那一列留空并说明原因。
  it('角色列表读不到时仍能读目录，并说明只有那一列列不出来', async () => {
    vi.mocked(rbacApi.listRoles).mockRejectedValue(
      new ConnectError('权限不足', Code.PermissionDenied),
    )

    const container = await renderPage()

    expect(container.textContent).toContain('rbac.role.read')
    expect(container.textContent).toContain('读取角色定义与角色列表')
    expect(container.textContent).toContain('只有「谁持有」这一列列不出来')
  })
})
