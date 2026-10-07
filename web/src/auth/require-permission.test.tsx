import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as identityApi from '../api/identity'
import { PermissionCodes } from '../gen/permission-codes'
import type { PermissionCode } from '../gen/permission-codes'
import { LOADING_TEXT } from '../ui/LoadingHint'
import { SessionProvider } from './session'
import { RequirePermission } from './require-permission'

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

let root: Root | null = null

/** 把一段受保护内容挂在准入守卫之下。 */
async function mountGuard(require?: readonly PermissionCode[]): Promise<HTMLElement> {
  // 只在真的给出时才传这个属性：`require` 的类型是"可省略"，而不是"可以显式传
  // undefined"，而 tsconfig 开了 exactOptionalPropertyTypes 会把这两者分开。
  const guard = require === undefined ? <RequirePermission /> : <RequirePermission require={require} />

  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <SessionProvider>
        <MemoryRouter initialEntries={['/']}>
          <Routes>
            <Route path="/login" element={<p>登录页</p>} />
            <Route path="/forbidden" element={<p>无权限页</p>} />
            <Route element={guard}>
              <Route path="/" element={<p>受保护内容</p>} />
            </Route>
          </Routes>
        </MemoryRouter>
      </SessionProvider>,
    )
  })
  return container
}

/** 让会话进入"已认证、且一个权限都没有"的状态。 */
function mockZeroPermissionSession(): void {
  globalThis.localStorage.setItem(TOKEN_KEY, 'tok')
  globalThis.localStorage.setItem(SCOPE_KEY, '')
  vi.mocked(identityApi.whoAmI).mockResolvedValue({
    $typeName: 'aladdin.identity.v1.WhoAmIResponse',
    subjectId: 'usr_abc',
    subjectType: 'user',
    defaultScope: '',
  })
  vi.mocked(identityApi.getSessionPermissions).mockResolvedValue({
    $typeName: 'aladdin.identity.v1.GetSessionPermissionsResponse',
    scope: '',
    permissions: [],
  })
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

describe('路由准入', () => {
  // 上一个版本里，整个外壳（含首页）都挂在一层 rbac 权限门禁之下，于是
  // **零权限主体登录后看到的是 403**。这与"首次登录是成功的，界面是空的"
  // 直接矛盾，也让他连自己的昵称都设不了。
  it('未声明基础权限时，零权限主体也能进入', async () => {
    mockZeroPermissionSession()

    const container = await mountGuard()

    expect(container.textContent).toContain('受保护内容')
  })

  it('声明了基础权限而主体不持有时，进无权限页', async () => {
    mockZeroPermissionSession()

    const container = await mountGuard([PermissionCodes.RbacRoleRead])

    expect(container.textContent).toContain('无权限页')
    expect(container.textContent).not.toContain('受保护内容')
  })

  // 未认证与无权限必须走向不同的地方：都跳首页会让"看起来像点击失灵"。
  it('未认证时进登录页', async () => {
    const container = await mountGuard()

    expect(container.textContent).toContain('登录页')
  })

  // **打开主站的第一眼。** 会话还没问出来之前这一页整块是空的（连外壳都还没渲染），
  // 那一段时间不能只有一颗贴在左上角的裸转圈——既不像在加载，也不像坏了。
  it('会话判定期间给出加载提示', async () => {
    globalThis.localStorage.setItem(TOKEN_KEY, 'tok')
    vi.mocked(identityApi.whoAmI).mockReturnValue(new Promise<never>(() => {}))

    const container = await mountGuard()

    expect(container.textContent).toContain(LOADING_TEXT)
    expect(container.textContent).not.toContain('受保护内容')
  })
})
