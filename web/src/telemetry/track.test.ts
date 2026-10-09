import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

/** 一次上报请求的形状（只关心本用例断言的那部分）。 */
interface ReportCall {
  events: Array<{
    client: Client
    result: Result
    attrs: Record<string, string>
    durationMs: number
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
import { startTimer, track } from './track'

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

  // 量耗时的两个时刻，以及"怎么算"这件事，都只在这一处实现（见 docs/ssot-registry.md）。
  // 这里钉住它：起点是构造计时器那一刻，终点是 end 那一刻。
  it('startTimer 把量到的耗时随事件一起上报', () => {
    let now = 1000
    const spy = vi.spyOn(performance, 'now').mockImplementation(() => now)
    try {
      const timer = startTimer()
      now = 1250
      timer.end({ surface: Surface.WEB_EDITOR, action: Action.EDITOR_OPEN, result: Result.FAIL })

      expect(batchOf(0)[0]?.durationMs).toBe(250)
    } finally {
      spy.mockRestore()
    }
  })

  // 取整到毫秒，且**不补最小值**：不足半毫秒就是 0，而 0 的语义是"未提供"，页面上
  // 显示「—」。补一个 1 是在编数字——那是这一整列可信度的来源。
  it('耗时取整到毫秒，且不会是一个负数', () => {
    let now = 1000
    const spy = vi.spyOn(performance, 'now').mockImplementation(() => now)
    try {
      const timer = startTimer()

      now = 1000.4
      expect(timer.elapsedMs()).toBe(0)
      now = 1000.6
      expect(timer.elapsedMs()).toBe(1)

      // 单调时钟理论上不会倒退。万一它倒退了，也不该让耗时变成负数。
      now = 900
      expect(timer.elapsedMs()).toBe(0)
    } finally {
      spy.mockRestore()
    }
  })
})
