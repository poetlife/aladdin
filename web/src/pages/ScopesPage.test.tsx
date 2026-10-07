import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as identityApi from '../api/identity'
import * as rbacApi from '../api/rbac'
import { Code, ConnectError } from '../api/errors'
import { SessionProvider } from '../auth'
import { ScopesProvider } from '../rbac'
import { ThemeProvider } from '../theme'
import { ScopesPage } from './ScopesPage'

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

async function renderPage(permissions: string[]): Promise<HTMLElement> {
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
    permissions,
  })

  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <ThemeProvider>
        <SessionProvider>
          {/* 范围目录由外壳持有；单独渲染这一页时补上它，与真实装配一致。 */}
          <ScopesProvider>
            <ScopesPage />
          </ScopesProvider>
        </SessionProvider>
      </ThemeProvider>,
    )
  })
  await act(async () => {})
  return container
}

/** 去掉空白再比：antd 会在两个汉字之间插一个空格（「删除」渲染成「删 除」）。 */
function buttonByText(container: HTMLElement, text: string): HTMLButtonElement | undefined {
  return [...container.querySelectorAll('button')].find((b) =>
    (b.textContent ?? '').replace(/\s+/g, '').includes(text),
  )
}

/** 往受控输入框里写值：直接改 value 不会触发 React 的 onChange。 */
function typeInto(input: Element, value: string): void {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
  setter?.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

function scopeResponse(scopes: { path: string; displayName: string }[]) {
  return {
    $typeName: 'aladdin.rbac.v1.ListScopesResponse' as const,
    scopes: scopes.map((s) => ({
      $typeName: 'aladdin.rbac.v1.Scope' as const,
      path: s.path,
      displayName: s.displayName,
    })),
  }
}

const READ_ONLY = ['rbac.scope.read']
const READ_WRITE = ['rbac.scope.read', 'rbac.scope.write']

beforeEach(() => {
  globalThis.localStorage?.clear()
  vi.mocked(rbacApi.listScopes).mockResolvedValue(
    scopeResponse([{ path: 'tenant/acme', displayName: 'Acme 事业部' }]),
  )
})

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
  vi.clearAllMocks()
})

describe('范围页', () => {
  it('列出已登记的范围', async () => {
    const container = await renderPage(READ_ONLY)

    expect(rbacApi.listScopes).toHaveBeenCalledWith('tenant/acme')
    expect(container.textContent).toContain('tenant/acme')
    expect(container.textContent).toContain('Acme 事业部')
  })

  // 能不能删由服务端说了算（"仍被 N 条绑定引用"），界面把那条理由原样呈现出来，
  // 不自己判断——界面看到的是可能已经过期的视图。
  it('删除被引用的范围时，把服务端的理由原样呈现', async () => {
    vi.mocked(rbacApi.deleteScope).mockRejectedValue(
      new ConnectError('范围仍被使用: "tenant/acme" 仍被 2 条绑定引用', Code.FailedPrecondition),
    )

    const container = await renderPage(READ_WRITE)
    buttonByText(container, '删除')?.click()
    // 二次确认之后才真的删。
    await act(async () => {})
    const confirm = [...document.querySelectorAll('.ant-popconfirm button')].find((b) =>
      (b.textContent ?? '').replace(/\s+/g, '').includes('删除'),
    )
    expect(confirm, '确认框里没有「删除」').not.toBeUndefined()
    await act(async () => {
      ;(confirm as HTMLButtonElement).click()
    })

    expect(rbacApi.deleteScope).toHaveBeenCalledWith('tenant/acme', 'tenant/acme')
    expect(container.textContent).toContain('仍被 2 条绑定引用')
  })

  it('登记一个范围后重新拉取列表', async () => {
    vi.mocked(rbacApi.putScope).mockResolvedValue({
      $typeName: 'aladdin.rbac.v1.PutScopeResponse',
      scope: { $typeName: 'aladdin.rbac.v1.Scope', path: 'tenant/other', displayName: '' },
    })

    const container = await renderPage(READ_WRITE)
    const pathInput = container.querySelector('#path')
    expect(pathInput, '没有找到路径输入框').not.toBeNull()
    typeInto(pathInput as Element, 'tenant/other')

    await act(async () => {
      buttonByText(container, '登记')?.click()
    })
    await act(async () => {})

    expect(rbacApi.putScope).toHaveBeenCalledWith('tenant/acme', 'tenant/other', '')
    // 初始一次 + 登记后一次。
    expect(vi.mocked(rbacApi.listScopes).mock.calls.length).toBe(2)
  })

  // 控件级裁剪不渲染而不是留一列灰按钮。
  it('没有写权限时不出现登记表单与操作列', async () => {
    const container = await renderPage(READ_ONLY)

    expect(container.textContent).toContain('你没有登记范围的权限')
    expect(buttonByText(container, '删除')).toBeUndefined()
    expect(buttonByText(container, '登记')).toBeUndefined()
  })
})
