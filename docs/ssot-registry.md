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
| 一个 RPC 方法需要认证 / 需要哪个权限码 | `rbac.Resolve` | [internal/rbac/annotation.go](../internal/rbac/annotation.go) |
| 数据库后端类型的合法取值 | `database.ParseDialect` | [internal/database/dialect.go](../internal/database/dialect.go) |
| 该读哪一个配置文件（显式指定 > 环境变量 > 默认位置） | `config.locateFile` | [internal/config/file.go](../internal/config/file.go) |
| 配置文件里的键是否属于本端 | `config.readFileValues` | [internal/config/file.go](../internal/config/file.go) |
| 凭证文件权限是否可接受（仅属主可读写） | 凭证文件权限校验 | [internal/auth/credentials.go](../internal/auth/credentials.go) |
| 合入 / 发版前的门禁范围（跑哪些检查） | 可复用工作流 | [.github/workflows/gate.yml](../.github/workflows/gate.yml) |
| 一份渠道凭证是否可信、代表哪个渠道上的哪个身份 | `identity.TokenVerifier` 接口；每个渠道一个实现 | [internal/identity/channel.go](../internal/identity/channel.go)（Google 实现见 [google_verifier.go](../internal/identity/google_verifier.go)、GitHub 见 [github_verifier.go](../internal/identity/github_verifier.go)） |
| 当前启用了哪些登录渠道 | `identity.Registry` | [internal/identity/channel.go](../internal/identity/channel.go) |
| 一个 HTTP 路径是不是浏览器直连的非 RPC 入口 | `isBrowserEntry` | [internal/server/middleware.go](../internal/server/middleware.go) |
| 服务端签发的浏览器 cookie 怎么构造、怎么取（属性集合唯一入口） | `opaqueCookie` / `cookieValue` | [internal/server/cookie.go](../internal/server/cookie.go) |
| 一份浏览器直连的一次性凭据是否用过、是否还有效（导航状态与待绑定凭据共用） | `oneTimeStore` | [internal/server/one_time_store.go](../internal/server/one_time_store.go) |
| 重定向型绑定的一份已校验身份是否还在等待兑换 | `pendingBindings` | [internal/server/pending_bindings.go](../internal/server/pending_bindings.go) |
| 一个身份能不能从原主体被认领（原主体只有这条身份、且没有角色绑定） | 身份存储的 `Reclaim`（身份侧不变式）+ 生命周期锁下的 RBAC 只读检查 | [internal/identity/identity.go](../internal/identity/identity.go) / [internal/server/subject_lifecycle_gate.go](../internal/server/subject_lifecycle_gate.go) |
| 一个渠道身份属于哪个主体 | 身份的解析入口（按（来源，身份标识）查别名，未命中才登记主体） | [internal/identity/identity_resolver.go](../internal/identity/identity_resolver.go) |
| 一份会话凭证是否有效、代表谁 | 会话存储的查询入口 | [internal/identity/session.go](../internal/identity/session.go) |
| 某个邮箱（展示值）对应哪些已登记身份 | 身份别名的按展示值查询 | [internal/identity/identity.go](../internal/identity/identity.go) |
| 一个主体的展示名（昵称，未设则回退到渠道标识，再回退到主体标识） | 档案的展示名解析入口 | [internal/profile/profiles.go](../internal/profile/profiles.go) |
| 一段头像字节是不是一张可接受的图片 | 头像的类型嗅探与白名单（唯一入口，不接受上传方声明的类型） | [internal/profile/avatar.go](../internal/profile/avatar.go) |
| 一份短码或设备码是否已获批准、是否已被交付 | `deviceLogins`（状态流转与交付） | [internal/server/device_logins.go](../internal/server/device_logins.go) |
| 本机二进制相对最新发布是旧是新（要不要升级） | 严格版本的解析与比较入口 | [internal/upgrade/version.go](../internal/upgrade/version.go) |

> **配置不得成为权限的来源**。主体、角色、权限码、作用域一律不得由配置提供；默认作用域只能来自主体的绑定关系。见 [docs/design/config/README.md](design/config/README.md)。

> **唯一例外是引导**：系统里还没有任何角色绑定时，用引导配置建立第一个管理员。它是初始化的输入，不是判定的输入——判定路径只读存储、从不读配置。四条边界（物化 / 一次性 / 留痕 / 按不可变标识）见 [CLAUDE.md](../CLAUDE.md) 第 7 条，生效入口见 [internal/server/bootstrap.go](../internal/server/bootstrap.go)。

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
| 界面主题偏好的读取、落盘与「跟随系统」的折算 | `readThemePreference` / `writeThemePreference` / `resolveTheme` | [web/src/theme/theme-preference.ts](../web/src/theme/theme-preference.ts) |
| 遥测实现（TracerProvider / MeterProvider）的构建 | `observability.NewProvider` | [internal/observability/provider.go](../internal/observability/provider.go) |
| 数据库连接串的归一与脱敏摘要 | `database.NormalizeDSN` / `database.Describe` | [internal/database/dialect.go](../internal/database/dialect.go) |
| 数据库连接的建立与连接池取值 | `database.Open` | [internal/database/database.go](../internal/database/database.go) |
| 库结构演进到最新版本（迁移的执行与版本记录） | `migrate.Run` | [internal/database/migrate/migrate.go](../internal/database/migrate/migrate.go) |
| 内置角色在库中的初始化 | `rbac.EnsureBuiltinRoles` | [internal/rbac/builtin_roles.go](../internal/rbac/builtin_roles.go) |
| 角色列表与绑定列表的排序规则 | `rbac.SortRoles` / `rbac.SortBindings` | [internal/rbac/sort.go](../internal/rbac/sort.go) |
| 新主体标识的分配（唯一入口） | `identity` 的身份解析入口 | [internal/identity/identity_resolver.go](../internal/identity/identity_resolver.go) |
| 会话凭证的生成与摘要计算 | `identity` 的会话签发入口 | [internal/identity/session.go](../internal/identity/session.go) |
| 过期会话行的回收时机 | 服务端启动路径上的回收调用 | [internal/server/server.go](../internal/server/server.go) |

> **链路标识只用 OTel 的传播实现**。仓库里不保留任何自研的 trace_id 生成、注入或继承逻辑：那会与 `traceparent` 形成两套并存的标识，而它们迟早会不一致（见 [docs/observability.md](observability.md)）。

## 副作用类（Side Effects）

> 同一类副作用（写日志、发通知、apply 资源等）只能有一个实现入口。

| 副作用内容 | 唯一入口 | 所在文件 |
|---------|---------|---------|
| 结构化日志输出 | `observability.NewLogger` 返回的 `*zap.Logger` | [internal/observability/logger.go](../internal/observability/logger.go) |
| 前端出站请求：凭证注入、链路标识注入、线格式选择、会话失效处理 | `createTransport`（前端出站请求的唯一出口） | [web/src/api/transport.ts](../web/src/api/transport.ts) |
| 鉴权决策留痕（含 subject/permission/decision/reason） | `rbac.Engine.Check` 内部统一埋点 | [internal/rbac/engine.go](../internal/rbac/engine.go) |
| 登录、绑定与解绑的留痕（含主体标识与渠道，**不含令牌**） | `IdentityService` 的对应处理方法 | [internal/server/identity_service.go](../internal/server/identity_service.go) |
| 档案变更的留痕（含主体标识与改了哪一项，**不含头像字节与简介全文**） | `ProfileService` 的对应处理方法 | [internal/server/profile_service.go](../internal/server/profile_service.go) |
| 拒绝结论到 RPC 错误码与错误详情的转换 | `reject` / `DenyByAnnotation` | [internal/server/interceptor/rejection.go](../internal/server/interceptor/rejection.go) |
| 按客户端协议写出错误响应（中间件层） | `connect.ErrorWriter` | [internal/server/middleware.go](../internal/server/middleware.go) |
| 服务端为每个请求起 span、回写 `traceparent` 与 `x-trace-id` 响应头 | `observability.StartServerSpan` / `WriteTraceHeaders` | [internal/observability/tracing.go](../internal/observability/tracing.go) |
| 每个请求留一行可检索的日志（含 trace_id / 过程名 / 结果码 / 耗时） | `telemetryMiddleware.logRequest` | [internal/server/middleware.go](../internal/server/middleware.go) |
| 遥测数据的导出与退出前冲刷 | `observability.Provider` 的 `Shutdown`（导出失败不影响业务） | [internal/observability/provider.go](../internal/observability/provider.go) |
| 指标名、属性键与记录入口 | 常量定义 + `observability.Metrics` 的方法 | [internal/observability/metrics.go](../internal/observability/metrics.go) |
| 发布产物的构建与打包（跨平台二进制、前端包、校验和） | `make release-build` | [Makefile](../Makefile) |
| 把产物部署到生产（拉取、校验、替换、重启、回滚） | `deploy/deploy.sh` | [deploy/deploy.sh](../deploy/deploy.sh) |
| 本机命令行的替换（原子、按符号链接指向的真实文件） | `upgrade` 的替换入口 | [internal/upgrade/replace.go](../internal/upgrade/replace.go) |

## 数据源类（Data Sources）

> 同一份数据（配置、清单、映射表等）只能有一个读取入口。

| 数据内容 | 唯一入口 | 所在文件 |
|---------|---------|---------|
| 接口契约与消息定义（服务端 + 前端类型） | proto 定义，经 `buf generate` 同时派生出 Go 与 TS 两侧代码 | [api/proto/](../api/proto/) |
| 拒绝原因枚举（Go / TypeScript / CLI 三端同源） | [api/proto/aladdin/rbac/v1/errors.proto](../api/proto/aladdin/rbac/v1/errors.proto) | 由 `buf generate` 派生，三端均引用生成常量 |
| 权限码全集（Go 常量与前端常量同源） | 权限目录，经 `make gen` 双向派生 | [api/permissions/catalog.yaml](../api/permissions/catalog.yaml) |
| 角色 / 权限 / 主体关系的持久化数据 | `rbac.Store` 接口（内存实现与 SQL 实现并存，语义由同一套契约测试守着） | [internal/rbac/store.go](../internal/rbac/store.go) / [internal/rbac/gormstore/](../internal/rbac/gormstore/store.go) |
| 会话（谁、到什么时候为止、作用域）的持久化数据 | 会话存储接口（内存实现与 SQL 实现并存，语义由同一套契约测试守着） | [internal/identity/session.go](../internal/identity/session.go) / [internal/identity/gormstore/](../internal/identity/gormstore/store.go) |
| 身份别名（（来源，身份标识）→ 主体）的持久化数据 | 身份别名的存储接口（内存实现与 SQL 实现并存，语义由同一套契约测试守着） | [internal/identity/identity.go](../internal/identity/identity.go) / [internal/identity/gormstore/identity.go](../internal/identity/gormstore/identity.go) |
| 档案（昵称、简介、头像对象键）的持久化数据 | 档案的存储接口（内存实现与 SQL 实现并存，语义由同一套契约测试守着） | [internal/profile/profile.go](../internal/profile/profile.go) / [internal/profile/gormstore/profile.go](../internal/profile/gormstore/profile.go) |
| 头像字节与它的读取地址 | 头像存储接口（生产实现是 COS，测试注入假实现） | [internal/profile/avatar.go](../internal/profile/avatar.go) / [internal/profile/cosstore/avatar.go](../internal/profile/cosstore/avatar.go) |
| 表结构与迁移清单（库里长什么样） | 迁移清单，由 `migrate.Run` 执行 | [internal/database/schema.go](../internal/database/schema.go) / [internal/database/migrate/migrations.go](../internal/database/migrate/migrations.go) |
| 服务端配置（监听地址、日志级别与路径） | 服务端 `config.yml` + `config.local.yml`，经 `config.LoadServer` 读取 | [internal/config/load.go](../internal/config/load.go) |
| CLI 配置（目标地址、超时、输出详细度） | CLI `config.yml` + `config.local.yml`，经 `config.LoadCLI` 读取 | [internal/config/load.go](../internal/config/load.go) |
| 两端各自认识的配置键 | `serverKeys` / `cliKeys` | [internal/config/file.go](../internal/config/file.go) |
| CLI 凭证（令牌、绑定作用域、过期时间） | 用户配置目录下的 `credentials.json`，经 `auth.Resolve` 读取 | [internal/auth/credentials.go](../internal/auth/credentials.go) |
| 环境变量名（`ALADDIN_` 前缀） | 各模块内集中定义：配置项在 config、凭证在 auth、开发旁路在 server；调用方不得手写字符串字面量 | [internal/config/config.go](../internal/config/config.go) |
| 发布产物清单与校验和（自更新的唯一来源） | `upgrade` 的发布源读取入口 | [internal/upgrade/release.go](../internal/upgrade/release.go) |
| 代码生成与静态检查工具的版本（本机安装与 CI 缓存 key 都由此派生） | Makefile 的 `TOOLS` 清单 | [Makefile](../Makefile) |
