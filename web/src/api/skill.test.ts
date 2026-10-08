import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { DirectUploadCredentialSchema } from '../gen/proto/aladdin/objectstore/v1/upload_pb'
import {
  BeginSkillImageUploadResponseSchema,
  CommitSkillImageUploadResponseSchema,
  SkillImageSchema,
  SkillSchema,
} from '../gen/proto/aladdin/skill/v1/skill_pb'
import { directUpload } from '../upload/direct-upload'
import { addSkillImage } from './skill'
import * as transport from './transport'

// 只换掉出站的两个客户端：这里要验的是**三步的顺序与参数**，不是 HTTP 怎么发。
vi.mock('./transport', () => ({
  skillClient: vi.fn(),
  skillAdminClient: vi.fn(),
}))

vi.mock('../upload/direct-upload', () => ({ directUpload: vi.fn() }))

const beginSkillImageUpload = vi.fn()
const commitSkillImageUpload = vi.fn()

function credential() {
  return create(DirectUploadCredentialSchema, {
    bucket: 'examplebucket-1250000000',
    region: 'ap-guangzhou',
    key: 'skills/skl_a/img_a',
    secretId: 'tmp-id',
    secretKey: 'tmp-key',
    sessionToken: 'tmp-token',
    expiresAt: '2030-01-01T00:00:00Z',
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(transport.skillAdminClient).mockReturnValue({
    beginSkillImageUpload,
    commitSkillImageUpload,
  } as unknown as ReturnType<typeof transport.skillAdminClient>)
})

describe('展示图上传', () => {
  it('加一张走完签发 → 直传 → 提交，提交用服务端分配的图标识', async () => {
    const upload = credential()
    beginSkillImageUpload.mockResolvedValue(
      create(BeginSkillImageUploadResponseSchema, { imageId: 'img_new', upload }),
    )
    const submitted = create(SkillSchema, {
      id: 'skl_a',
      images: [create(SkillImageSchema, { id: 'img_new', url: 'https://example.test/new.png' })],
    })
    commitSkillImageUpload.mockResolvedValue(
      create(CommitSkillImageUploadResponseSchema, { skill: submitted }),
    )

    const file = new File([new Uint8Array([1, 2, 3])], 'cover.png', { type: 'image/png' })
    const skill = await addSkillImage('skl_a', file)

    // 留空的 imageId 表示新增：标识交给服务端分配，不在这里拼一个。
    expect(beginSkillImageUpload).toHaveBeenCalledWith({
      skillId: 'skl_a',
      imageId: '',
      contentType: 'image/png',
      sizeBytes: 3n,
    })
    // 凭证原样交给直传模块；**字节不经过服务端**。
    expect(directUpload).toHaveBeenCalledWith(upload, file, 'image/png')
    // 提交的是签发时定的那一个标识，不是本地猜的。
    expect(commitSkillImageUpload).toHaveBeenCalledWith({ skillId: 'skl_a', imageId: 'img_new' })
    // 返回的是服务端那一份：调用方据此刷新，而不是本地拼地址。
    expect(skill).toBe(submitted)
  })

  it('给出现有的标识表示换掉那一张的字节：签发与提交都指同一个标识', async () => {
    beginSkillImageUpload.mockResolvedValue(
      create(BeginSkillImageUploadResponseSchema, { imageId: 'img_a', upload: credential() }),
    )
    commitSkillImageUpload.mockResolvedValue(
      create(CommitSkillImageUploadResponseSchema, { skill: create(SkillSchema, { id: 'skl_a' }) }),
    )

    const file = new File([new Uint8Array([1])], 'cover.png', { type: 'image/png' })
    await addSkillImage('skl_a', file, 'img_a')

    expect(beginSkillImageUpload).toHaveBeenCalledWith({
      skillId: 'skl_a',
      imageId: 'img_a',
      contentType: 'image/png',
      sizeBytes: 1n,
    })
    // 键与位置都不变：换图就是覆盖那一个键的字节。
    expect(commitSkillImageUpload).toHaveBeenCalledWith({ skillId: 'skl_a', imageId: 'img_a' })
  })

  it('服务端没给直传凭证时不直传、也不提交', async () => {
    beginSkillImageUpload.mockResolvedValue(create(BeginSkillImageUploadResponseSchema, {}))

    const file = new File([new Uint8Array([1])], 'cover.png', { type: 'image/png' })
    await expect(addSkillImage('skl_a', file)).rejects.toThrow('服务端没有返回直传凭证')

    expect(directUpload).not.toHaveBeenCalled()
    expect(commitSkillImageUpload).not.toHaveBeenCalled()
  })
})
