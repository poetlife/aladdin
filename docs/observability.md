# 可观测性总览

本文档是 aladdin 可观测性**功能行为的唯一信源**。三端（服务端、CLI、Web）各自的实现细节只改代码；**传播约定、字段语义、导出行为**的变更必须先改本文档。

三端有独立的采集链路，但共用同一套数据模型与同一套 `trace_id` / `span_id` 传播约定。

| 端 | 入口 | 日志 | 追踪 | 导出 |
|----|------|------|------|------|
| 服务端 | [cmd/aladdin-server](../cmd/aladdin-server) | zap：stderr 人类可读 + 可选 JSON Lines 文件 | 为每个请求起 server span | OTLP/HTTP（配置了端点才上报） |
| CLI | [cmd/aladdin](../cmd/aladdin) | 直接写 stderr，未接 zap（见"CLI 的现状"） | 为每次 RPC 起 client span | 同上 |
| Web | [web/](../web) | 浏览器 console | 生成并传播 `traceparent`，读回响应头 | 不上报，只做关联 |

---

## 链路追踪（Tracing）

### 传播协议是 W3C Trace Context

跨端传播使用 [W3C Trace Context](https://www.w3.org/TR/trace-context/) 的 `traceparent` 头，**不使用自定义头**：

```
traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-0edd0d76afec1634-01
             ││ └────────── trace-id（32 位小写十六进制）┘└── span-id（16 位）─┘└ flags
             │└ 版本
             └ 分隔符
```

`tracestate` 若存在则原样透传，本仓库不解析其内容。

**为什么必须是这个头**：OTel Collector、Tempo、Jaeger、以及 Envoy/Istio 这类 service mesh 都只认 `traceparent`。用自定义头（如 `x-trace-id`）的后果不是"不好看"，而是**链路断在第一个 hop**——所有标准组件都看不见这条链路。

### 只有 trace_id 不够，必须有 span

`trace_id` 回答"这是同一条链路"，`span_id` 回答"这是链路里的哪一步"。排障时问的几乎总是后者，因此**链路标识的载体是 span，不是裸 ID**：

- 服务端为每个进入的 RPC 请求起一个 server span，名字取 RPC 过程名（如 `aladdin.rbac.v1.RBACService/ListRoles`）。
- CLI 为每次 RPC 起一个 client span。
- 日志携带 `trace_id` 与 `span_id` 两个字段（见下文"日志与追踪的关联"）。

### 三端如何参与

| 端 | 生成 / 继承 | 是否上报 |
|----|-----------|---------|
| 服务端 | 从入站 `traceparent` 继承 trace-id，生成自己的 span-id；无有效入站值时新起一条链路 | 配置了 `otel_endpoint` 才上报 |
| CLI | 每次调用新起一条链路（CLI 进程即链路起点） | 同上 |
| Web | 浏览器侧生成 `traceparent`（32 位 trace-id + 16 位 span-id），遵循同一格式 | **不上报**。前端只负责传播与关联 |

三种 RPC 协议（Connect / gRPC / gRPC-Web）的元数据都落在 HTTP 头里，因此**服务端只有一处提取点**，一次实现同时覆盖三种协议。

### 入站值必须校验

服务端收到 `traceparent` 后**必须校验**，通过才继承；不通过则**丢弃并新起一条链路**。

校验项：版本为 `00`、trace-id 是 32 位小写十六进制且**非全零**、span-id 是 16 位小写十六进制且非全零。

两条硬性要求：

1. **非法值绝不能被原样继承或回显**。不校验就采信的后果有两层：日志里出现格式非法的 trace_id 会破坏所有基于它的检索；而"入站值优先"意味着调用方可以用一个固定值把**不同请求**的日志合并成同一条链路——这是可观测性数据的完整性问题。
2. **校验失败不得导致请求失败**。观测数据的格式问题绝不能升级成业务故障，那既是一次自伤，也是一条拒绝服务的路径。处理方式是丢弃、新起链路、留一条 DEBUG 级记录。

### 响应头

服务端在响应中写回**两个**头，二者取自**同一个** span 上下文：

| 头 | 取值 | 给谁用 |
|----|------|-------|
| `traceparent` | trace-id 与请求一致（入站值非法时是服务端新起的那个）；span-id 是**服务端自己这个 span** 的 | OTel Collector、Tempo/Jaeger、service mesh 等标准组件 |
| `x-trace-id` | 就是那 32 位 trace-id | 人 |

**两个都要，不能只留一个：**

- 只有 `traceparent` 时，人要拿 trace-id 得从 `00-` 与 span-id 之间手工剥出中段 32 位——复制它去搜日志很容易抄错，在 DevTools 里可读性也差。
- 只有 `x-trace-id` 时，标准组件接不上链路——那正是当初弃用自定义头的原因。

返回一个可直接复制的关联 ID 是通行做法：GitHub 的 `x-github-request-id`、Cloudflare 的 `cf-ray` 都是这个意思。

`x-trace-id` 有两条边界：

1. **只在响应方向出现，不是传播机制**。请求方向的传播只认 `traceparent`；服务端**不读**入站的 `x-trace-id`。接受它就等于退回"自定义头当传播协议"。
2. **必须与 `traceparent` 同源**。两者从同一个 span 上下文写出，任何一方都不单独生成——两个 ID 不一致比只有一个更糟，排障的人会拿它们互相搜，两边都搜不到。

回传 span-id 而不是原样回显请求头，客户端才能拿到"服务端在哪一步处理了这次请求"。

**这两个头里的 trace-id 一定搜得到**：服务端为每个请求留一行带该 `trace_id` 的日志（见下文"请求留痕"）。这是"回写一个可复制的 ID"这件事成立的前提——只在下游判定处埋点的话，不走判定的请求会返回一个查不到的 ID。

浏览器侧的可见性：跨域部署时服务端必须返回 `Access-Control-Expose-Headers: traceparent, x-trace-id`，否则浏览器读不到这两个头。**开发环境是同源（Vite 代理），不需要额外配置**；生产跨域部署时必须加上。

### 没有 Collector 也要能工作

SDK 一定构建，导出器按配置挂载。这不只是为了方便：

- **trace_id / span_id 的生成与是否导出无关**。没配端点时链路标识照常生成、照常传播、照常回写响应头，只是 trace 不被上报。若把"生成"和"上报"绑在一起，会出现"没装 Collector 就没有 trace_id"——那会让所有关联能力一起失效，而很多环境根本不会装 Collector。
- 端点为空时不上报，**不打印连接错误**。否则每次导出失败都会刷日志，把真正的错误淹没。

### 采样

采样决定 span 是否被记录与导出，**不影响 ID 的生成**——被采样的判定为"丢弃"的 span 依然持有合法的 trace_id/span_id，日志关联照常工作。

默认 `parentbased_always_on`：有上游时跟随上游决定，否则全采。可按比例配置（见"配置项"）。骨架阶段默认全采，是为了让排障时不会"恰好没采到"。

---

## 日志（Logging）

- 框架：**zap**（由 [internal/observability](../internal/observability) 构建）
- 输出：控制台为人类可读纯文本；文件为 JSON Lines（每行一条）

### CLI 的现状与缺口

CLI 把结果写 stdout、诊断写 stderr，**没有**经过 zap。因此 CLI 侧目前没有结构化日志，也没有带 `trace_id` 的行。

它的链路参与是完整的（起 client span 并注入 `traceparent`），所以服务端日志里的 `trace_id` 与 CLI 那一次调用是同一个值——只是 CLI 自己这边没有可检索的记录。

补齐它需要在"用户数据"与"日志"之间先划清边界（结果属于数据，绝不能进日志），属于独立的一次改动。

### 文件输出的实际字段

| 字段 | 类型 | 说明 |
|------|------|------|
| `ts` | string | ISO 8601 时间 |
| `level` | string | 大写：`DEBUG` / `INFO` / `WARN` / `ERROR` / `DPANIC` / `PANIC` / `FATAL` |
| `msg` | string | 日志消息 |
| `logger` | string | 服务标识（`aladdin-server` / `aladdin-cli`） |
| `trace_id` | string | 若非空，32 位十六进制 |
| `span_id` | string | 若非空，16 位十六进制 |
| 其余键 | 任意 | 业务字段，**在顶层平铺**，不包在 `extra` 之类的容器里 |

**`trace_id` 与 `span_id` 不是必填字段。** 它们只在日志与某个 span 关联时出现；进程启动、配置加载、优雅退出这类没有请求上下文的日志不带它们。把它们声明为 required 会与实际输出不符，也会诱导出"为了凑字段而伪造 trace_id"的做法。

### 鉴权相关的强制字段

RBAC 是 aladdin 的关键路径。**所有鉴权决策日志必须额外携带**：

| 字段 | 含义 |
|------|------|
| `trace_id` | 链路 ID（拒绝发生在 span 建立之后，因此这一项必定存在） |
| `subject_id` | 发起操作的主体标识 |
| `permission` | 本次请求判定所用的权限码（形如 `rbac.role.read`） |
| `decision` | `allow` / `deny` |
| `reason` | 拒绝时的判定依据（无匹配策略 / 显式拒绝 / 会话失效 / 作用域不符） |

缺少任一字段的鉴权日志视为**不合格埋点**，排障时不得作为依据。字段定义只在 [docs/design/rbac/enforcement.md](design/rbac/enforcement.md) 维护，本表仅声明要求。

### 日志与追踪的关联

日志通过 span 上下文取得 `trace_id` / `span_id`。**关联只有一处实现**：由 observability 包从 context 里读 span 上下文并派生字段。任何模块不得自行读取传播头、不得自行生成 ID。

### 关键路径日志链路设计

关键路径的日志必须形成可追溯的完整链路，避免故障复盘时出现"日志断点"。设计原则：

- **以排障记录为输入**：定期回顾 [`docs/debugging/registry.md`](./debugging/registry.md) 中反复出现的症状，识别那些"日志缺失/不足导致定位耗时"的故障类型，把这些路径列为关键路径。
- **关键路径的判定**：满足以下任一条件即视为关键路径，必须设计完整日志链路：
  - 在 debug registry 中出现过 ≥2 次同类症状
  - 涉及外部依赖调用（数据库、第三方 API、消息队列）
  - 涉及资金、权限、数据一致性等不可逆操作
  - 跨服务/跨进程的请求链路
- **完整链路的最低要求**：
  - **请求留痕**：每个进入服务端的请求都必须留下一行日志，含 `trace_id`、过程名、结果码、耗时、调用方标识。这一行**同时承担"入口"与"正常出口"**两个作用——拆成两行会让日志量翻倍而信息量几乎不增。
  - **请求留痕不能只靠下游埋点**：`Login`、`WhoAmI`、`GetSessionPermissions` 这些请求不经过任何判定或存储访问，只在判定处埋点就等于它们完全没有留痕。而浏览器打开页面时最先发的正是这几个方法。
  - **不记请求体**：请求体可能装着秘密——`Login` 的请求体就是凭证——因此**一律不进日志**。需要"关键入参"时记的是不含秘密的标识（作用域、目标 ID），而不是原始报文。
  - **关键决策点**：分支判断、缓存命中/未命中、降级触发等需 INFO 级日志
  - **外部调用**：调用前记录目标与参数，调用后记录耗时与结果（成功/失败）
  - **异常出口**：所有错误分支必须 ERROR 日志包含 `trace_id` + 完整堆栈 + 上下文快照
  - **高频路径可以降级但不可以不记**：探针、未匹配路径这类高频或低价值请求降到 DEBUG，但**仍要带 `trace_id`**——否则"服务是否健康"这类问题本身不可追溯。判据是"能不能搜到"，不是"好不好看"。
- **闭环机制**：每次新增一条 [排障记录](./debugging/registry.md) 时，在"预防措施"小节明确补充：本次故障对应的关键路径是否已具备完整日志？若否，本次修复必须同步补齐日志埋点。

---

## 指标（Metrics）

- 框架：**OpenTelemetry Metrics**，与链路追踪同栈。选它而不是另起一套 Prometheus 客户端，是为了让指标与 trace 共享同一套资源标识，并支持用 exemplar 把某个指标点关联到具体 trace。
- 命名遵循 OTel 语义约定：**点分小写，不带单位后缀**，单位放在 instrument 的 `unit` 上。

必须暴露的指标：

| 指标 | 类型 | 属性 | 说明 |
|------|------|------|------|
| `aladdin.server.requests` | counter | `procedure`、`code` | 请求总数与错误率的分母/分子来源 |
| `aladdin.server.request.duration` | histogram（单位 `s`） | `procedure` | 响应延迟，用于算 P50/P99 |
| `aladdin.rbac.decisions` | counter | `permission`、`decision` | 决策计数，用于发现权限被大规模拒绝的异常 |
| `aladdin.rbac.decision.duration` | histogram（单位 `s`） | `permission` | 决策耗时 |

属性基数必须受控：只允许有界取值（RPC 过程名、权限码、决策结论），**不得**使用原始 URL、用户输入或主体 ID 作为属性值——那会让时序数量随流量增长。

---

## 配置项

追踪与指标共用一组配置，两端（服务端与 CLI）都有：

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `otel_endpoint` | `ALADDIN_OTEL_ENDPOINT` | 空 | OTLP/HTTP 端点（`host:port`）。为空表示只生成与传播链路标识、不上报 |
| `otel_insecure` | `ALADDIN_OTEL_INSECURE` | `false` | 是否用明文 HTTP 连接端点。本地 Collector 通常需要开启 |
| `otel_sample_ratio` | `ALADDIN_OTEL_SAMPLE_RATIO` | `1` | 采样比例，取值 0–1 |

配置项的加载与合并规则见 [docs/design/config/](design/config/README.md)——本表只声明"可观测性需要哪些配置"，不重复优先级与缺省语义。

`service.name` 不由配置项提供：它由每个入口在构建时声明（服务端为 `aladdin-server`、CLI 为 `aladdin-cli`），避免出现"同一个二进制在不同环境报告成不同服务名"。

### 导出失败的处理

上报失败**不得**影响业务：导出由 SDK 的后台批处理完成，失败只记录到 SDK 自身的错误通道，不改变请求的成败，也不改变响应内容。进程退出时必须在优雅退出的时间预算内 flush 一次，避免丢掉最后一批 span。

---

## 健康检查

- RPC：提供 `grpc.health.v1.Health`（经 grpchealth 组件），用于 k8s readiness/liveness
- 健康检查与反射属于基础设施过程，**不参与业务鉴权**，但**照常产生 span 与请求指标**——否则"服务是否健康"这件事本身不可观测。

---

> 本文档描述行为，不描述实现。函数签名、内部数据结构、SDK 装配顺序属于实现细节，只改代码。
> 若发现本文档与实际输出不符，**以文档为错误方**修正文档或修正代码，不允许两套说法并存（见 [CLAUDE.md](../CLAUDE.md) 的 SSOT 原则）。
