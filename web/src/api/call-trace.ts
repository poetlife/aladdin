import type { CallOptions } from '@connectrpc/connect'

import { traceIdOf } from './errors'
import { traceIdFromHeaders } from './trace-context'

/**
 * 一次调用的链路标识捕获。
 *
 * 前端拿到服务端 trace_id 有两条路，各覆盖一半：
 *
 *   - **失败**：链路标识随错误对象回来，`traceIdOf(error)` 即可（见 ./errors）；
 *   - **成功**：返回值就是消息本身，不带任何响应头。服务端**每一条**响应都写了
 *     （成功也一样，见 internal/server 的处理中间件），但客户端不主动去读就丢了。
 *
 * 本模块补的是后一半：把捕获点作为调用选项交给客户端，响应头一到就取值。
 *
 * 它是**每次调用一份**的。这不是洁癖：页面加载时同时打两三个 RPC 是常态，用
 * 一个"最近一次 trace_id"的模块级变量会让归属变成由时序决定的偶然值——而链路
 * 标识的全部价值就在于"这一条确实指那次请求"。
 *
 * 它只解决"读得到"，不决定"该不该带"：一条客户端事件只带一个 trace_id 的取舍
 * 见 traceIdForAction。
 */
export interface TraceCapture {
  /**
   * 本次调用拿到的 trace_id。
   *
   * 读的时机是**调用返回之后**。仍为 null 有两种情况，都是正常结论而非故障：
   * 这次调用没走到服务端；或这次调用以错误结束——错误路径不经过 onHeader
   * （见 @connectrpc/connect 的 promise-client），那种情况的链路标识在错误对象上。
   */
  traceId: string | null
  /**
   * 交给调用的 onHeader。
   *
   * 它是这份捕获的**唯一写入口，且只能配给这一次调用**：配错了就会把别人的
   * 链路标识写进来。
   */
  onHeader(headers: Headers): void
}

/** 构造一份只属于某一次调用的链路标识捕获。 */
export function captureTrace(): TraceCapture {
  const capture: TraceCapture = {
    traceId: null,
    onHeader(headers: Headers): void {
      capture.traceId = traceIdFromHeaders(headers)
    },
  }
  return capture
}

/**
 * 把捕获点折成调用选项；没有捕获点时返回 undefined（即客户端默认行为）。
 *
 * 包一层而不是把 `capture.onHeader` 直接传出去：那个方法靠闭包取值，传裸函数
 * 会让"它究竟绑在哪份捕获上"取决于实现细节，而配错的表现是张冠李戴。
 */
export function traceCallOptions(capture: TraceCapture | undefined): CallOptions | undefined {
  if (capture === undefined) {
    return undefined
  }
  return { onHeader: (headers) => capture.onHeader(headers) }
}

/**
 * 取这次**动作**该带上的链路标识。
 *
 * 一条客户端事件只带一个 trace_id（字段只有一个），规则是：
 *
 *   - **失败**：出错那一次调用的。它就是问题所在，比"第一次"更值得直接定位；
 *     错误对象里没有链路标识时（本地校验失败、网络中断）退回捕获到的那个。
 *   - **成功**：这次动作**第一次**调用的。它回答"这次操作是从哪一次请求开始的"，
 *     且不随可选分支变化——同一个页面在有没有配置资产桶、有没有发布域时走的
 *     请求条数不同，取"第一次"才不会让同一条事件在不同部署下指不同的东西。
 *
 * 返回 undefined 表示这次动作没有可指的链路：被前端拦下、确认框被取消、纯前端
 * 切换这类**没有伴随 RPC** 的动作，以及请求根本没发出去的情况。空是正确结论，
 * 不该编一个 ID 出来。
 */
export function traceIdForAction(capture: TraceCapture, error?: unknown): string | undefined {
  if (error !== undefined) {
    return traceIdOf(error) ?? capture.traceId ?? undefined
  }
  return capture.traceId ?? undefined
}
