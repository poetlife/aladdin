import { describe, expect, it } from 'vitest'
import { Code, ConnectError, createClient } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'

import { messageOf, traceIdOf } from './errors'
import { RBACService } from '../gen/proto/aladdin/rbac/v1/rbac_pb'

const traceID = '4bf92f3577b34da6a3ce929d0e0e4736'
const otherTraceID = 'aaaabbbbccccddddeeeeffff00001111'
const traceparent = `00-${traceID}-0edd0d76afec1634-01`

describe('traceIdOf', () => {
  // 服务端同时回写两个头，这里故意让它们不一样，好把"优先取哪个"钉死。
  it('优先取 x-trace-id', () => {
    const error = new ConnectError(
      '权限不足',
      Code.PermissionDenied,
      new Headers({ 'x-trace-id': traceID, traceparent: `00-${otherTraceID}-0edd0d76afec1634-01` }),
    )
    expect(traceIdOf(error)).toBe(traceID)
  })

  // 前后端可以独立部署：后端还没升级时不回 x-trace-id，
  // 那就退回解析 traceparent，而不是显示不出追踪 ID。
  it('x-trace-id 缺失时回退到解析 traceparent', () => {
    const error = new ConnectError('权限不足', Code.PermissionDenied, new Headers({ traceparent }))
    expect(traceIdOf(error)).toBe(traceID)
  })

  it('x-trace-id 非法时也回退到解析 traceparent', () => {
    const error = new ConnectError(
      '权限不足',
      Code.PermissionDenied,
      new Headers({ 'x-trace-id': 'not-a-trace-id', traceparent }),
    )
    expect(traceIdOf(error)).toBe(traceID)
  })

  // 没走到服务端的失败（网络中断、被浏览器拦下）没有这些头。
  // 此时必须返回 null，而不是编一个 ID 出来——编出来的 ID 在日志里查不到，
  // 会把排障引向"日志丢了"的错误结论。
  it('两个头都没有时返回 null', () => {
    expect(traceIdOf(new ConnectError('网络中断', Code.Unavailable))).toBeNull()
  })

  it('两个头都非法时返回 null', () => {
    const error = new ConnectError(
      'x',
      Code.Internal,
      new Headers({ 'x-trace-id': 'bad', traceparent: 'not-a-traceparent' }),
    )
    expect(traceIdOf(error)).toBeNull()
  })

  it('非 Connect 错误也能安全处理', () => {
    expect(traceIdOf(new Error('boom'))).toBeNull()
  })
})

/**
 * 用假 fetch 走一遍**真实的 connect-es 解码链路**。
 *
 * 上面那几条只验证 traceIdOf 自己的解析；而它能不能拿到值，取决于
 * connect-es 是否把响应头放进 ConnectError.metadata。那条接线不测就没人守——
 * 它一旦不成立，前端会静默地取不到 ID，功能看起来像是"做了但没生效"。
 */
describe('traceIdOf 接在真实响应上', () => {
  const respondWith = (headers: Record<string, string>) =>
    createClient(
      RBACService,
      createConnectTransport({
        baseUrl: 'http://aladdin.test',
        fetch: (() =>
          Promise.resolve(
            new Response(JSON.stringify({ code: 'permission_denied', message: '权限不足' }), {
              status: 403,
              headers: { 'Content-Type': 'application/json', ...headers },
            }),
          )) as typeof globalThis.fetch,
      }),
    )

  const call = async (headers: Record<string, string>): Promise<unknown> =>
    respondWith(headers)
      .listRoles({ scope: 'tenant/acme' })
      .catch((e: unknown) => e)

  it('取回服务端回写的 x-trace-id，并给出可展示的文案', async () => {
    const error = await call({ 'x-trace-id': traceID, traceparent })

    expect(traceIdOf(error)).toBe(traceID)
    expect(messageOf(error)).toContain('权限不足')
  })

  it('只有 traceparent 时也能取回（后端未升级的情况）', async () => {
    const error = await call({ traceparent })

    expect(traceIdOf(error)).toBe(traceID)
  })

  it('服务端两个头都没回写时返回 null', async () => {
    const error = await call({})

    expect(traceIdOf(error)).toBeNull()
  })
})
