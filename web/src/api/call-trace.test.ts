import { describe, expect, it } from 'vitest'
import { Code, ConnectError, createClient } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'

import { captureTrace, traceCallOptions, traceIdForAction } from './call-trace'
import { RBACService } from '../gen/proto/aladdin/rbac/v1/rbac_pb'

const traceID = '4bf92f3577b34da6a3ce929d0e0e4736'
const otherTraceID = 'aaaabbbbccccddddeeeeffff00001111'
const traceparent = `00-${traceID}-0edd0d76afec1634-01`

describe('captureTrace', () => {
  it('从 x-trace-id 取值', () => {
    const capture = captureTrace()
    capture.onHeader(new Headers({ 'x-trace-id': traceID }))
    expect(capture.traceId).toBe(traceID)
  })

  // 前后端可以独立部署：后端还没升级时不回 x-trace-id，那就退回解析 traceparent。
  it('x-trace-id 缺失时回退到解析 traceparent', () => {
    const capture = captureTrace()
    capture.onHeader(new Headers({ traceparent }))
    expect(capture.traceId).toBe(traceID)
  })

  // 请求没走到服务端时没有这些头。此时必须保持 null，而不是编一个 ID 出来——
  // 编出来的 ID 在日志里查不到，会把排障引向"日志丢了"的错误结论。
  it('两个头都没有时保持 null', () => {
    const capture = captureTrace()
    capture.onHeader(new Headers())
    expect(capture.traceId).toBeNull()
  })

  // 这是"每次调用一份"的全部理由：页面加载会同时打两三个 RPC，共用一个
  // "最近一次 trace_id"会让归属变成由时序决定的偶然值。
  it('两次捕获互不干扰', () => {
    const first = captureTrace()
    const second = captureTrace()
    first.onHeader(new Headers({ 'x-trace-id': traceID }))
    second.onHeader(new Headers({ 'x-trace-id': otherTraceID }))
    expect(first.traceId).toBe(traceID)
    expect(second.traceId).toBe(otherTraceID)
  })
})

describe('traceCallOptions', () => {
  it('没有捕获点时不给调用选项，走客户端默认行为', () => {
    expect(traceCallOptions(undefined)).toBeUndefined()
  })

  it('捕获点的 onHeader 只写回自己那一份', () => {
    const capture = captureTrace()
    traceCallOptions(capture)?.onHeader?.(new Headers({ 'x-trace-id': traceID }))
    expect(capture.traceId).toBe(traceID)
  })
})

describe('traceIdForAction', () => {
  it('成功时用捕获到的值', () => {
    const capture = captureTrace()
    capture.onHeader(new Headers({ 'x-trace-id': traceID }))
    expect(traceIdForAction(capture)).toBe(traceID)
  })

  // 失败时出错的那一次才是问题所在，它比"第一次调用"更值得直接定位。
  it('失败时优先用出错那一次调用的', () => {
    const capture = captureTrace()
    capture.onHeader(new Headers({ 'x-trace-id': traceID }))
    const error = new ConnectError(
      '权限不足',
      Code.PermissionDenied,
      new Headers({ 'x-trace-id': otherTraceID }),
    )
    expect(traceIdForAction(capture, error)).toBe(otherTraceID)
  })

  it('失败但错误里没有链路标识时，退回捕获到的那个', () => {
    const capture = captureTrace()
    capture.onHeader(new Headers({ 'x-trace-id': traceID }))
    expect(traceIdForAction(capture, new Error('草稿校验没过'))).toBe(traceID)
  })

  it('两者都没有时返回 undefined', () => {
    expect(traceIdForAction(captureTrace())).toBeUndefined()
    expect(traceIdForAction(captureTrace(), new ConnectError('网络中断', Code.Unavailable))).toBe(
      undefined,
    )
  })
})

/**
 * 走一遍**真实的 connect-es 调用链**验成功路径。
 *
 * 上面的用例只验证捕获点自己的行为；而"成功的响应到底有没有把 trace_id 交到调用方
 * 手里"取决于 connect-es 是否真的按 CallOptions.onHeader 回吐响应头。那条接线不测
 * 就没人守——它一旦不成立，页面上那一列会静默地全空，看起来像"服务端没回写"。
 */
describe('成功路径接在真实响应上', () => {
  const respondWith = (headers: Record<string, string>) =>
    createClient(
      RBACService,
      createConnectTransport({
        baseUrl: 'http://aladdin.test',
        fetch: (() =>
          Promise.resolve(
            new Response(JSON.stringify({}), {
              status: 200,
              headers: { 'Content-Type': 'application/json', ...headers },
            }),
          )) as typeof globalThis.fetch,
      }),
    )

  it('成功的响应同样把 trace_id 交到调用方手里', async () => {
    const capture = captureTrace()

    await respondWith({ 'x-trace-id': traceID }).listRoles(
      { scope: 'tenant/acme' },
      traceCallOptions(capture),
    )

    expect(capture.traceId).toBe(traceID)
  })

  it('没有捕获点的调用照常完成，且不写进别人那份捕获', async () => {
    const capture = captureTrace()

    await respondWith({ 'x-trace-id': traceID }).listRoles({ scope: 'tenant/acme' })

    expect(capture.traceId).toBeNull()
  })
})
