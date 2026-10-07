# 遥测页「追踪 ID」列成片为空：成功的响应头没人读

**日期**：2026-10-07
**收敛到的端**：Web（前端）。服务端与该字段的落库/读侧都正常，问题在"谁来读响应头"

## 症状

`/admin/telemetry` 的明细表里，「追踪 ID」整列都是 `—`，连"工程列表 / 工作台 / 独立预览页"这类
**明显伴随 RPC** 的动作也一样。同时「耗时」「属性」两列也多为空。看起来像"上报时没带这个字段"
或"服务端没回写"。

## 收敛过程

四次排除，逐次把范围缩小：

1. **服务端到底写没写这个头**：对着在跑的实例打一次**成功**的公开调用——
   `curl -i -X POST http://127.0.0.1:9090/aladdin.identity.v1.IdentityService/GetAuthMethods -d '{}'`——
   200 响应里带着 `X-Trace-Id` 与 `Traceparent`。**成功响应也写**（见 `WriteTraceHeaders`，
   它在 `next.ServeHTTP` 之前调用）。所以不是服务端的问题。
2. **写侧与读侧有没有把它弄丢**：`internal/telemetry/sanitize.go` 只校验形状、不合规置空；
   `gormstore` 原样存 `client_trace_id`；管理面原样返回。中间单测里还断言过一个具体值。
   落库与读出这条链路是通的。
3. **前端从哪儿取值**：`traceIdOf(error)` 从 `ConnectError.metadata` 里取——**它只在失败时存在**。
   成功的返回值就是消息本身，不带任何响应头；而传输层的拦截器成功路径是
   `return await next(req)`，从来没读过响应头。响应头一直在，只是没人读。
4. **数一遍调用点**：全仓只有 4 处给事件填了 `traceId`，写法都是
   `error === undefined ? undefined : traceIdOf(error)`，即**只有失败分支**。
   截图里那几行全是"成功"，于是必然是空。

## 根因

**取值入口长在错误模块里**：`traceIdOf` 是随"失败提示里给个可复制的追踪 ID"这个需求一起
写进 `web/src/api/errors.ts` 的，于是它天然只覆盖了失败那一半；成功那一半从来没有人补。

配套的判据（看到这个症状先查什么）：

- **只在成功行上空、失败行上有** → 就是这个症状，不要去查服务端与落库。
- **连失败行也空** → 才去查服务端有没有回写（第 1 步的 curl）、以及是不是跨域部署
  （自定义头 `x-trace-id` 在跨域时默认读不到，必须由反向代理放进
  `Access-Control-Expose-Headers`；本仓库标准部署是同源，不受影响）。
- **blocked / cancel / 纯前端切换这些行永远为空**，那是正确结论：这些动作**没有伴随 RPC**，
  没有"那一次请求"可指，不该编一个 ID 出来。

## 结论与边界

- **取值按调用捕获，不用全局值**。connect-es 的 `CallOptions.onHeader` 是每次调用的回调，用它
  把响应头回填到一个只属于这次调用的捕获点（`web/src/api/call-trace.ts`）。
  禁用"最近一次 trace_id"这类模块级变量——页面加载同时打两三个 RPC 是常态，那会让归属变成
  由时序决定的偶然值。
- **一条事件只带一个 trace_id 的规则**（写在 [docs/observability.md](../../observability.md)
  的「客户端事件」里）：失败带**出错那一次**调用的；成功带这次动作**第一次**调用的；
  没有伴随 RPC 或请求根本没发出去时为空。
- 这条接线必须**有测试守着**：它一旦断了，页面上只会静默地全空，看起来和服务端没回写一模一样。
  因此 `web/src/api/call-trace.test.ts` 里有一条走真实 connect-es 解码链路的成功路径用例，
  页面侧另有一条断言事件真的带上了值。

## 参考

- [web/src/api/call-trace.ts](../../../web/src/api/call-trace.ts)
- [web/src/api/trace-context.ts](../../../web/src/api/trace-context.ts)（响应头 → trace_id 的唯一实现处）
- [internal/observability/tracing.go](../../../internal/observability/tracing.go)（`WriteTraceHeaders`）
- [docs/observability.md](../../observability.md)
