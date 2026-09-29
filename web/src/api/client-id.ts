/**
 * 上报端标识的常量。**它们与服务端的 observability 包同源**，改动时必须同步：
 * 头名是 `observability.HeaderClient`，取值是 `observability.ClientWeb` /
 * `ClientCLI`（见 docs/observability.md）。
 *
 * 为什么前端也要有一份：TypeScript 引用不了 Go 常量，这与 traceparent 的
 * 常量是同一处取舍（见 ./trace-context.ts）。
 */

/** 上报端标识在请求头中的名字。 */
export const CLIENT_HEADER = 'x-aladdin-client'

/** 浏览器端的取值。它必须与客户端事件里 `Client.WEB` 的日志取值一致。 */
export const CLIENT_WEB = 'web'
