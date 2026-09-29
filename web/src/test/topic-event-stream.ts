import { create } from '@bufbuild/protobuf'

import {
  Control,
  EventSchema,
  type Event as WatchEvent,
} from '../gen/proto/aladdin/events/v1/events_pb'

/**
 * 一条**可由用例驱动**的事件流：订阅方（use-watch）从它读到什么，完全由用例决定。
 *
 * 它替代不了真实传输，只替代"事件什么时候到、什么时候断"这一件事——而那正是
 * 订阅策略（重连、退避、退订）唯一的输入。跨模块共享一份是因为两个用例文件都要
 * 用它：一个测重连与退避，一个测事件到达之后页面做了什么。
 */
export interface FakeTopicStream {
  /** 传给 `watchTopics` 的返回值：订阅方读的就是它。 */
  stream: AsyncIterable<WatchEvent>
  /** 推一条"这条主题变了"。 */
  publish(topic: string): void
  /** 推一条控制事件（心跳 / RESYNC）。 */
  control(kind: Control, topic?: string): void
  /** 让这条流结束（寿命到点、对端断开——订阅方对两者的处理相同：重连）。 */
  end(): void
  /** 让这条流**以失败结束**（订阅方据此决定重连还是不重连）。 */
  fail(error: unknown): void
  /** 订阅方是否已经退订（中止信号）。 */
  readonly aborted: boolean
}

/** 造一条假的事件流；`signal` 被中止时它也随之结束。 */
export function fakeTopicStream(signal: AbortSignal): FakeTopicStream {
  const queue: WatchEvent[] = []
  let wake: (() => void) | null = null
  let ended = false
  let aborted = false
  let failure: unknown = null

  const finish = (error: unknown = null): void => {
    ended = true
    failure = error
    wake?.()
    wake = null
  }
  signal.addEventListener('abort', () => {
    aborted = true
    finish()
  })

  const stream: AsyncIterable<WatchEvent> = {
    [Symbol.asyncIterator](): AsyncIterator<WatchEvent> {
      return {
        next: async (): Promise<IteratorResult<WatchEvent>> => {
          while (queue.length === 0 && !ended) {
            await new Promise<void>((resolve) => {
              wake = resolve
            })
          }
          if (queue.length === 0 && failure !== null) {
            throw failure
          }
          const value = queue.shift()
          return value === undefined ? { done: true, value: undefined } : { done: false, value }
        },
      }
    },
  }

  const enqueue = (event: WatchEvent): void => {
    queue.push(event)
    wake?.()
    wake = null
  }

  return {
    stream,
    publish: (topic) => enqueue(create(EventSchema, { topic, control: Control.UNSPECIFIED })),
    control: (kind, topic = '') => enqueue(create(EventSchema, { topic, control: kind })),
    end: () => finish(),
    fail: (error) => finish(error),
    get aborted() {
      return aborted
    },
  }
}
