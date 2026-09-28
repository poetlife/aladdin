import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as profileApi from '../api/profile'
import { SessionProvider } from '../auth'
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
              </Route>
            </Routes>
          </MemoryRouter>
        </SessionProvider>
      </ThemeProvider>,
    )
  })
  return container
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
