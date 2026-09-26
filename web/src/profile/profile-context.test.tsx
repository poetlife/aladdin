import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as profileApi from '../api/profile'
import type { Profile } from '../gen/proto/aladdin/profile/v1/profile_pb'
import { ProfileProvider, useProfile } from './profile-context'
import type { ProfileState } from './profile-context'

vi.mock('../api/profile', () => ({
  getMyProfile: vi.fn(),
  updateMyProfile: vi.fn(),
  updateMyAvatar: vi.fn(),
  deleteMyAvatar: vi.fn(),
}))

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let latest: ProfileState | null = null
let root: Root | null = null

function Probe() {
  latest = useProfile()
  return null
}

async function mountProvider(): Promise<void> {
  const container = document.createElement('div')
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <ProfileProvider>
        <Probe />
      </ProfileProvider>,
    )
  })
}

function profile(overrides: Partial<Profile> = {}): Profile {
  return {
    $typeName: 'aladdin.profile.v1.Profile',
    nickname: '',
    displayName: 'a@example.com',
    bio: '',
    avatarUrl: '',
    avatarUploadEnabled: true,
    avatarMaxBytes: 2 * 1024 * 1024,
    ...overrides,
  }
}

beforeEach(() => {
  latest = null
  root = null
})

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  vi.clearAllMocks()
})

describe('档案状态', () => {
  it('挂载时读一次档案', async () => {
    vi.mocked(profileApi.getMyProfile).mockResolvedValue({
      $typeName: 'aladdin.profile.v1.GetProfileResponse',
      profile: profile({ nickname: '阿拉丁' }),
    })

    await mountProvider()

    expect(latest?.loading).toBe(false)
    expect(latest?.profile?.nickname).toBe('阿拉丁')
    expect(latest?.profile?.displayName).toBe('a@example.com')
  })

  it('读取失败时留下错误，而不是把 loading 永远挂住', async () => {
    vi.mocked(profileApi.getMyProfile).mockRejectedValue(new Error('读取失败'))

    await mountProvider()

    expect(latest?.loading).toBe(false)
    expect(latest?.profile).toBeNull()
    expect(latest?.error).toBe('读取失败')
  })

  it('保存之后用**服务端返回的那一份**更新状态，而不是提交的值', async () => {
    vi.mocked(profileApi.getMyProfile).mockResolvedValue({
      $typeName: 'aladdin.profile.v1.GetProfileResponse',
      profile: profile(),
    })
    // 服务端会去掉首尾空白。把提交的值直接拼进状态，界面就会与库里的不一致——
    // 而那种不一致的表现是"页头显示的名字和我刚填的不一样"。
    vi.mocked(profileApi.updateMyProfile).mockResolvedValue({
      $typeName: 'aladdin.profile.v1.UpdateProfileResponse',
      profile: profile({ nickname: '阿拉丁', displayName: '阿拉丁' }),
    })

    await mountProvider()
    await act(async () => {
      await latest?.updateProfile('  阿拉丁  ', '')
    })

    expect(latest?.profile?.nickname).toBe('阿拉丁')
    expect(latest?.profile?.displayName).toBe('阿拉丁')
  })

  it('上传头像后用服务端返回的地址更新状态', async () => {
    vi.mocked(profileApi.getMyProfile).mockResolvedValue({
      $typeName: 'aladdin.profile.v1.GetProfileResponse',
      profile: profile(),
    })
    vi.mocked(profileApi.updateMyAvatar).mockResolvedValue({
      $typeName: 'aladdin.profile.v1.UpdateAvatarResponse',
      profile: profile({ avatarUrl: 'https://bucket.example.com/avatars/u1?sig=x' }),
    })

    await mountProvider()
    await act(async () => {
      await latest?.updateAvatar(new Uint8Array([1, 2, 3]))
    })

    expect(latest?.profile?.avatarUrl).toBe('https://bucket.example.com/avatars/u1?sig=x')
  })
})
