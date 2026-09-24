import { afterEach, describe, expect, it, vi } from 'vitest'

import { resolveBinaryFormat } from './transport'

describe('resolveBinaryFormat', () => {
  afterEach(() => {
    vi.unstubAllEnvs()
  })

  it('开发环境默认 JSON，便于在 DevTools 里直接读报文', () => {
    vi.stubEnv('PROD', false)
    expect(resolveBinaryFormat('')).toBe(false)
  })

  it('生产环境默认二进制', () => {
    vi.stubEnv('PROD', true)
    expect(resolveBinaryFormat('')).toBe(true)
  })

  it('生产环境可以用查询参数切到 JSON，无需重新发布', () => {
    vi.stubEnv('PROD', true)
    expect(resolveBinaryFormat('?wire=json')).toBe(false)
  })

  it('开发环境可以用查询参数切回二进制，用于发布前验证', () => {
    vi.stubEnv('PROD', false)
    expect(resolveBinaryFormat('?wire=binary')).toBe(true)
  })

  it('无法识别的取值回退到环境默认，而不是当成有效覆盖', () => {
    vi.stubEnv('PROD', true)
    expect(resolveBinaryFormat('?wire=yaml')).toBe(true)
  })

  it('与其他查询参数共存时仍能识别', () => {
    vi.stubEnv('PROD', true)
    expect(resolveBinaryFormat('?foo=1&wire=json&bar=2')).toBe(false)
  })
})
