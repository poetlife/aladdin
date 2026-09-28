import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as profileApi from '../api/profile'
import { SessionProvider } from '../auth'
import { ProfileProvider } from '../profile'
import { ProfilePage } from './ProfilePage'

vi.mock('../api/identity', () => ({
  AuthSource: { Google: 'google', Github: 'github' },
  bindGoogleIdentity: vi.fn(),
  getAuthMethods: vi.fn(),
  listIdentities: vi.fn(),
  unbindIdentity: vi.fn(),
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

async function renderPage(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <SessionProvider>
        <ProfileProvider>
          <ProfilePage />
        </ProfileProvider>
      </SessionProvider>,
    )
  })
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
})

describe('资料页的加载态', () => {
  // 骨架的形状要与真实页面一致：三张卡，数据到了就地替换，视线不用重新找位置。
  it('档案未到时用同形的骨架占位，而不是一句「正在读取」', async () => {
    vi.mocked(profileApi.getMyProfile).mockReturnValue(new Promise<never>(() => {}))

    const container = await renderPage()

    expect(container.querySelectorAll('.ant-card')).toHaveLength(3)
    expect(container.querySelector('.ant-skeleton')).not.toBeNull()
    expect(container.textContent).toContain('头像')
    expect(container.textContent).toContain('昵称与简介')
    expect(container.textContent).toContain('登录方式')
  })

  // 档案确实读不到时是另一回事：那时不该再摆骨架，而要说清读不到并给出重试。
  it('档案读不到时说清失败并给出重试，而不是一直摆骨架', async () => {
    vi.mocked(profileApi.getMyProfile).mockRejectedValue(new Error('服务不可用'))

    const container = await renderPage()

    expect(container.querySelector('.ant-skeleton')).toBeNull()
    expect(container.textContent).toContain('读取档案失败')
    expect(container.querySelector('button')).not.toBeNull()
  })
})
