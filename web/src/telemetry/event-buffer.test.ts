import { create } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { EventSchema, type Event } from '../gen/proto/aladdin/telemetry/v1/telemetry_pb'
import { EventBuffer, FLUSH_INTERVAL_MS, MAX_BATCH } from './event-buffer'

/** 造一条占位事件——缓冲只搬运它，不看内容。 */
function stubEvent(): Event {
  return create(EventSchema, {})
}

describe('EventBuffer', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('攒够上限立即发送，不等时间窗', () => {
    const send = vi.fn()
    const buffer = new EventBuffer(send)
    for (let i = 0; i < MAX_BATCH; i++) {
      buffer.add(stubEvent())
    }
    expect(send).toHaveBeenCalledTimes(1)
    expect(send.mock.calls[0]?.[0]).toHaveLength(MAX_BATCH)
  })

  it('没攒够时由时间窗触发', () => {
    const send = vi.fn()
    const buffer = new EventBuffer(send)
    buffer.add(stubEvent())
    expect(send).not.toHaveBeenCalled()

    vi.advanceTimersByTime(FLUSH_INTERVAL_MS)
    expect(send).toHaveBeenCalledTimes(1)
    expect(send.mock.calls[0]?.[0]).toHaveLength(1)
  })

  it('时间窗按第一批起算，后续事件不会把它一直往后推', () => {
    const send = vi.fn()
    const buffer = new EventBuffer(send)
    buffer.add(stubEvent())
    vi.advanceTimersByTime(FLUSH_INTERVAL_MS / 2)
    buffer.add(stubEvent())

    vi.advanceTimersByTime(FLUSH_INTERVAL_MS / 2)
    expect(send).toHaveBeenCalledTimes(1)
  })

  it('flush 立即发送并清空；再 flush 不发空批', () => {
    const send = vi.fn()
    const buffer = new EventBuffer(send)
    buffer.add(stubEvent())
    buffer.add(stubEvent())

    buffer.flush()
    expect(send).toHaveBeenCalledTimes(1)
    expect(send.mock.calls[0]?.[0]).toHaveLength(2)

    buffer.flush()
    expect(send).toHaveBeenCalledTimes(1)
  })

  it('flush 之后新来的事件属于下一批，不会被清掉', () => {
    const send = vi.fn()
    const buffer = new EventBuffer(send)
    buffer.add(stubEvent())
    buffer.flush()
    buffer.add(stubEvent())
    buffer.add(stubEvent())

    buffer.flush()
    expect(send).toHaveBeenCalledTimes(2)
    expect(send.mock.calls[1]?.[0]).toHaveLength(2)
  })
})
