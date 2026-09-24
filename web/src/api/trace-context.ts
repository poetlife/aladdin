/**
 * W3C Trace Context 的生成与解析。
 *
 * 本文件是前端链路标识的**唯一实现处**（见 docs/ssot-registry.md）：
 * 传输层与错误展示都调用它，组件不得自行拼 traceparent。
 *
 * 前端**不引入 OTel SDK**：这里只做标准要求的生成与校验，负责传播与关联，
 * 不上报。理由与完整约定见 docs/observability.md。
 */

/** 传播头名，与服务端 observability.TraceparentHeader 一致。 */
export const TRACEPARENT_HEADER = 'traceparent'

/**
 * 给人看的**响应**头名，与服务端 observability.ResponseTraceIDHeader 一致。
 *
 * 只在响应方向使用。请求方向的传播只发 traceparent——把这个头当成可发送的
 * 传播载体，就等于退回"自定义头当传播协议"，标准组件再也接不上链路。
 */
export const TRACE_ID_HEADER = 'x-trace-id'

/** 解析后的链路上下文。 */
export interface TraceContext {
  traceId: string
  spanId: string
  sampled: boolean
}

/** trace-id 的十六进制位数。 */
const TRACE_ID_HEX = 32
/** span-id 的十六进制位数。 */
const SPAN_ID_HEX = 16
/** flags 的十六进制位数。 */
const FLAGS_HEX = 2

const ZERO_TRACE_ID = '0'.repeat(TRACE_ID_HEX)
const ZERO_SPAN_ID = '0'.repeat(SPAN_ID_HEX)

/**
 * 生成一个 traceparent。
 *
 * 返回 null 表示当前环境拿不到密码学随机数。此时**不发这个头**，
 * 而不是退化成 `Math.random()`：后者会产出格式非法的 trace_id 写进服务端
 * 日志，破坏所有基于它的检索，而服务端还会把它当作一条真实链路记录下来。
 * 缺这个头的后果仅仅是服务端自己起一条链路，可接受得多。
 */
export function newTraceparent(): string | null {
  const traceId = randomHex(TRACE_ID_HEX)
  const spanId = randomHex(SPAN_ID_HEX)
  if (traceId === null || spanId === null) {
    return null
  }
  return `00-${traceId}-${spanId}-01`
}

/**
 * 解析并校验一个 traceparent。
 *
 * 校验项与规范一致：版本为 `00`、trace-id 与 span-id 都是**小写**十六进制且
 * 非全零、flags 为两位十六进制。不合规一律返回 null，调用方据此忽略该值。
 *
 * 严格校验服务端回写的值看似多余，其实不然：这个 ID 最终会展示给用户并被
 * 拿去检索日志，放行一个畸形值只会让人在日志里找不到东西。
 */
export function parseTraceparent(value: string | null | undefined): TraceContext | null {
  if (!value) {
    return null
  }
  const parts = value.trim().split('-')
  if (parts.length !== 4) {
    return null
  }

  // 逐段取值后显式判空，而不是依赖上面的长度判断：长度检查一旦被改动
  // （例如允许将来带额外字段的版本），解构出的 undefined 会一路流到返回值里。
  const [version, traceId, spanId, flags] = parts
  if (version !== '00') {
    return null
  }
  if (traceId === undefined || spanId === undefined || flags === undefined) {
    return null
  }
  if (!isLowerHex(traceId, TRACE_ID_HEX) || traceId === ZERO_TRACE_ID) {
    return null
  }
  if (!isLowerHex(spanId, SPAN_ID_HEX) || spanId === ZERO_SPAN_ID) {
    return null
  }
  if (!isLowerHex(flags, FLAGS_HEX)) {
    return null
  }
  return {
    traceId,
    spanId,
    sampled: (Number.parseInt(flags, 16) & 0x01) === 0x01,
  }
}

/** 报告字符串是否为指定长度的小写十六进制。 */
function isLowerHex(value: string, length: number): boolean {
  return value.length === length && /^[0-9a-f]+$/.test(value)
}

/**
 * 校验一个"裸" trace-id。
 *
 * 服务端的 x-trace-id 响应头给的就是这个形式：32 位小写十六进制、非全零。
 * 不合规返回 null，调用方据此忽略这个值而不是把畸形 ID 展示给用户。
 */
export function parseTraceID(value: string | null | undefined): string | null {
  if (!value) {
    return null
  }
  const trimmed = value.trim()
  if (!isLowerHex(trimmed, TRACE_ID_HEX) || trimmed === ZERO_TRACE_ID) {
    return null
  }
  return trimmed
}

/** 取 n 字节的随机十六进制；环境不提供密码学随机数时返回 null。 */
function randomHex(hexLength: number): string | null {
  const webCrypto = globalThis.crypto
  if (webCrypto === undefined || typeof webCrypto.getRandomValues !== 'function') {
    return null
  }
  const buffer = new Uint8Array(hexLength / 2)
  webCrypto.getRandomValues(buffer)
  return Array.from(buffer, (byte) => byte.toString(16).padStart(2, '0')).join('')
}
