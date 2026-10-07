import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as profileApi from '../api/profile'
import { SessionProvider } from '../auth'
import { ProfileProvider } from '../profile'
import { HomePage } from './HomePage'

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

vi.mock('../api/profile', () => ({
  getMyProfile: vi.fn(),
  updateMyProfile: vi.fn(),
  beginAvatarUpload: vi.fn(),
  commitAvatarUpload: vi.fn(),
  deleteMyAvatar: vi.fn(),
}))

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

async function renderHome(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <SessionProvider>
        <ProfileProvider>
          <HomePage />
        </ProfileProvider>
      </SessionProvider>,
    )
  })
  return container
}

/** 「显示名」那一格。按标签定位，而不是按整页文本——别的格子也会出现「—」。 */
function displayNameCell(container: HTMLElement): Element | undefined {
  return Array.from(container.querySelectorAll('.ant-descriptions-item')).find((cell) =>
    cell.textContent?.includes('显示名'),
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
})

describe('首页的显示名', () => {
  // 会话与权限码在进这一页之前就已就绪，档案是唯一晚到的数据。
  it('档案还没到时给骨架，而不是先用「—」把话说死', async () => {
    vi.mocked(profileApi.getMyProfile).mockReturnValue(new Promise<never>(() => {}))

    const container = await renderHome()

    expect(displayNameCell(container)?.querySelector('.ant-skeleton')).not.toBeNull()
  })

  it('档案到了之后骨架消失，换成真正的值', async () => {
    // 这份响应里没有档案，于是显示名回落到服务端那套回退规则的最后一步：破折号。
    vi.mocked(profileApi.getMyProfile).mockResolvedValue(
      {} as Awaited<ReturnType<typeof profileApi.getMyProfile>>,
    )

    const container = await renderHome()

    const cell = displayNameCell(container)
    expect(cell?.querySelector('.ant-skeleton')).toBeNull()
    expect(cell?.textContent).toContain('—')
  })
})
