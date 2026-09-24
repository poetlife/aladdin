# SSOT 注册表（Single Source of Truth Registry）

项目里**"同一件事的唯一入口"**的登记表。配合 [CLAUDE.md](../CLAUDE.md) 的 `Single Source of Truth 原则` 章节使用。

**改动前必查本表**：确认要做的判断 / 操作 / 数据读取是否已有登记。
- 已有登记 → 直接复用，需求不满足时改那一个入口，不得在调用侧绕开。
- 未登记但会跨模块复用 → 抽到公共模块后在此登记一行。

---

## 判断类（Checks）

> 同一个判断只能有一个实现入口。

| 判断内容 | 唯一入口 | 所在文件 |
|---------|---------|---------|
| 某主体是否拥有某权限（RBAC 决策） | `rbac.Engine.Check` | [internal/rbac/engine.go](../internal/rbac/engine.go) |
| 权限码形状是否合法 | `rbac.PermissionCode.Valid` | [internal/rbac/permission.go](../internal/rbac/permission.go) |
| 权限码是否已登记 | 权限目录 | [api/permissions/catalog.yaml](../api/permissions/catalog.yaml) |
| 前端当前会话是否持有某权限码（仅用于展示裁剪） | `usePermission()` | [web/src/auth/use-permission.ts](../web/src/auth/use-permission.ts) |
| 一个 RPC 方法需要认证 / 需要哪个权限码 | `interceptor.Resolve` | [internal/server/interceptor/annotation.go](../internal/server/interceptor/annotation.go) |
| 数据库后端类型的合法取值 | `database.ParseDialect` | [internal/database/dialect.go](../internal/database/dialect.go) |
| 该读哪一个配置文件（显式指定 > 环境变量 > 默认位置） | `config.locateFile` | [internal/config/file.go](../internal/config/file.go) |
| 配置文件里的键是否属于本端 | `config.readFileValues` | [internal/config/file.go](../internal/config/file.go) |
| 凭证文件权限是否可接受（仅属主可读写） | 凭证文件权限校验 | [internal/auth/credentials.go](../internal/auth/credentials.go) |

> **配置不得成为权限的来源**。主体、角色、权限码、作用域一律不得由配置提供；默认作用域只能来自主体的绑定关系。见 [docs/design/config/README.md](design/config/README.md)。

> **前端权限判断不是安全边界**。前端 `usePermission` 只决定"要不要渲染"，服务端 `rbac.Engine.Check` 才是唯一有约束力的判定。两者判定逻辑必须一致，见 [docs/design/rbac/frontend-permissions.md](design/rbac/frontend-permissions.md)。

## 加载 / 合并 / 规范化类（Load & Transform）

> 同一次数据加载或规范化只能有一个实现入口。

| 操作内容 | 唯一入口 | 所在文件 |
|---------|---------|---------|
| 服务端配置的五层合并与校验 | `config.LoadServer` | [internal/config/load.go](../internal/config/load.go) |
| CLI 配置的五层合并与校验 | `config.LoadCLI` | [internal/config/load.go](../internal/config/load.go) |
| 分层顺序（默认值 < 配置文件 < 本地覆盖 < 环境变量 < 命令行参数） | 两端展开时共用同一条语义与同一批来源读取函数 | [internal/config/load.go](../internal/config/load.go) |
| CLI 凭证解析（参数 > 环境变量 > 凭证文件） | `auth.Resolve` | [internal/auth/credentials.go](../internal/auth/credentials.go) |
| 日志 logger 构建 | `observability.NewLogger` | [internal/observability/logger.go](../internal/observability/logger.go) |
| trace_id / span_id 的生成与继承 | OTel 传播器，经 `observability.StartServerSpan` / `StartClientSpan` | [internal/observability/tracing.go](../internal/observability/tracing.go) |
| 日志与链路的关联（`trace_id` / `span_id` 字段） | `observability.SpanLogger` | [internal/observability/tracing.go](../internal/observability/tracing.go) |
| 前端链路标识的生成与校验 | `newTraceparent` / `parseTraceparent` | [web/src/api/trace-context.ts](../web/src/api/trace-context.ts) |
| 遥测实现（TracerProvider / MeterProvider）的构建 | `observability.NewProvider` | [internal/observability/provider.go](../internal/observability/provider.go) |
| 数据库连接串的归一与脱敏摘要 | `database.NormalizeDSN` / `database.Describe` | [internal/database/dialect.go](../internal/database/dialect.go) |
| 数据库连接的建立与连接池取值 | `database.Open` | [internal/database/database.go](../internal/database/database.go) |
| 库结构演进到最新版本（迁移的执行与版本记录） | `migrate.Run` | [internal/database/migrate/migrate.go](../internal/database/migrate/migrate.go) |
| 内置角色在库中的初始化 | `rbac.EnsureBuiltinRoles` | [internal/rbac/builtin_roles.go](../internal/rbac/builtin_roles.go) |
| 角色列表与绑定列表的排序规则 | `rbac.SortRoles` / `rbac.SortBindings` | [internal/rbac/sort.go](../internal/rbac/sort.go) |

> **链路标识只用 OTel 的传播实现**。仓库里不保留任何自研的 trace_id 生成、注入或继承逻辑：那会与 `traceparent` 形成两套并存的标识，而它们迟早会不一致（见 [docs/observability.md](observability.md)）。

## 副作用类（Side Effects）

> 同一类副作用（写日志、发通知、apply 资源等）只能有一个实现入口。

| 副作用内容 | 唯一入口 | 所在文件 |
|---------|---------|---------|
| 结构化日志输出 | `observability.NewLogger` 返回的 `*zap.Logger` | [internal/observability/logger.go](../internal/observability/logger.go) |
| 前端出站请求：凭证注入、链路标识注入、线格式选择、会话失效处理 | `createTransport`（前端出站请求的唯一出口） | [web/src/api/transport.ts](../web/src/api/transport.ts) |
| 鉴权决策留痕（含 subject/permission/decision/reason） | `rbac.Engine.Check` 内部统一埋点 | [internal/rbac/engine.go](../internal/rbac/engine.go) |
| 拒绝结论到 RPC 错误码与错误详情的转换 | `reject` / `DenyByAnnotation` | [internal/server/interceptor/rejection.go](../internal/server/interceptor/rejection.go) |
| 按客户端协议写出错误响应（中间件层） | `connect.ErrorWriter` | [internal/server/middleware.go](../internal/server/middleware.go) |
| 服务端为每个请求起 span、回写 `traceparent` 与 `x-trace-id` 响应头 | `observability.StartServerSpan` / `WriteTraceHeaders` | [internal/observability/tracing.go](../internal/observability/tracing.go) |
| 每个请求留一行可检索的日志（含 trace_id / 过程名 / 结果码 / 耗时） | `telemetryMiddleware.logRequest` | [internal/server/middleware.go](../internal/server/middleware.go) |
| 遥测数据的导出与退出前冲刷 | `observability.Provider` 的 `Shutdown`（导出失败不影响业务） | [internal/observability/provider.go](../internal/observability/provider.go) |
| 指标名、属性键与记录入口 | 常量定义 + `observability.Metrics` 的方法 | [internal/observability/metrics.go](../internal/observability/metrics.go) |

## 数据源类（Data Sources）

> 同一份数据（配置、清单、映射表等）只能有一个读取入口。

| 数据内容 | 唯一入口 | 所在文件 |
|---------|---------|---------|
| 接口契约与消息定义（服务端 + 前端类型） | proto 定义，经 `buf generate` 同时派生出 Go 与 TS 两侧代码 | [api/proto/](../api/proto/) |
| 拒绝原因枚举（Go / TypeScript / CLI 三端同源） | [api/proto/aladdin/rbac/v1/errors.proto](../api/proto/aladdin/rbac/v1/errors.proto) | 由 `buf generate` 派生，三端均引用生成常量 |
| 权限码全集（Go 常量与前端常量同源） | 权限目录，经 `make gen` 双向派生 | [api/permissions/catalog.yaml](../api/permissions/catalog.yaml) |
| 角色 / 权限 / 主体关系的持久化数据 | `rbac.Store` 接口（内存实现与 SQL 实现并存，语义由同一套契约测试守着） | [internal/rbac/store.go](../internal/rbac/store.go) / [internal/rbac/gormstore/](../internal/rbac/gormstore/store.go) |
| 表结构与迁移清单（库里长什么样） | 迁移清单，由 `migrate.Run` 执行 | [internal/database/schema.go](../internal/database/schema.go) / [internal/database/migrate/migrations.go](../internal/database/migrate/migrations.go) |
| 服务端配置（监听地址、日志级别与路径） | 服务端 `config.yml` + `config.local.yml`，经 `config.LoadServer` 读取 | [internal/config/load.go](../internal/config/load.go) |
| CLI 配置（目标地址、超时、输出详细度） | CLI `config.yml` + `config.local.yml`，经 `config.LoadCLI` 读取 | [internal/config/load.go](../internal/config/load.go) |
| 两端各自认识的配置键 | `serverKeys` / `cliKeys` | [internal/config/file.go](../internal/config/file.go) |
| CLI 凭证（令牌、绑定作用域、过期时间） | 用户配置目录下的 `credentials.json`，经 `auth.Resolve` 读取 | [internal/auth/credentials.go](../internal/auth/credentials.go) |
| 环境变量名（`ALADDIN_` 前缀） | 各模块内集中定义：配置项在 config、凭证在 auth、开发旁路在 server；调用方不得手写字符串字面量 | [internal/config/config.go](../internal/config/config.go) |
