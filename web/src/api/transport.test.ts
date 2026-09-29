import { createClient } from '@connectrpc/connect'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { IdentityService } from '../gen/proto/aladdin/identity/v1/identity_pb'
import { CLIENT_HEADER, CLIENT_WEB } from './client-id'
import { createTransport, resolveBinaryFormat } from './transport'

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

describe('传输层注入的上报端标识', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  // 服务端请求留痕据此区分浏览器与命令行。它写死在拦截器里，因此每个出站
  // 请求都必然带上——漏掉的表现是"服务端把 web 的调用当成未知来源"，
  // 而那不会报错，只会让按端检索少一半数据。
  it('每个出站请求都带上 x-aladdin-client: web', async () => {
    const headers: Headers[] = []
    vi.stubGlobal('fetch', async (_input: unknown, init?: RequestInit) => {
      headers.push(new Headers(init?.headers))
      // 报文内容不重要：本用例只看出站请求头。
      return new Response('{}', { status: 200, headers: { 'content-type': 'application/json' } })
    })

    const transport = createTransport({ baseUrl: '', getToken: () => null })
    const client = createClient(IdentityService, transport)
    // 响应解码失败无所谓：请求已经发出去了，头部信息已经拿到。
    await client.getAuthMethods({}).catch(() => undefined)

    expect(headers).toHaveLength(1)
    expect(headers[0]?.get(CLIENT_HEADER)).toBe(CLIENT_WEB)
  })
})
