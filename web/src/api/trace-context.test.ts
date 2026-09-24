import { afterEach, describe, expect, it, vi } from 'vitest'

import { newTraceparent, parseTraceID, parseTraceparent } from './trace-context'

describe('newTraceparent', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('生成的 traceparent 能被自己解析回来，且标记为已采样', () => {
    const value = newTraceparent()
    expect(value).not.toBeNull()

    const parsed = parseTraceparent(value)
    expect(parsed).not.toBeNull()
    expect(parsed?.traceId).toHaveLength(32)
    expect(parsed?.spanId).toHaveLength(16)
    expect(parsed?.sampled).toBe(true)
    expect(value?.startsWith('00-')).toBe(true)
  })

  it('每次生成的都是新链路', () => {
    const seen = new Set<string>()
    for (let i = 0; i < 100; i += 1) {
      seen.add(newTraceparent() ?? '')
    }
    expect(seen.size).toBe(100)
  })

  // 环境拿不到密码学随机数时**不发这个头**，而不是退化成 Math.random：
  // 后者产出的非法 trace_id 会写进服务端日志，而缺头只是让服务端自己起一条链路。
  it('拿不到密码学随机数时返回 null，而不是编一个非法值', () => {
    vi.stubGlobal('crypto', undefined)
    expect(newTraceparent()).toBeNull()
  })
})

describe('parseTraceparent', () => {
  const valid = '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01'

  it('解析合法值并取出各段', () => {
    expect(parseTraceparent(valid)).toEqual({
      traceId: '4bf92f3577b34da6a3ce929d0e0e4736',
      spanId: '00f067aa0ba902b7',
      sampled: true,
    })
  })

  it('flags 的采样位为 0 时报告未采样', () => {
    expect(parseTraceparent('00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00')?.sampled).toBe(
      false,
    )
  })

  it.each([
    ['空值', null],
    ['空串', ''],
    ['段数不对', '00-4bf92f-00f067aa0ba902b7-01'],
    ['版本不是 00', '01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01'],
    ['大写十六进制', '00-4BF92F3577B34DA6A3CE929D0E0E4736-00F067AA0BA902B7-01'],
    ['trace-id 全零', '00-00000000000000000000000000000000-00f067aa0ba902b7-01'],
    ['span-id 全零', '00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01'],
    ['flags 长度不对', '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-1'],
    ['根本不是这个格式', 'abc'],
  ])('拒绝「%s」', (_name, value) => {
    expect(parseTraceparent(value)).toBeNull()
  })
})

describe('parseTraceID', () => {
  it('接受 32 位小写十六进制', () => {
    expect(parseTraceID('4bf92f3577b34da6a3ce929d0e0e4736')).toBe('4bf92f3577b34da6a3ce929d0e0e4736')
  })

  it('容忍两端空白', () => {
    expect(parseTraceID('  4bf92f3577b34da6a3ce929d0e0e4736  ')).toBe('4bf92f3577b34da6a3ce929d0e0e4736')
  })

  it.each([
    ['空值', null],
    ['空串', ''],
    ['长度不对', '4bf92f3577b34da6a3ce929d0e0e473'],
    ['大写', '4BF92F3577B34DA6A3CE929D0E0E4736'],
    ['全零', '00000000000000000000000000000000'],
    ['带 traceparent 前缀', '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01'],
  ])('拒绝「%s」', (_name, value) => {
    expect(parseTraceID(value)).toBeNull()
  })
})
