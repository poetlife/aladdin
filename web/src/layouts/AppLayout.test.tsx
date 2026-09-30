import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as identityApi from '../api/identity'
import * as profileApi from '../api/profile'
import * as rbacApi from '../api/rbac'
import { SessionProvider } from '../auth'
import { ScopesPage } from '../pages/ScopesPage'
import { ThemeProvider } from '../theme'
import { installMatchMedia } from '../test/match-media'
import { AppLayout } from './AppLayout'

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

// 顶栏的管理范围会列出"我自己绑定的范围"，导航的权限分组也按权限码裁剪——
// 两者都需要管理面接口，因此在文件级挡住它们。
vi.mock('../api/rbac', () => ({
  listRoles: vi.fn(),
  listSubjectBindings: vi.fn(),
  assignRole: vi.fn(),
  listScopes: vi.fn(),
  putScope: vi.fn(),
  deleteScope: vi.fn(),
}))

vi.mock('../api/profile', () => ({
  getMyProfile: vi.fn(),
  updateMyProfile: vi.fn(),
  beginAvatarUpload: vi.fn(),
  commitAvatarUpload: vi.fn(),
  deleteMyAvatar: vi.fn(),
}))

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const SIDER = '.ant-layout-sider'
const DRAWER_OPEN = '.ant-drawer-open'

let root: Root | null = null

async function renderShell(path = '/'): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <ThemeProvider>
        <SessionProvider>
          <MemoryRouter initialEntries={[path]}>
            <Routes>
              <Route element={<AppLayout />}>
                <Route path="/" element={<p>概览内容</p>} />
                <Route path="/profile" element={<p>档案内容</p>} />
                <Route path="/docs" element={<p>文档内容</p>} />
                <Route path="/docs/cli" element={<p>命令行内容</p>} />
                <Route path="/access/roles" element={<p>角色定义内容</p>} />
                <Route path="/access/subjects" element={<p>人员授权内容</p>} />
                <Route path="/access/scopes" element={<ScopesPage />} />
              </Route>
            </Routes>
          </MemoryRouter>
        </SessionProvider>
      </ThemeProvider>,
    )
  })
  return container
}

/**
 * 渲染一个**已认证且持有指定权限码**的外壳。
 *
 * 显式写入范围，绕开"首次登录时采纳服务端默认范围"那条路径——那条已由
 * session.test.tsx 覆盖，这里要测的是外壳本身。
 */
async function renderAuthenticated(
  permissions: string[],
  path = '/',
  scope = 'tenant/acme',
): Promise<HTMLElement> {
  globalThis.localStorage.setItem('aladdin.token', 'tok')
  globalThis.localStorage.setItem('aladdin.scope', scope)
  vi.mocked(identityApi.whoAmI).mockResolvedValue({
    $typeName: 'aladdin.identity.v1.WhoAmIResponse',
    subjectId: 'u1',
    subjectType: 'user',
    defaultScope: 'tenant/acme',
  })
  vi.mocked(identityApi.getSessionPermissions).mockResolvedValue({
    $typeName: 'aladdin.identity.v1.GetSessionPermissionsResponse',
    scope,
    permissions,
  })
  return renderShell(path)
}

/** 往受控输入框里写值：直接改 value 不会触发 React 的 onChange。 */
function typeInto(input: Element, value: string): void {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
  setter?.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

/** 在文本框里按下回车。 */
async function pressEnter(input: Element): Promise<void> {
  await act(async () => {
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
  })
}

function scopeInput(container: HTMLElement): HTMLInputElement {
  const input = container.querySelector('[aria-label="管理范围"]')
  expect(input, '页头里没有管理范围控件').not.toBeNull()
  return input as HTMLInputElement
}

/** 按无障碍标签点一颗按钮。 */
async function clickByLabel(container: HTMLElement, label: string): Promise<void> {
  const button = container.querySelector(`[aria-label="${label}"]`)
  expect(button, `没有找到标签为「${label}」的控件`).not.toBeNull()
  await act(async () => {
    button?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

/** 在抽屉里按下某个导航项。 */
async function clickDrawerNav(label: string): Promise<void> {
  const items = Array.from(document.querySelectorAll('.ant-drawer-body .ant-menu-item'))
  const target = items.find((item) => item.textContent?.includes(label))
  expect(target, `抽屉里没有「${label}」这项`).not.toBeUndefined()
  await act(async () => {
    target?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

/** 打开抽屉底部的账号区菜单，再点其中的一项。 */
async function clickAccountMenu(label: string): Promise<void> {
  const accountButton = document.querySelector('.ant-drawer button[title]')
  expect(accountButton, '抽屉里没有账号区').not.toBeNull()
  await act(async () => {
    accountButton?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })

  const items = Array.from(document.querySelectorAll('.ant-dropdown-menu-item'))
  const target = items.find((item) => item.textContent?.includes(label))
  expect(target, `账号区菜单里没有「${label}」`).not.toBeUndefined()
  await act(async () => {
    target?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

beforeEach(() => {
  globalThis.localStorage?.clear()
  vi.mocked(profileApi.getMyProfile).mockResolvedValue(
    {} as Awaited<ReturnType<typeof profileApi.getMyProfile>>,
  )
})

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
})

describe('外壳的宽窄两态', () => {
  it('宽屏是常驻侧边栏：开关收放的是导轨', async () => {
    installMatchMedia(false)

    const container = await renderShell()

    expect(container.querySelector(SIDER)).not.toBeNull()
    // 窄屏的那套东西在宽屏下根本不该被渲染出来。
    expect(container.querySelector('.ant-drawer')).toBeNull()

    await clickByLabel(container, '收起侧边栏')

    expect(container.querySelector(`${SIDER}-collapsed`)).not.toBeNull()
    // 收起来之后，开关该变成"展开"。
    expect(container.querySelector('[aria-label="展开侧边栏"]')).not.toBeNull()
  })

  it('窄屏不渲染侧边栏，导航改由抽屉承载', async () => {
    installMatchMedia(true)

    const container = await renderShell()

    expect(container.querySelector(SIDER)).toBeNull()
    expect(document.querySelector(DRAWER_OPEN)).toBeNull()

    await clickByLabel(container, '打开导航')

    expect(document.querySelector(DRAWER_OPEN)).not.toBeNull()
    // 抽屉里的导航与侧边栏是同一份内容：这里必须有菜单。
    expect(document.querySelector('.ant-drawer-body .ant-menu')).not.toBeNull()
  })

  it('抽屉里选中一个导航项后，跳转并自动关闭', async () => {
    installMatchMedia(true)

    const container = await renderShell()
    await clickByLabel(container, '打开导航')

    await clickDrawerNav('个人资料')

    expect(container.textContent).toContain('档案内容')
    expect(document.querySelector(DRAWER_OPEN)).toBeNull()
  })

  // 账号区是另一条跳转路径（Dropdown 而不是 Menu）。它一度漏了"跳转后收起抽屉"，
  // 表现为点完人已经在个人资料页，抽屉却还盖在上面。
  it('从账号区跳转后也要收起抽屉', async () => {
    installMatchMedia(true)

    const container = await renderShell()
    await clickByLabel(container, '打开导航')

    await clickAccountMenu('个人资料')

    expect(container.textContent).toContain('档案内容')
    expect(document.querySelector(DRAWER_OPEN)).toBeNull()
  })

  // 文档区不要权限码：零权限的主体最需要它，否则"先装命令行才能登录、
  // 登录了才看得到怎么装命令行"这个环闭不上（见 docs/design/web/docs-area.md）。
  it('导航里有文档入口，零权限也渲染得出来', async () => {
    installMatchMedia(true)

    const container = await renderShell()
    await clickByLabel(container, '打开导航')
    await clickDrawerNav('文档')

    expect(container.textContent).toContain('文档内容')
  })

  // 导航项都是一级路径，而 /docs/cli 这类子页比它深。若拿整个路径去比对，
  // 进到子页时父项就不再高亮——二级导航项一出现就会撞上。
  it('进到子页时父导航项仍然高亮', async () => {
    installMatchMedia(false)

    const container = await renderShell('/docs/cli')

    expect(container.textContent).toContain('命令行内容')
    const selected = container.querySelector('.ant-menu-item-selected')
    expect(selected?.textContent).toContain('文档')
  })
})

describe('权限分组与管理范围', () => {
  it('有读角色权限时，「权限」分组里出现角色定义并能跳转', async () => {
    installMatchMedia(true)

    const container = await renderAuthenticated(['rbac.role.read'])
    await clickByLabel(container, '打开导航')

    // 分组是可展开的容器，子项要点开才看得见（这也是"分组"与"平铺两项"的区别）。
    const group = [...document.querySelectorAll('.ant-drawer-body .ant-menu-submenu-title')].find(
      (el) => el.textContent?.includes('权限'),
    )
    expect(group, '抽屉里没有「权限」分组').not.toBeUndefined()
    await act(async () => {
      group?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    await clickDrawerNav('角色定义')

    expect(container.textContent).toContain('角色定义内容')
  })

  // 深链或刷新进来时，选中项藏在收起的分组里等于没被选中——分组必须自己展开。
  it('深链直接进角色定义页时，分组已展开且子项被选中', async () => {
    installMatchMedia(false)

    const container = await renderAuthenticated(['rbac.role.read'], '/access/roles')
    await act(async () => {})

    expect(container.textContent).toContain('角色定义内容')
    const selected = container.querySelector('.ant-menu-item-selected')
    expect(selected?.textContent).toContain('角色定义')
  })

  it('管理范围为全局时显示「全局」，不出现内部写法', async () => {
    installMatchMedia(false)

    const container = await renderAuthenticated([], '/', '')

    expect(scopeInput(container).value).toBe('全局')
    expect(container.textContent).not.toContain('<global>')
  })

    it('输入「全局」并回车后，提交的是空范围', async () => {
    installMatchMedia(false)

    const container = await renderAuthenticated([], '/', 'tenant/acme')
    const input = scopeInput(container)
    expect(input.value).toBe('tenant/acme')

    // 让服务端如实回传被请求的范围：这条测的是"提交出去的是什么"，
    // 别让夹具的固定返回值把提交值盖掉。必须在 render 之后设——mockResolvedValue
    // 就是一次 mockImplementation，先设会被 renderAuthenticated 里那一次覆盖。
    vi.mocked(identityApi.getSessionPermissions).mockImplementation(async (scope: string) => ({
      $typeName: 'aladdin.identity.v1.GetSessionPermissionsResponse',
      scope,
      permissions: [],
    }))

    typeInto(input, '全局')
    await pressEnter(input)

    expect(identityApi.getSessionPermissions).toHaveBeenLastCalledWith('')
    expect(globalThis.localStorage.getItem('aladdin.scope')).toBe('')
  })

  // 范围目录由外壳持有：范围页新建之后，顶栏的候选必须立刻跟着变。
  // 三处各拉一份列表时这一条会失败——界面自相矛盾（见 web/src/rbac/scopes-context.tsx）。
  it('在范围页新建的范围，立刻出现在顶栏的候选里', async () => {
    installMatchMedia(false)
    vi.mocked(rbacApi.listScopes)
      .mockResolvedValueOnce({
        $typeName: 'aladdin.rbac.v1.ListScopesResponse',
        scopes: [{ $typeName: 'aladdin.rbac.v1.Scope', path: 'tenant/acme', displayName: '' }],
      })
      // 建完之后服务端的那一份就该多出这一条。
      .mockResolvedValue({
        $typeName: 'aladdin.rbac.v1.ListScopesResponse',
        scopes: [{ $typeName: 'aladdin.rbac.v1.Scope', path: 'tenant/acme/project', displayName: '' }],
      })
    vi.mocked(rbacApi.putScope).mockResolvedValue({
      $typeName: 'aladdin.rbac.v1.PutScopeResponse',
      scope: { $typeName: 'aladdin.rbac.v1.Scope', path: 'tenant/acme/project', displayName: '' },
    })

    const container = await renderAuthenticated(
      ['rbac.scope.read', 'rbac.scope.write'],
      '/access/scopes',
    )

    const pathInput = container.querySelector('#path')
    expect(pathInput, '范围页的路径输入框没渲染出来').not.toBeNull()
    typeInto(pathInput as Element, 'tenant/acme/project')
    await act(async () => {
      const submit = [...container.querySelectorAll('button')].find(
        (b) => (b.textContent ?? '').replace(/\s+/g, '') === '登记',
      )
      submit?.click()
    })
    await act(async () => {})

    // 打开顶栏那个控件：候选里应该有它。
    const auto = container.querySelector('.ant-select-auto-complete')
    expect(auto, '顶栏没有管理范围控件').not.toBeNull()
    const trigger = (auto as Element).querySelector('.ant-select-content') ?? auto
    await act(async () => {
      trigger?.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    })

    const options = [...document.querySelectorAll('.ant-select-item-option')].map((o) => o.textContent)
    expect(options).toContain('tenant/acme/project')
  })

  // 列不出候选（没读主体的权限、主体还没登记、这次读取失败）是可预期的状态，
  // 不能让用户以为"没得选"：提示要落在他能看到候选的那块地方——下拉空态。
  it('列不出候选时，下拉空态里说明可以手输', async () => {
    installMatchMedia(false)

    const container = await renderAuthenticated([], '/')
    const auto = container.querySelector('.ant-select-auto-complete')
    expect(auto, '顶栏没有管理范围控件').not.toBeNull()
    const trigger = (auto as Element).querySelector('.ant-select-content') ?? auto
    await act(async () => {
      trigger?.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    })

    expect(document.body.textContent).toContain('可直接输入路径')
  })
})
