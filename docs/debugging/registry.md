# Debug Registry（排障索引）

查问题前先检索本表，确认是否有同质症状的既有结论。
同一个症状不应在仓库里出现两套排查结论。

---

| 症状关键词 | 根因摘要 | 排障记录 |
|-----------|---------|---------|
| 管理员登录后进入 /forbidden；接口全放行但界面一个入口都进不去 | 会话权限码里的通配未展开，前端拿到的集合只有 `"*"` | [2026-09-27-admin-login-forbidden.md](records/2026-09-27-admin-login-forbidden.md) |
| 预览窗格启动失败、make 报 `getcwd: Operation not permitted`（同一 shell 里 npm 却能跑） | 预览沙箱的进程无法 `getcwd`，而 make 启动即调用它；launch.json 只能放不依赖 `getcwd` 的直接命令 | [2026-09-28-preview-sandbox-cannot-run-make.md](records/2026-09-28-preview-sandbox-cannot-run-make.md) |
| 前端测试在本机一条告警都没有、CI 上却刷屏 | vitest 5 的默认 reporter 不打印通过用例的 console 输出；用 `--reporter=verbose` 才复现 | [2026-09-29-local-tests-hide-console-output.md](records/2026-09-29-local-tests-hide-console-output.md) |
| 命令行报 `read server preface` / `frame header looked like an HTTP/1.1 header` | 对面回的是 HTTP/1.1，说明目标地址上不是 RPC 端点（默认地址是本机 9090，可能被别的服务占着） | [2026-09-29-cli-http1-preface-error.md](records/2026-09-29-cli-http1-preface-error.md) |
| 命令行经反向代理调用：成功正常、**所有错误**变成一个空的 `Unknown` | 反代吞掉了空正文响应的 gRPC trailers（nginx 1.24 对 connect-go 的错误响应形状处理不了）；命令行因此走 Connect 而非原生 gRPC | [2026-09-29-grpc-trailers-dropped-by-nginx.md](records/2026-09-29-grpc-trailers-dropped-by-nginx.md) |
| 流式方法（含 grpc 反射）一律返回 `Internal … does not implement http.Flusher`，同一服务的 unary 方法却正常 | 中间件包装了 `ResponseWriter` 而没实现 `Flusher`；connect-go 取 Flusher 走**直接类型断言**，`Unwrap` 帮不上忙 | [2026-09-29-streaming-internal-flusher.md](records/2026-09-29-streaming-internal-flusher.md) |
| 遥测管理页「追踪 ID」列成片为空，而服务端响应头里明明有 | 前端取 trace_id 的入口（`traceIdOf`）长在错误模块里，只吃 `ConnectError.metadata`；**成功的响应头从来没被读**。按调用捕获即可（`captureTrace`） | [2026-10-07-telemetry-trace-id-empty.md](records/2026-10-07-telemetry-trace-id-empty.md) |
| `skill add` / `skill sync` 在 30 秒处被掐掉，报 `deadline_exceeded`，服务端日志却是 **503** | 取回是 `3 + 文件数` 次**串行**出站请求（约 0.7 秒一次），而 CLI 的默认超时是给一次普通调用的 30 秒——按 spec 的原样命令纳管首批三件，两件必然超时。那个 503 是调用方的 deadline 到期，不是远端故障 | [2026-10-07-skill-add-times-out-at-30s.md](records/2026-10-07-skill-add-times-out-at-30s.md) |

---

## 按端检索提示

aladdin 是三端仓库，同一个"看不到数据"的表象可能来自三个完全不同的地方。定位时先按端收敛：

| 表象 | 先查 |
|------|------|
| 前端按钮消失 / 菜单不显示 | 前端会话权限码集合是否为空 → 服务端是否返回了权限码（见 [frontend-permissions](../design/rbac/frontend-permissions.md)） |
| 前端请求 403 / PermissionDenied | 服务端鉴权拦截器的 `reason` 字段，不要先怀疑前端 |
| CLI 报权限不足 | CLI 凭证是否过期 / 目标环境是否配错；`aladdin --debug` 打印的决策链路 |
| CLI 报连接层错误（preface / frame / header / connection refused） | 先分"对面有服务但不是 RPC 端点"与"对面没服务"，再核对 `aladdin --debug` 解析出的地址是不是你以为的那个 |
| CLI 报 `deadline_exceeded`，服务端日志同一时刻出现 **503** | 先看 `duration_ms` 是否紧贴超时值：是则**是超时，不是远端故障**（那个 503 是调用方 ctx 到期被误归类成"远端不可达"）。耗时随请求数增长的命令另有更长的内置默认值，`--timeout` 可覆盖 |
| 服务端整体拒绝所有请求 | 认证拦截器（身份提取）先于鉴权拦截器，先确认身份是否解析成功 |
| 服务端重启后角色/授权全没了 | 先确认启动日志里的数据库定位信息是不是你以为的那个库——默认是**工作目录**下的 `aladdin.db`，在不同目录启动就会连到不同的库（见 [persistence](../design/persistence/schema.md)） |
| 服务端启动即退出、报结构或版本错误 | 库结构与二进制不匹配：查看日志里的迁移结论；未知版本会被拒绝启动（见 [persistence](../design/persistence/README.md)） |
