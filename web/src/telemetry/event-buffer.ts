import type { Event } from '../gen/proto/aladdin/telemetry/v1/telemetry_pb'

/** 一批最多带多少条事件：攒够就发。 */
export const MAX_BATCH = 20

/** 第一条事件进来之后，最多等这么久就发一次。 */
export const FLUSH_INTERVAL_MS = 3000

/** 发送一批事件。失败如何处理由调用方决定（P0：丢了就丢）。 */
export type Sender = (events: Event[]) => void

/**
 * EventBuffer 把客户端事件攒成批，按条数与时间触发发送。
 *
 * 为什么要攒批：切预览、开弹层这类 UI 动作会连发，一条一次 RPC 会把请求量与弱网
 * 失败率一起放大（见 docs/observability.md 的「客户端事件」）。攒批的代价是最后
 * 一批可能来不及发出去，因此 caller 必须在页面隐藏时显式 `flush()`。
 *
 * 纯内存：**没有离线队列**，进程结束就丢。P0 刻意如此——为一次可能失败的遥测
 * 上报引入本地持久化，成本远大于它挽回的信息量。
 */
export class EventBuffer {
  private pending: Event[] = []
  private timer: ReturnType<typeof setTimeout> | null = null

  constructor(private readonly send: Sender) {}

  /**
   * 加入一条事件。攒够了立即发送，否则起一个定时器。
   *
   * 「立即」与否由调用方判断（见 track.ts：失败类动作不等批次）——本类型只负责
   * 攒与发，不判断哪些事件值得优先。
   */
  add(event: Event): void {
    this.pending.push(event)
    if (this.pending.length >= MAX_BATCH) {
      this.flush()
      return
    }
    this.arm()
  }

  /** 立即发送当前批次；没有待发事件时什么也不做。 */
  flush(): void {
    this.disarm()
    if (this.pending.length === 0) {
      return
    }
    // 先摘出这一批再发送：发送是异步的，此时若有新事件进来，它应当属于下一批，
    // 而不是被这一次发送悄悄带走（或被清空）。
    const batch = this.pending
    this.pending = []
    this.send(batch)
  }

  private arm(): void {
    if (this.timer !== null) {
      return
    }
    this.timer = setTimeout(() => {
      this.timer = null
      this.flush()
    }, FLUSH_INTERVAL_MS)
  }

  private disarm(): void {
    if (this.timer === null) {
      return
    }
    clearTimeout(this.timer)
    this.timer = null
  }
}
