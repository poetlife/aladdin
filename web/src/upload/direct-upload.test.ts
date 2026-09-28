import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { DirectUploadCredentialSchema } from '../gen/proto/aladdin/objectstore/v1/upload_pb'
import { directUpload } from './direct-upload'

// 记录交给 COS SDK 的构造函数参数（临时凭证）与 putObject 参数（桶、地域、键、类型）。
const { putObjectMock, initOptions } = vi.hoisted(() => ({
  putObjectMock: vi.fn(),
  initOptions: [] as unknown[],
}))

// 把 COS SDK 换成一个最小替身：不联网，只把收到的参数交回给测试断言。
// putObject 的返回值当作回调的 err：默认 null（成功），测试可换成错误对象。
vi.mock('cos-js-sdk-v5', () => ({
  default: class {
    constructor(options: unknown) {
      initOptions.push(options)
    }
    putObject(params: unknown, callback: (err: unknown) => void): void {
      callback(putObjectMock(params) ?? null)
    }
  },
}))

function credential() {
  return create(DirectUploadCredentialSchema, {
    bucket: 'examplebucket-1250000000',
    region: 'ap-guangzhou',
    key: 'avatars/u1',
    secretId: 'tmp-id',
    secretKey: 'tmp-key',
    sessionToken: 'tmp-token',
    expiresAt: '2030-01-01T00:00:00Z',
  })
}

beforeEach(() => {
  putObjectMock.mockReset()
  putObjectMock.mockReturnValue(null)
  initOptions.length = 0
})

describe('直传', () => {
  it('用临时凭证三元组初始化 SDK', async () => {
    await directUpload(credential(), new Blob(['hello']), 'text/plain')

    expect(initOptions).toEqual([
      { SecretId: 'tmp-id', SecretKey: 'tmp-key', SecurityToken: 'tmp-token' },
    ])
  })

  it('把凭证里的桶、地域、键与声明的类型原样交给 putObject', async () => {
    const body = new Blob(['hello'], { type: 'image/png' })

    await directUpload(credential(), body, 'image/png')

    expect(putObjectMock).toHaveBeenCalledWith({
      Bucket: 'examplebucket-1250000000',
      Region: 'ap-guangzhou',
      Key: 'avatars/u1',
      Body: body,
      ContentType: 'image/png',
    })
  })

  it('存储侧失败时抛出可重试的错误，且错误里不含凭证', async () => {
    putObjectMock.mockReturnValue({ code: 'AccessDenied', message: 'Forbidden', statusCode: 403 })

    const failure = await directUpload(credential(), new Blob(['x']), 'image/png').catch(
      (err: unknown) => err,
    )

    expect(failure).toBeInstanceOf(Error)
    const message = (failure as Error).message
    expect(message).toContain('AccessDenied')
    expect(message).not.toContain('tmp-key')
    expect(message).not.toContain('tmp-token')
  })
})
