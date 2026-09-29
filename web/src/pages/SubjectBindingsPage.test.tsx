import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as identityApi from '../api/identity'
import * as rbacApi from '../api/rbac'
import { Code, ConnectError } from '../api/errors'
import { SessionProvider } from '../auth'
import { ThemeProvider } from '../theme'
import { SubjectBindingsPage } from './SubjectBindingsPage'

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

const TOKEN_KEY = 'aladdin.token'
const SCOPE_KEY = 'aladdin.scope'

async function renderPage(permissions: string[]): Promise<HTMLElement> {
  globalThis.localStorage.setItem(TOKEN_KEY, 'tok')
  globalThis.localStorage.setItem(SCOPE_KEY, 'tenant/acme')
  vi.mocked(identityApi.whoAmI).mockResolvedValue({
    $typeName: 'aladdin.identity.v1.WhoAmIResponse',
    subjectId: 'u1',
    subjectType: 'user',
    defaultScope: 'tenant/acme',
  })
  vi.mocked(identityApi.getSessionPermissions).mockResolvedValue({
    $typeName: 'aladdin.identity.v1.GetSessionPermissionsResponse',
    scope: 'tenant/acme',
    permissions,
  })

  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <ThemeProvider>
        <SessionProvider>
          <MemoryRouter>
            <SubjectBindingsPage />
          </MemoryRouter>
        </SessionProvider>
      </ThemeProvider>,
    )
  })
  // 抗住"查询自己"那段异步：挂载后它才会把表格填上。
  await act(async () => {})
  return container
}

function buttonByText(container: HTMLElement, text: string): HTMLButtonElement | undefined {
  // 去掉空白再比：antd 的 Button 会在两个汉字之间插一个空格（「授予」渲染成「授 予」），
  // 直接比对文本会找不到按钮。
  return [...container.querySelectorAll('button')].find((b) =>
    (b.textContent ?? '').replace(/\s+/g, '').includes(text),
  )
}

const READ_ONLY = ['rbac.subject.read']
const READ_WRITE = ['rbac.subject.read', 'rbac.subject.assign']

beforeEach(() => {
  globalThis.localStorage?.clear()
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
})

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
  vi.clearAllMocks()
})

describe('人员授权页', () => {
  // 多数时候管理员要做的第一件事就是核对自己现在有什么，因此进来就查自己；
  // 而"全局"必须显示成「全局」，不能是空串或内部写法。
  it('进来先查当前主体，绑定里的全局显示为「全局」', async () => {
    vi.mocked(rbacApi.listSubjectBindings).mockResolvedValue({
      $typeName: 'aladdin.rbac.v1.ListSubjectBindingsResponse',
      bindings: [
        {
          $typeName: 'aladdin.rbac.v1.RoleBinding',
          subjectId: 'u1',
          roleId: 'viewer',
          scope: '',
        },
      ],
      effectivePermissions: ['rbac.role.read'],
    })

    const container = await renderPage(READ_ONLY)

    expect(rbacApi.listSubjectBindings).toHaveBeenCalledWith('tenant/acme', 'u1')
    expect(container.textContent).toContain('viewer')
    expect(container.textContent).toContain('只读用户')
    expect(container.textContent).toContain('全局')
    expect(container.textContent).toContain('展开后的权限（1）')
  })

  // 服务端没有"主体标识 → 可读名字"的反查，也不容许给不存在的标识写绑定
  // （那会留下一条谁也认领不了的悬空绑定），因此这条要说明原因并禁掉授予。
  it('主体不存在时说明原因，并禁掉授予', async () => {
    vi.mocked(rbacApi.listSubjectBindings).mockRejectedValue(
      new ConnectError('主体不存在: u9', Code.NotFound),
    )

    const container = await renderPage(READ_WRITE)

    expect(container.textContent).toContain('主体不存在')
    expect(container.textContent).toContain('不存在或尚未登记，无法授予')
    const grant = buttonByText(container, '授予')
    expect(grant, '没有找到「授予」按钮').not.toBeUndefined()
    expect(grant?.disabled).toBe(true)
  })

  it('查到主体后可以授予', async () => {
    vi.mocked(rbacApi.listSubjectBindings).mockResolvedValue({
      $typeName: 'aladdin.rbac.v1.ListSubjectBindingsResponse',
      bindings: [],
      effectivePermissions: [],
    })

    const container = await renderPage(READ_WRITE)

    const grant = buttonByText(container, '授予')
    expect(grant, '没有找到「授予」按钮').not.toBeUndefined()
    expect(grant?.disabled).toBe(false)
  })

  // 控件级裁剪不渲染而不是留一列灰按钮：没有授予权限的人不需要看到"回收"。
  it('没有授予权限时，回收那列不出现', async () => {
    vi.mocked(rbacApi.listSubjectBindings).mockResolvedValue({
      $typeName: 'aladdin.rbac.v1.ListSubjectBindingsResponse',
      bindings: [
        {
          $typeName: 'aladdin.rbac.v1.RoleBinding',
          subjectId: 'u1',
          roleId: 'viewer',
          scope: '',
        },
      ],
      effectivePermissions: [],
    })

    const container = await renderPage(READ_ONLY)

    expect(container.textContent).toContain('viewer')
    expect(buttonByText(container, '回收')).toBeUndefined()
    expect(container.textContent).toContain('你没有授予角色的权限')
  })

  it('有授予权限时，回收按钮出现', async () => {
    vi.mocked(rbacApi.listSubjectBindings).mockResolvedValue({
      $typeName: 'aladdin.rbac.v1.ListSubjectBindingsResponse',
      bindings: [
        {
          $typeName: 'aladdin.rbac.v1.RoleBinding',
          subjectId: 'u1',
          roleId: 'viewer',
          scope: '',
        },
      ],
      effectivePermissions: [],
    })

    const container = await renderPage(READ_WRITE)

    expect(buttonByText(container, '回收')).not.toBeUndefined()
  })
})
