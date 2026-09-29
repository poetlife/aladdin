# 流式方法一律 Internal：包装过的 ResponseWriter 让 http.Flusher 断言失败

**日期**：2026-09-29
**收敛到的端**：服务端入口——遥测中间件对 `ResponseWriter` 的包装

## 症状

服务端流（订阅通道 `Watch`）在两条协议上都无法开流：

```
rpc error: code = Internal desc = *server.statusRecorder does not implement http.Flusher
```

同一个服务上的 unary 方法**全部正常**，日志里那条请求也是正常的 200。错误信息不是我们
写的——它来自 connect-go 自己。

## 收敛过程

1. 报错点名 `statusRecorder`：那是遥测中间件为了记录状态码给 `ResponseWriter` 套的
   一层包装（[internal/server/middleware.go](../../../internal/server/middleware.go)）。
2. 那层包装**已经**有一个 `Unwrap()`，注释还写着"少了它会直接坏掉流式响应"。于是先怀疑
   是别处的问题，去看 connect-go 到底怎么取 Flusher：

   ```go
   // connect@v1.21.0/protocol.go 的 newStreamingResponseWriter
   if _, flushable := responseWriter.(http.Flusher); requiresFlusher && !flushable {
       return NewError(CodeInternal, fmt.Errorf("%T does not implement http.Flusher", responseWriter))
   }
   ```

   是**直接的类型断言**，不走 `http.ResponseController`。
3. 于是结论清楚：`Unwrap` 只服务于走 `ResponseController` 的调用方（以及标准库的新式
   接口协商），对这里那次直接断言毫无帮助——包装类型必须自己实现 `Flush`。

## 根因

`statusRecorder` 包装了 `ResponseWriter`，但只保留了 `WriteHeader` / `Write`，把
`Flusher` 藏在 `Unwrap` 后面。connect-go 需要 `Flusher` 时直接断言，拿到的是包装类型，
断言失败 → 它按 Internal 拒绝这次流式调用。

**它不只影响新加的那条流。** `grpc.reflection` 也是流式的，走的是同一个中间件，因此
这处断言一直会失败——只是没有测试覆盖到反射服务，而旧注释对失败方式的猜测（"连接挂住
而不是报错"）也不对：真实表现是客户端立刻拿到一个 Internal。

## 结论与边界

- **包装 `ResponseWriter` 的中间件必须显式实现 `http.Flusher`**（以及调用方可能断言的
  其他可选接口：`Hijacker`、`io.ReaderFrom` 等）。`Unwrap` 不是它的替代品，两者服务的
  是两类不同的调用方，缺哪一类就在哪一类上失败。
- 包装层**不能声明一个底层没有的能力**：底层不是 Flusher 时 `Flush` 应当什么都不做，
  而不是 panic，也不是假装刷成功。
- 离线判据：`internal/server` 的 `TestStatusRecorderExposesFlusher`——断言包装类型能满足
  `http.Flusher`，且刷新透传到底层。
- 端到端判据：`test/e2e` 的 `TestWatchSeesChangesOverBothProtocols`——两条协议
  各开一条流；这条链路坏掉时报的就是上面那句 Internal。
