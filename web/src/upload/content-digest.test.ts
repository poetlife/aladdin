import { describe, expect, it } from 'vitest'

import { sha256Hex } from './content-digest'

describe('内容摘要', () => {
  // 摘要必须算对：它是公开区地址的键，服务端会在发布时核对，算错的表现是
  // "这个版本发布不出来"。用已知向量把它钉住。
  it('给出 SHA-256 的小写十六进制', async () => {
    expect(await sha256Hex(new Uint8Array([]).buffer)).toBe(
      'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
    )
    expect(await sha256Hex(new TextEncoder().encode('abc').buffer as ArrayBuffer)).toBe(
      'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad',
    )
  })
})
