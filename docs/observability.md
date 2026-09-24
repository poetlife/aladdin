# 可观测性总览

本文档描述 aladdin 可观测性基础设施的接入方式与使用规范。

aladdin 是三端一体仓库，日志有三条独立链路，**互不复用实现**，但共用同一套字段规范与 `trace_id` 传播约定：

| 端 | 入口 | 框架 | 输出去向 |
|----|------|------|---------|
| gRPC 服务端 | `cmd/aladdin-server` | zap | stdout（控制台可读）+ 文件 JSON Lines |
| CLI | `cmd/aladdin` | zap | stderr（**不写文件**，避免污染管道输出） |
| Web 前端 | `web/` | 浏览器 console | 不采集文件；生产环境上报走 `web/src/api/` 下的上报通道 |

## 日志（Logging）

- 框架：**zap**（Go 服务端与 CLI 共用 `internal/observability` 构建的 logger）
- 格式：
  - 控制台：人类可读的纯文本（含时间戳、级别、消息）
  - 文件：结构化 JSON，每行一条记录（JSON Lines）
- JSON Schema（文件输出字段定义）：
  ```json
  {
    "$schema": "http://json-schema.org/draft-07/schema#",
    "type": "object",
    "required": ["timestamp", "level", "message", "trace_id"],
    "properties": {
      "timestamp": { "type": "string", "format": "date-time", "description": "ISO 8601 UTC 时间" },
      "level":     { "type": "string", "enum": ["DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL"] },
      "message":   { "type": "string" },
      "logger":    { "type": "string", "description": "logger 名称 / 模块路径" },
      "trace_id":  { "type": "string", "description": "链路追踪 ID，用于串联同一请求的事件链" },
      "extra":     { "type": "object", "description": "业务自定义字段，任意键值对" }
    }
  }
  ```
- 级别约定：ERROR=需要立即处理的故障；WARN=可恢复的异常；INFO=关键业务事件；DEBUG=开发调试

### 鉴权相关的强制字段

RBAC 是 aladdin 的关键路径（见下方判定条件：涉及权限、不可逆操作）。**所有鉴权决策日志必须额外携带**：

| 字段 | 含义 |
|------|------|
| `trace_id` | 链路 ID |
| `subject_id` | 发起操作的主体标识 |
| `permission` | 本次请求判定所用的权限码（形如 `rbac.role.read`） |
| `decision` | `allow` / `deny` |
| `reason` | 拒绝时的判定依据（无匹配策略 / 显式拒绝 / 会话失效 / 作用域不符） |

缺少任一字段的鉴权日志视为**不合格埋点**，排障时不得作为依据。字段定义只在 [docs/design/rbac/enforcement.md](design/rbac/enforcement.md) 维护，本表仅声明要求。

### 关键路径日志链路设计

关键路径的日志必须形成可追溯的完整链路，避免故障复盘时出现"日志断点"。设计原则：

- **以排障记录为输入**：定期回顾 [`docs/debugging/registry.md`](./debugging/registry.md) 中反复出现的症状，识别那些"日志缺失/不足导致定位耗时"的故障类型，把这些路径列为关键路径。
- **关键路径的判定**：满足以下任一条件即视为关键路径，必须设计完整日志链路：
  - 在 debug registry 中出现过 ≥2 次同类症状
  - 涉及外部依赖调用（数据库、第三方 API、消息队列）
  - 涉及资金、权限、数据一致性等不可逆操作
  - 跨服务/跨进程的请求链路
- **完整链路的最低要求**：
  - **入口日志**：记录请求进入时的 `trace_id`、关键入参、调用方标识
  - **关键决策点**：分支判断、缓存命中/未命中、降级触发等需 INFO 级日志
  - **外部调用**：调用前记录目标与参数，调用后记录耗时与结果（成功/失败）
  - **异常出口**：所有 catch 分支必须 ERROR 日志包含 `trace_id` + 完整堆栈 + 上下文快照
  - **正常出口**：记录处理结果摘要与总耗时
- **trace_id 传递**：同一请求在所有日志中必须携带相同 `trace_id`。
  - gRPC：通过 metadata key `x-trace-id` 透传；服务端拦截器在**认证之前**生成或继承 `trace_id`，确保被拒绝的请求同样留痕。
  - CLI → 服务端：CLI 每次调用生成新的 `trace_id` 写入 `x-trace-id`，并在 CLI 侧以 DEBUG 级记录同一个 ID，使端到端可串联。
  - Web → 服务端：前端在请求头写 `x-trace-id`；生产环境的前端错误上报须携带同一 ID。
- **闭环机制**：每次新增一条 [排障记录](./debugging/registry.md) 时，在"预防措施"小节明确补充：本次故障对应的关键路径是否已具备完整日志？若否，本次修复必须同步补齐日志埋点。

## 指标（Metrics）

- 框架：Go 侧 `prometheus/client_golang`，经 `/metrics` 暴露
- 命名规范：`aladdin_<module>_<metric>_<unit>`
- 必须暴露的基础指标：请求总数、错误率、响应延迟（P50/P99）
- 鉴权专属指标：
  - `aladdin_rbac_decision_total{permission,decision}` — 决策计数，用于发现权限被大规模拒绝的异常
  - `aladdin_rbac_decision_duration_seconds` — 决策耗时直方图

## 链路追踪（Tracing）

- 框架：OpenTelemetry（Go SDK）；初期仅落地 `trace_id` 贯通，Span 采集待接入
- TraceID 传播：请求头 `x-trace-id`。三种 RPC 协议（Connect / gRPC / gRPC-Web）共用同一个头名，因此一次跨端操作在日志里是同一条链路

## 健康检查

- RPC：提供 `grpc.health.v1.Health`（经 grpchealth 组件），用于 k8s readiness/liveness
- HTTP 端点（若启用 gateway）：`GET /health`，返回 `{"status": "ok"}`
