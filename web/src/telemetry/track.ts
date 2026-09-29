import { create } from '@bufbuild/protobuf'

import { telemetryClient } from '../api/transport'
import {
  Client,
  EventSchema,
  Result,
  type Action,
  type Event,
  type Surface,
} from '../gen/proto/aladdin/telemetry/v1/telemetry_pb'
import { EventBuffer } from './event-buffer'

/**
 * 前端客户端事件的**唯一上报入口**（见 docs/observability.md 的「客户端事件」）。
 *
 * 它回答的是服务端请求留痕答不了的问题：那些**根本没发出请求**的动作——前端
 * 拦截下来的保存、按上限早退的上传、切预览、开弹层。已经打到服务端的 RPC 由
 * 服务端中间件留痕（含 client 与 trace_id），这里**不重复上报**。
 *
 * 三条硬性约束：
 *
 *   - **绝不抛异常、绝不阻塞调用方**。上报失败只写 console.debug；一次遥测故障
 *     不能影响任何一次界面操作。
 *   - **只上报清单内的动作**（proto 的 Action 枚举）与有界属性。禁止写入请求体、
 *     凭证、本地路径、用户输入的自由文本。
 *   - **失败类动作即时发送**（result ≠ ok），成功类攒批。失败后常常紧跟一次跳转
 *     或重试，等批次会把它丢在页面卸载之前。
 */

/** 一条待上报的事件。`client` 由本模块填成 web，调用方不填。 */
export interface TrackedEvent {
  surface: Surface
  action: Action
  result: Result
  // 三个可选字段显式允许 undefined：调用方常用 `traceIdOf(err) ?? undefined`
  // 这类表达式，而 tsconfig 开了 exactOptionalPropertyTypes。
  durationMs?: number | undefined
  /** 若这次动作伴随一次 RPC，带上它的 trace_id 以便与服务端留痕关联。 */
  traceId?: string | undefined
  /** 有界属性。键与取值都由服务端的白名单与取值域约束（见 internal/telemetry）。 */
  attrs?: Record<string, string> | undefined
}

let buffer: EventBuffer | null = null
let triggersInstalled = false

/** 上报一条客户端事件。 */
export function track(event: TrackedEvent): void {
  const pending = ensureBuffer()
  pending.add(toWireEvent(event))
  if (event.result !== Result.OK) {
    // 失败类动作立即发：等批次会把它丢在紧随其后的跳转或重试里。
    pending.flush()
  }
}

/** 把调用方的输入补成一个完整的线格式事件。 */
function toWireEvent(event: TrackedEvent): Event {
  return create(EventSchema, {
    client: Client.WEB,
    surface: event.surface,
    action: event.action,
    result: event.result,
    durationMs: event.durationMs ?? 0,
    traceId: event.traceId ?? '',
    attrs: event.attrs ?? {},
  })
}

/**
 * 惰性构造缓冲并**只装一次**页面生命周期触发器。
 *
 * 惰性而不是在模块加载时构造：纯静态导入不该有副作用，而 flush 触发器又必须在
 * 真的有人上报之后才有意义。
 */
function ensureBuffer(): EventBuffer {
  if (buffer === null) {
    buffer = new EventBuffer(sendBatch)
  }
  if (!triggersInstalled) {
    installFlushTriggers()
    triggersInstalled = true
  }
  return buffer
}

/**
 * 发送一批事件。
 *
 * 传输层尚未初始化、或请求失败，都只写 console.debug 并丢弃——**P0 没有离线队列**，
 * 缓冲仅内存、丢了就丢（见 docs/observability.md）。
 */
function sendBatch(events: Event[]): void {
  let call: Promise<unknown>
  try {
    call = telemetryClient().reportEvents({ events })
  } catch (error) {
    // 传输层未初始化时 telemetryClient() 会同步抛出。这里吞掉：一次遥测上报
    // 绝不能让界面操作失败。
    console.debug('客户端事件未能上报（传输层未就绪，已丢弃）', error)
    return
  }
  void call.catch((error: unknown) => {
    console.debug('客户端事件上报失败（已丢弃）', error)
  })
}

/**
 * 页面隐藏/卸载时把最后一批发出去。
 *
 * 两条都要：`pagehide` 覆盖导航离开与关闭，`visibilitychange` 覆盖切后台
 * （移动端常在这种情况下直接杀掉页面，等不到 pagehide）。
 */
function installFlushTriggers(): void {
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'hidden') {
      buffer?.flush()
    }
  })
  window.addEventListener('pagehide', () => buffer?.flush())
}
