import { afterEach, describe, expect, it, vi } from 'vitest'

import { FRAME_CONTENT_CREDENTIAL_REQUEST_TYPE, FRAME_CONTENT_CREDENTIAL_TYPE, frameMessage } from './frame-channel.js'
import { contentCredential } from './credentials.js'

/**
 * 一个假的宿主窗口与页面窗口。两者共用同一份监听表——页面往宿主的表里注册，
 * 宿主用同一条表回话。
 */
function fakeFrame(): {
  host: WindowProxy
  self: Window
  sent: { data: unknown; targetOrigin: string }[]
  reply: (data: unknown, source?: unknown) => void
} {
  const sent: { data: unknown; targetOrigin: string }[] = []
  const listeners: ((event: MessageEvent) => void)[] = []
  const host = {
    postMessage(data: unknown, targetOrigin: string) {
      sent.push({ data, targetOrigin })
    },
  } as unknown as WindowProxy
  const self = {
    parent: host,
    addEventListener(_type: string, callback: (event: MessageEvent) => void) {
      listeners.push(callback)
    },
    removeEventListener(_type: string, callback: (event: MessageEvent) => void) {
      const index = listeners.indexOf(callback)
      if (index >= 0) {
        listeners.splice(index, 1)
      }
    },
  } as unknown as Window
  return {
    host,
    self,
    sent,
    reply: (data: unknown, source?: unknown) => {
      for (const listener of listeners) {
        listener({ source: source ?? host, data } as unknown as MessageEvent)
      }
    },
  }
}

afterEach(() => {
  vi.useRealTimers()
})

describe('取一份取内容用的凭证', () => {
  it('调用方直接给了一份就不再问宿主（本地调试那一条路）', async () => {
    const { host, self, sent } = fakeFrame()
    await expect(contentCredential({ credential: 'from-cli', host, self })).resolves.toEqual({
      credential: 'from-cli',
      expiresAt: '',
      reason: '',
    })
    expect(sent).toEqual([])
  })

  it('页面没有被嵌进任何宿主时如实说清楚，而不是干等', async () => {
    await expect(contentCredential()).resolves.toEqual({
      credential: '',
      expiresAt: '',
      reason: '这一页没有被宿主嵌进来，取不到凭证',
    })
  })

  it('向宿主要一条，并把它的回答读回来', async () => {
    const { host, self, sent, reply } = fakeFrame()
    const answer = frameMessage(FRAME_CONTENT_CREDENTIAL_TYPE, { credential: 'tok', expiresAt: '', reason: '' })

    const pending = contentCredential({ host, self })
    // 发出去的确实是一条认得出的凭证请求，而不是别的东西。
    expect(sent).toHaveLength(1)
    expect((sent[0]?.data as { type?: string }).type).toBe(FRAME_CONTENT_CREDENTIAL_REQUEST_TYPE)
    reply(answer)

    await expect(pending).resolves.toEqual({ credential: 'tok', expiresAt: '', reason: '' })
  })

  it('不是自己那个宿主窗口发来的消息一律不理', async () => {
    vi.useFakeTimers()
    const { host, self, reply } = fakeFrame()
    const answer = frameMessage(FRAME_CONTENT_CREDENTIAL_TYPE, { credential: '别处来的', expiresAt: '', reason: '' })

    const pending = contentCredential({ host, self, timeoutMs: 50 })
    reply(answer, {})
    await vi.advanceTimersByTimeAsync(100)

    await expect(pending).resolves.toEqual({
      credential: '',
      expiresAt: '',
      reason: '宿主没有在时限内回答凭证请求',
    })
  })

  it('宿主不理它时给一条明确的否定，不是一句超时的异常', async () => {
    vi.useFakeTimers()
    const { host, self } = fakeFrame()

    const pending = contentCredential({ host, self, timeoutMs: 50 })
    await vi.advanceTimersByTimeAsync(100)

    await expect(pending).resolves.toEqual({
      credential: '',
      expiresAt: '',
      reason: '宿主没有在时限内回答凭证请求',
    })
  })
})
