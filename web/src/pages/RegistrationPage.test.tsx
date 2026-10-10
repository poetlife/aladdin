import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as identityApi from '../api/identity'
import * as rbacApi from '../api/rbac'
import * as registrationApi from '../api/registration'
import { SessionProvider } from '../auth'
import { ScopesProvider } from '../rbac'
import { ThemeProvider } from '../theme'
import { RegistrationMode } from '../gen/proto/aladdin/identity/v1/registration_pb'
import { RegistrationPage } from './RegistrationPage'

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

vi.mock('../api/rbac', () => ({
  listRoles: vi.fn(),
  listScopes: vi.fn(),
}))

vi.mock('../api/registration', () => ({
  RegistrationMode: {
    UNSPECIFIED: 0,
    OPEN: 1,
    INVITE: 2,
    CLOSED: 3,
  },
  getRegistrationPolicy: vi.fn(),
  putRegistrationPolicy: vi.fn(),
  listInvites: vi.fn(),
  createInvite: vi.fn(),
  revokeInvite: vi.fn(),
}))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

function policy(overrides: Partial<registrationApi.RegistrationPolicy> = {}) {
  return {
    $typeName: 'aladdin.identity.v1.RegistrationPolicy' as const,
    mode: RegistrationMode.OPEN,
    defaultRoleId: '',
    defaultScope: '',
    updatedBySubjectId: '',
    updatedAt: '',
    ...overrides,
  }
}

function invite(overrides: Partial<registrationApi.Invite> = {}) {
  return {
    $typeName: 'aladdin.identity.v1.Invite' as const,
    id: 'inv_a',
    label: '给张三',
    createdBySubjectId: 'usr_admin',
    createdAt: '2026-10-10T12:00:00Z',
    maxUses: 1,
    usedCount: 0,
    expiresAt: '',
    revokedAt: '',
    ...overrides,
  }
}

async function renderPage(permissions: string[], invites = [invite()]): Promise<HTMLElement> {
  globalThis.localStorage.setItem('aladdin.token', 'tok')
  globalThis.localStorage.setItem('aladdin.scope', '')
  vi.mocked(identityApi.whoAmI).mockResolvedValue({
    $typeName: 'aladdin.identity.v1.WhoAmIResponse',
    subjectId: 'usr_admin',
    subjectType: 'user',
    defaultScope: '',
  })
  vi.mocked(identityApi.getSessionPermissions).mockResolvedValue({
    $typeName: 'aladdin.identity.v1.GetSessionPermissionsResponse',
    scope: '',
    permissions,
  })
  vi.mocked(registrationApi.getRegistrationPolicy).mockResolvedValue(policy())
  vi.mocked(registrationApi.listInvites).mockResolvedValue(invites)
  vi.mocked(rbacApi.listRoles).mockResolvedValue({
    $typeName: 'aladdin.rbac.v1.ListRolesResponse',
    roles: [
      {
        $typeName: 'aladdin.rbac.v1.Role',
        id: 'viewer',
        displayName: '只读用户',
        builtin: true,
        permissions: [],
        inherits: [],
        mutuallyExclusiveWith: [],
      },
    ],
  })
  vi.mocked(rbacApi.listScopes).mockResolvedValue({
    $typeName: 'aladdin.rbac.v1.ListScopesResponse',
    scopes: [],
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
            <RegistrationPage />
          </ScopesProvider>
        </SessionProvider>
      </ThemeProvider>,
    )
  })
  await act(async () => {})
  return container
}

/** 去掉空白再比：antd 会在两个汉字之间插一个空格。 */
function buttonByText(container: HTMLElement, text: string): HTMLButtonElement | undefined {
  return [...container.querySelectorAll('button')].find((b) =>
    (b.textContent ?? '').replace(/\s+/g, '').includes(text),
  )
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
  vi.resetAllMocks()
})

describe('注册管理页', () => {
  it('把三选一的准入姿态摆在界面上，而不是三个独立的开关', async () => {
    const container = await renderPage(['identity.registration.read', 'identity.registration.write'])

    const radios = container.querySelectorAll('input[type="radio"]')
    expect(radios.length).toBe(3)
    expect(container.textContent).toContain('开放注册')
    expect(container.textContent).toContain('邀请码')
    expect(container.textContent).toContain('不接受新账号')
  })

  it('说清默认角色只对未登记身份生效，且不追溯', async () => {
    const container = await renderPage(['identity.registration.read', 'identity.registration.write'])

    expect(container.textContent).toContain('未登记身份')
    expect(container.textContent).toContain('不追溯')
  })

  it('没有写权限时表单缺席，而策略与邀请码照常可读', async () => {
    const container = await renderPage(['identity.registration.read'])

    expect(container.textContent).toContain('只读')
    // 列表仍然在：读与写是两道门。
    expect(container.textContent).toContain('给张三')
    expect(buttonByText(container, '保存')).toBeUndefined()
  })

  it('签发之后把明文显示出来，并说明它只出现这一次', async () => {
    vi.mocked(registrationApi.createInvite).mockResolvedValue({
      invite: invite({ id: 'inv_new' }),
      code: 'ABCD-EFGH-JKMN-PQRS',
    })

    const container = await renderPage(['identity.registration.read', 'identity.registration.write'])

    const issue = buttonByText(container, '签发')
    expect(issue).toBeDefined()
    await act(async () => {
      issue?.click()
    })
    await act(async () => {})

    expect(container.textContent).toContain('ABCD-EFGH-JKMN-PQRS')
    expect(container.textContent).toContain('只保存它的摘要')
  })

  it('邀请码列表显示状态与用量，撤销过的行不再给撤销按钮', async () => {
    const container = await renderPage(
      ['identity.registration.read', 'identity.registration.write'],
      [
        invite({ id: 'inv_live', label: '还能用', usedCount: 1, maxUses: 3 }),
        invite({ id: 'inv_used', label: '已用尽', usedCount: 1, maxUses: 1 }),
        invite({ id: 'inv_gone', label: '已撤销', revokedAt: '2026-10-10T13:00:00Z' }),
      ],
    )

    expect(container.textContent).toContain('1 / 3')
    expect(container.textContent).toContain('1 / 1')
    expect(container.textContent).toContain('可用')
    expect(container.textContent).toContain('已用尽')
    expect(container.textContent).toContain('已撤销')
    // 三行里只有两行还能撤销：已撤销的那一行只显示状态。
    const revokeButtons = [...container.querySelectorAll('button')].filter((b) =>
      (b.textContent ?? '').includes('撤销'),
    )
    expect(revokeButtons.length).toBe(2)
  })
})
