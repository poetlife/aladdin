import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

/** 一次上报请求的形状（只关心本用例断言的那部分）。 */
interface ReportCall {
  events: Array<{
    client: Client
    result: Result
    attrs: Record<string, string>
  }>
}

const state = vi.hoisted(() => ({
  unavailable: false,
  // 显式给出调用签名：mock.calls 才会被推导成"有一次上报请求"，否则参数元组是空的。
  reportEvents: vi.fn<(req: { events: unknown[] }) => Promise<unknown>>(() => Promise.resolve({})),
}))

vi.mock('../api/transport', () => ({
  telemetryClient: () => {
    if (state.unavailable) {
      throw new Error('传输层尚未初始化')
    }
    return { reportEvents: state.reportEvents }
  },
}))

import { Action, Client, Result, Surface } from '../gen/proto/aladdin/telemetry/v1/telemetry_pb'
import { FLUSH_INTERVAL_MS } from './event-buffer'
import { track } from './track'

/** 取第 n 次上报带的那一批事件。 */
function batchOf(call: number): ReportCall['events'] {
  return (state.reportEvents.mock.calls[call]?.[0] as ReportCall | undefined)?.events ?? []
}

describe('track', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    state.unavailable = false
    state.reportEvents.mockClear()
    state.reportEvents.mockImplementation(() => Promise.resolve({}))
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('失败类动作立即发送，并补上 client=web', () => {
    track({
      surface: Surface.WEB_AUTH,
      action: Action.AUTH_LOGIN,
      result: Result.FAIL,
      attrs: { channel: 'password' },
    })

    expect(state.reportEvents).toHaveBeenCalledTimes(1)
    const [event] = batchOf(0)
    expect(event?.client).toBe(Client.WEB)
    expect(event?.result).toBe(Result.FAIL)
    expect(event?.attrs).toEqual({ channel: 'password' })
  })

  it('成功类动作攒批，到时间窗才发', () => {
    track({ surface: Surface.WEB_PREVIEW, action: Action.PREVIEW_TOGGLE, result: Result.OK })
    track({ surface: Surface.WEB_PREVIEW, action: Action.PREVIEW_TOGGLE, result: Result.OK })
    expect(state.reportEvents).not.toHaveBeenCalled()

    vi.advanceTimersByTime(FLUSH_INTERVAL_MS)
    expect(state.reportEvents).toHaveBeenCalledTimes(1)
    expect(batchOf(0)).toHaveLength(2)
  })

  it('上报失败不抛出：一次遥测故障不能影响界面操作', async () => {
    state.reportEvents.mockImplementationOnce(() => Promise.reject(new Error('网络不可达')))

    expect(() =>
      track({ surface: Surface.WEB_EDITOR, action: Action.PUBLISH, result: Result.BLOCKED }),
    ).not.toThrow()
    // 让被吞掉的拒绝走完，确认没有未处理的拒绝。
    await Promise.resolve()
  })

  it('传输层未就绪时静默丢弃，不抛出', () => {
    state.unavailable = true
    expect(() =>
      track({ surface: Surface.WEB_EDITOR, action: Action.PUBLISH, result: Result.BLOCKED }),
    ).not.toThrow()
    expect(state.reportEvents).not.toHaveBeenCalled()
  })
})
