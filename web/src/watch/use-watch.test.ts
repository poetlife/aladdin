import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { Code, ConnectError } from '@connectrpc/connect'

import * as eventsApi from '../api/events'
import * as transport from '../api/transport'
import { Control } from '../gen/proto/aladdin/events/v1/events_pb'
import { fakeTopicStream, type FakeTopicStream } from '../test/topic-event-stream'
import { STALL_TIMEOUT_MS, useWatch } from './use-watch'

vi.mock('../api/events', () => ({
  watchTopics: vi.fn(),
}))

vi.mock('../api/transport', () => ({
  notifyUnauthenticated: vi.fn(),
}))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

/**
 * 退避的抖动是随机的，因此断言只能落在**区间**上：第一次重连的等待在
 * [500, 1000] 毫秒之间（基数是 1 秒），第二次在 [1000, 2000] 之间。
 */
const FIRST_RECONNECT_MAX_MS = 1_000
const SECOND_RECONNECT_MIN_MS = 1_000
const SECOND_RECONNECT_MAX_MS = 2_000

let root: Root | null = null
let container: HTMLElement | null = null

/** 探针：把 hook 装进一棵真的 React 树里，回调记进数组。 */
function probe(topics: string[], seen: string[]) {
  function Probe({ subscribed }: { subscribed: string[] }) {
    useWatch(subscribed, (topic) => seen.push(topic))
    return null
  }
  return createElement(Probe, { subscribed: topics })
}

/** 渲染探针，并把这一页开出来的每一条流收进 streams。 */
async function renderProbe(
  streams: FakeTopicStream[],
  seen: string[],
  topics: string[],
): Promise<void> {
  vi.mocked(eventsApi.watchTopics).mockImplementation((_topics, signal) => {
    const fake = fakeTopicStream(signal)
    streams.push(fake)
    return fake.stream
  })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(probe(topics, seen))
  })
}

/** 换一组订阅主题（同一棵树重新渲染）。 */
async function renderTopics(topics: string[], seen: string[]): Promise<void> {
  await act(async () => {
    root?.render(probe(topics, seen))
  })
}

/** 让挂起的微任务（取事件、回调、重连）走完，但不推进定时器到重连那一刻。 */
async function flush(): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0)
  })
}

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  container?.remove()
  container = null
  vi.useRealTimers()
  vi.clearAllMocks()
})

describe('事件订阅', () => {
  it('收到"某条主题变了"即回调，参数是那条主题', async () => {
    const streams: FakeTopicStream[] = []
    const seen: string[] = []
    await renderProbe(streams, seen, ['galaxy.project/甲'])

    streams[0]?.publish('galaxy.project/甲')
    await flush()
    expect(seen).toEqual(['galaxy.project/甲'])
  })

  it('RESYNC 也回调：它要求的正是"重拉这条主题"', async () => {
    const streams: FakeTopicStream[] = []
    const seen: string[] = []
    await renderProbe(streams, seen, ['galaxy.project/甲'])

    // 服务端在订阅成功之后为每条主题各发一条 RESYNC——客户端据此做首次全量拉取。
    streams[0]?.control(Control.RESYNC, 'galaxy.project/甲')
    await flush()
    expect(seen).toEqual(['galaxy.project/甲'])
  })

  it('心跳不是状态变化，不回调', async () => {
    const streams: FakeTopicStream[] = []
    const seen: string[] = []
    await renderProbe(streams, seen, ['galaxy.project/甲'])

    streams[0]?.control(Control.HEARTBEAT)
    await flush()
    expect(seen, '心跳被当成了状态变化').toEqual([])
  })

  it('集合变化即重开一条：旧的中止，新的一条订的是新集合', async () => {
    const streams: FakeTopicStream[] = []
    const seen: string[] = []
    await renderProbe(streams, seen, ['galaxy.project/甲'])
    expect(streams.length).toBe(1)

    await renderTopics(['galaxy.project/乙'], seen)
    await flush()

    expect(streams.length, '集合变了却没有重开一条').toBe(2)
    expect(streams[0]?.aborted, '旧的那条没有被中止').toBe(true)
    // 新的那条订的是新集合——服务端只推它订过的主题。
    expect(vi.mocked(eventsApi.watchTopics).mock.calls[1]?.[0]).toEqual(['galaxy.project/乙'])
  })

  it('空集合不订阅', async () => {
    const streams: FakeTopicStream[] = []
    await renderProbe(streams, [], [])
    expect(streams.length).toBe(0)
    expect(eventsApi.watchTopics).not.toHaveBeenCalled()
  })
})

describe('断线与重连', () => {
  it('流结束即重连：退避到点之前不重连，到点之后重连一条新的', async () => {
    const streams: FakeTopicStream[] = []
    await renderProbe(streams, [], ['galaxy.project/甲'])
    expect(streams.length).toBe(1)

    // 寿命到点、对端断开——订阅方对它们的处理相同：重连（并因此重拉）。
    streams[0]?.end()
    await flush()
    expect(streams.length, '退避到点之前就重连了').toBe(1)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(FIRST_RECONNECT_MAX_MS)
    })
    expect(streams.length, '退避到点之后没有重连').toBe(2)
  })

  it('一直连不上时退避逐次加大', async () => {
    const streams: FakeTopicStream[] = []
    await renderProbe(streams, [], ['galaxy.project/甲'])

    streams[0]?.end()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(FIRST_RECONNECT_MAX_MS)
    })
    expect(streams.length).toBe(2)

    // 第二条也断了：这次要等得更久（基数翻倍）。
    streams[1]?.end()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(SECOND_RECONNECT_MIN_MS - 1)
    })
    expect(streams.length, '退避没有变大，第二次重连来得和第一次一样快').toBe(2)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(SECOND_RECONNECT_MAX_MS)
    })
    expect(streams.length).toBe(3)
  })

  it('重试无意义的失败不重连', async () => {
    const streams: FakeTopicStream[] = []
    await renderProbe(streams, [], ['galaxy.project/甲'])

    // 比如：权限被收回、主题类型没注册过。退避再多次也是同一个结论。
    streams[0]?.fail(new ConnectError('没有权限', Code.PermissionDenied))
    await act(async () => {
      await vi.advanceTimersByTimeAsync(SECOND_RECONNECT_MAX_MS * 10)
    })
    expect(streams.length, '对一次不会变的失败反复重连').toBe(1)
  })

  it('会话失效交给会话层，不再重连', async () => {
    const streams: FakeTopicStream[] = []
    await renderProbe(streams, [], ['galaxy.project/甲'])

    // 流上的错误不会被传输层的拦截器看到（拦截器只包住"建立调用"），因此会话
    // 失效要在这里收口，否则一次吊销既不会引导重新登录、又会被无限重连。
    streams[0]?.fail(new ConnectError('凭证已失效', Code.Unauthenticated))
    await act(async () => {
      await vi.advanceTimersByTimeAsync(SECOND_RECONNECT_MAX_MS * 10)
    })

    expect(vi.mocked(transport.notifyUnauthenticated)).toHaveBeenCalled()
    expect(streams.length, '会话已失效却还在重连').toBe(1)
  })

  it('网络类失败仍然重连', async () => {
    const streams: FakeTopicStream[] = []
    await renderProbe(streams, [], ['galaxy.project/甲'])

    streams[0]?.fail(new TypeError('Failed to fetch'))
    await act(async () => {
      await vi.advanceTimersByTimeAsync(FIRST_RECONNECT_MAX_MS)
    })
    expect(streams.length, '一次网络中断之后不再重连').toBe(2)
  })

  it('卸载即中止订阅，且不再重连', async () => {
    const streams: FakeTopicStream[] = []
    await renderProbe(streams, [], ['galaxy.project/甲'])

    await act(async () => {
      root?.unmount()
    })
    root = null

    expect(streams[0]?.aborted, '卸载之后那条流还挂着').toBe(true)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(SECOND_RECONNECT_MAX_MS * 10)
    })
    expect(streams.length, '页面已经走了，却又开了一条流').toBe(1)
  })
})

describe('静默判活：连接死了而没有任何一方报错', () => {
  /**
   * 半开的连接——笔记本睡眠、NAT 表项过期、中途换网——**两边都不会报错**：服务端的
   * 心跳写不出去（它只是重传），客户端也永远读不到任何东西。不报错就不会重连，
   * 不重连就没有 RESYNC，页面于是可以无限期停在旧状态上。这一组钉的就是这条判活
   * （见 docs/design/events/README.md 的"页面隐藏时"）。
   */
  it('静默到判死就主动断开，重连之后依 RESYNC 重拉', async () => {
    const streams: FakeTopicStream[] = []
    const seen: string[] = []
    await renderProbe(streams, seen, ['galaxy.project/甲'])

    // 这条连接此后再无动静：没有事件，也没有心跳。
    await act(async () => {
      await vi.advanceTimersByTimeAsync(STALL_TIMEOUT_MS)
    })
    expect(streams[0]?.aborted, '静默了这么久却没有断开').toBe(true)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(FIRST_RECONNECT_MAX_MS)
    })
    expect(streams.length, '断开了却没有重连').toBe(2)

    // 重连之后服务端为每个主题各发一条 RESYNC——重拉就是从这里来的。
    streams[1]?.control(Control.RESYNC, 'galaxy.project/甲')
    await flush()
    expect(seen).toEqual(['galaxy.project/甲'])
  })

  it('判的是"这条连接还有没有动静"，不是"有没有变更"', async () => {
    const streams: FakeTopicStream[] = []
    const seen: string[] = []
    await renderProbe(streams, seen, ['galaxy.project/甲'])

    // 一直只有心跳：一个变更都没有，但它证明这条连接还是通的。
    for (let round = 0; round < 4; round += 1) {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(STALL_TIMEOUT_MS * 0.8)
      })
      streams[0]?.control(Control.HEARTBEAT)
      await flush()
    }

    expect(streams[0]?.aborted, '一直有心跳，却被判死了').toBe(false)
    expect(streams.length).toBe(1)
    expect(seen, '心跳被当成了状态变化').toEqual([])
  })
})
