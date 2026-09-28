import { create } from '@bufbuild/protobuf'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as profileApi from '../api/profile'
import {
  BeginAvatarUploadResponseSchema,
  CommitAvatarUploadResponseSchema,
} from '../gen/proto/aladdin/profile/v1/profile_pb'
import { DirectUploadCredentialSchema } from '../gen/proto/aladdin/objectstore/v1/upload_pb'
import type { Profile } from '../gen/proto/aladdin/profile/v1/profile_pb'
import { directUpload } from '../upload/direct-upload'
import { ProfileProvider, useProfile } from './profile-context'
import type { ProfileState } from './profile-context'

vi.mock('../api/profile', () => ({
  getMyProfile: vi.fn(),
  updateMyProfile: vi.fn(),
  beginAvatarUpload: vi.fn(),
  commitAvatarUpload: vi.fn(),
  deleteMyAvatar: vi.fn(),
}))

// 直传模块单独 mock：这里断言的是"上下文把凭证、文件与声明的类型原样交给它"，
// 而不是让真实的 COS SDK 发请求（那是 direct-upload 自己的测试）。
vi.mock('../upload/direct-upload', () => ({
  directUpload: vi.fn(),
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

  it('上传头像走签发 → 直传 → 提交，并用服务端返回的地址更新状态', async () => {
    vi.mocked(profileApi.getMyProfile).mockResolvedValue({
      $typeName: 'aladdin.profile.v1.GetProfileResponse',
      profile: profile(),
    })
    const credential = create(DirectUploadCredentialSchema, {
      bucket: 'examplebucket-1250000000',
      region: 'ap-guangzhou',
      key: 'avatars/u1',
      secretId: 'tmp-id',
      secretKey: 'tmp-key',
      sessionToken: 'tmp-token',
      expiresAt: '2030-01-01T00:00:00Z',
    })
    vi.mocked(profileApi.beginAvatarUpload).mockResolvedValue(
      create(BeginAvatarUploadResponseSchema, { upload: credential }),
    )
    vi.mocked(profileApi.commitAvatarUpload).mockResolvedValue(
      create(CommitAvatarUploadResponseSchema, {
        profile: profile({ avatarUrl: 'https://bucket.example.com/avatars/u1?sig=x' }),
      }),
    )

    await mountProvider()
    const file = new File([new Uint8Array([1, 2, 3])], 'me.png', { type: 'image/png' })
    await act(async () => {
      await latest?.updateAvatar(file)
    })

    // 类型与大小由上传方声明，原样带给签发；凭证原样交给直传模块，不复用、不改写。
    expect(profileApi.beginAvatarUpload).toHaveBeenCalledWith('image/png', 3)
    expect(directUpload).toHaveBeenCalledWith(credential, file, 'image/png')
    expect(profileApi.commitAvatarUpload).toHaveBeenCalledWith()
    expect(latest?.profile?.avatarUrl).toBe('https://bucket.example.com/avatars/u1?sig=x')
  })

  it('直传失败时向上抛出，而不是把半截状态留在本地', async () => {
    vi.mocked(profileApi.getMyProfile).mockResolvedValue({
      $typeName: 'aladdin.profile.v1.GetProfileResponse',
      profile: profile(),
    })
    vi.mocked(profileApi.beginAvatarUpload).mockResolvedValue(
      create(BeginAvatarUploadResponseSchema, {
        upload: create(DirectUploadCredentialSchema, { key: 'avatars/u1' }),
      }),
    )
    vi.mocked(directUpload).mockRejectedValue(new Error('直传失败：Network Error'))

    await mountProvider()
    const file = new File([new Uint8Array([1, 2, 3])], 'me.png', { type: 'image/png' })

    await expect(
      act(async () => {
        await latest?.updateAvatar(file)
      }),
    ).rejects.toThrow('直传失败：Network Error')
    // 提交没发生，本地档案保持原样。
    expect(profileApi.commitAvatarUpload).not.toHaveBeenCalled()
    expect(latest?.profile?.avatarUrl).toBe('')
  })
})
