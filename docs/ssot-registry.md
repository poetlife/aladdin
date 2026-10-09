# SSOT 注册表（Single Source of Truth Registry）

项目里**"同一件事的唯一入口"**的登记表。配合 [AGENTS.md](../AGENTS.md) 的 `Single Source of Truth 原则` 章节使用。

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
| 当前视口是否为窄屏（手机），及窄屏断点的取值 | `useNarrowViewport()` / `NARROW_MEDIA_QUERY` | [web/src/layouts/use-narrow-viewport.ts](../web/src/layouts/use-narrow-viewport.ts) |
| 交付用户内容的 iframe：沙箱属性（**含不含 `allow-same-origin` 决定它是否落在不透明源上**；参数里不留可以传入属性的口子），以及它的加载态（遮罩铺到哪一刻、多久算"慢"） | `SandboxFrame` | [web/src/pages/galaxy/SandboxFrame.tsx](../web/src/pages/galaxy/SandboxFrame.tsx) |
| 一块还没有内容可显示的地方在等的时候长什么样（居中 + 那一句话） | `LoadingHint` / `LOADING_TEXT` | [web/src/ui/LoadingHint.tsx](../web/src/ui/LoadingHint.tsx) |
| 一个 RPC 方法需要认证 / 需要哪个权限码 | `rbac.Resolve` | [internal/rbac/annotation.go](../internal/rbac/annotation.go) |
| 数据库后端类型的合法取值 | `database.ParseDialect` | [internal/database/dialect.go](../internal/database/dialect.go) |
| 该读哪一个配置文件（显式指定 > 环境变量 > 默认位置） | `config.locateFile` | [internal/config/file.go](../internal/config/file.go) |
| 配置文件里的键是否属于本端 | `config.readFileValues` | [internal/config/file.go](../internal/config/file.go) |
| 一个主机（或 `host:port` 目标）是否为本地回环（唯一判据；决定 CLI 是否允许明文） | `loopback.IsHost` / `loopback.IsAddress` | [internal/loopback/loopback.go](../internal/loopback/loopback.go) |
| 凭证文件权限是否可接受（仅属主可读写） | 凭证文件权限校验 | [internal/auth/credentials.go](../internal/auth/credentials.go) |
| 合入 / 发版前的门禁范围（跑哪些检查） | 可复用工作流 | [.github/workflows/gate.yml](../.github/workflows/gate.yml) |
| 一份渠道凭证是否可信、代表哪个渠道上的哪个身份 | `identity.TokenVerifier` 接口；每个渠道一个实现 | [internal/identity/channel.go](../internal/identity/channel.go)（Google 实现见 [google_verifier.go](../internal/identity/google_verifier.go)、GitHub 见 [github_verifier.go](../internal/identity/github_verifier.go)） |
| 当前启用了哪些登录渠道 | `identity.Registry` | [internal/identity/channel.go](../internal/identity/channel.go) |
| 一个 HTTP 路径是不是浏览器直连的非 RPC 入口（含 galaxy 的发布地址） | `isBrowserEntry` | [internal/server/middleware.go](../internal/server/middleware.go) |
| 一个上报端标识是否合法（`web` / `cli` 白名单） | `observability.ClientFromHeader` | [internal/observability/client-id.go](../internal/observability/client-id.go) |
| 一个裸 trace_id 是否合法（32 位小写十六进制、非全零） | `observability.ValidTraceID`；前端等价实现是 `parseTraceID` | [internal/observability/tracing.go](../internal/observability/tracing.go) |
| 服务端签发的浏览器 cookie 怎么构造、怎么取（属性集合唯一入口） | `opaqueCookie` / `cookieValue` | [internal/server/cookie.go](../internal/server/cookie.go) |
| 一份浏览器直连的一次性凭据是否用过、是否还有效（导航状态与待绑定凭据共用） | `oneTimeStore` | [internal/server/one_time_store.go](../internal/server/one_time_store.go) |
| 重定向型绑定的一份已校验身份是否还在等待兑换 | `pendingBindings` | [internal/server/pending_bindings.go](../internal/server/pending_bindings.go) |
| 一个身份能不能从原主体被认领（原主体只有这条身份、且没有角色绑定） | 身份存储的 `Reclaim`（身份侧不变式）+ 生命周期锁下的 RBAC 只读检查 | [internal/identity/identity.go](../internal/identity/identity.go) / [internal/server/subject_lifecycle_gate.go](../internal/server/subject_lifecycle_gate.go) |
| 一个渠道身份属于哪个主体 | 身份的解析入口（按（来源，身份标识）查别名，未命中才登记主体） | [internal/identity/identity_resolver.go](../internal/identity/identity_resolver.go) |
| 一份会话凭证是否有效、代表谁 | 会话存储的查询入口 | [internal/identity/session.go](../internal/identity/session.go) |
| 某个邮箱（展示值）对应哪些已登记身份 | 身份别名的按展示值查询 | [internal/identity/identity.go](../internal/identity/identity.go) |
| 一个主体的展示名（昵称，未设则回退到渠道标识，再回退到主体标识） | 档案的展示名解析入口 | [internal/profile/profiles.go](../internal/profile/profiles.go) |
| 一批主体的展示名与头像（读侧把事件里的标识换成展示信息） | `Profiles.GetMany`（`Get` 的批量形式，**回退规则仍是上面那一条实现**，不另写一份批量版拼接） | [internal/profile/profiles.go](../internal/profile/profiles.go) |
| 字节数的展示文案（头像上限、资产上限、文件大小） | `describeBytes` | [web/src/format/bytes.ts](../web/src/format/bytes.ts) |
| 一段源里出现了哪些 `asset://` 记号（去重且有序） | 记号的识别入口（逐字扫描，不解析 HTML） | [internal/galaxy/placeholder.go](../internal/galaxy/placeholder.go) |
| 一处引用（记号、文档间链接、站点内路径）解析成什么地址 | 引用的解析入口（发布态给发布根下的绝对地址、预览态给预览根下的绝对地址；两处共用同一处"落在哪一条条目上"的判断，预览那处只是不因坏引用而失败） | [internal/galaxy/link_resolver.go](../internal/galaxy/link_resolver.go) |
| 预览里草稿按哪条地址取字节（路径形状、短时凭证、"发布根换成预览根"） | 预览通道的路径与凭证入口 | [internal/galaxy/preview.go](../internal/galaxy/preview.go) |
| 一个路径是不是文件组里的一条条目，以及它是文本还是资产（发布态取字节的分派） | 文件组的集合成员查询（只查表，不解析路径） | [internal/galaxy/content_set.go](../internal/galaxy/content_set.go) |
| 内容对象的键（按内容摘要寻址） | 键的派生入口（galaxy 按工程前缀、skill 按 `skills/text/`） | [internal/galaxy/content_set.go](../internal/galaxy/content_set.go) / [internal/skill/package.go](../internal/skill/package.go) |
| 一个内容对象怎么算摘要、怎么判形状、怎么写入（**仅当不存在时写入**，写入前核对摘要） | 内容对象的公共规则（galaxy 的产物与 skill 的纳管共用） | [internal/objectstore/content.go](../internal/objectstore/content.go) |
| 一条相对路径的形状合法性（相对、URL 非保留字符集、无 `.` / `..` / 空段、长度上限） | `relpath.Valid`（galaxy 文件组的条目与 skill 包内路径、来源子路径共用） | [internal/relpath/relpath.go](../internal/relpath/relpath.go) |
| 不可猜标识的分配（取多少熵、怎么编码） | `idgen.New`；前缀由调用方给（galaxy 的工程 / 版本 / 资产、skill 的技能 / 版本） | [internal/idgen/idgen.go](../internal/idgen/idgen.go) |
| 发布态文本条目的响应形状（内容类型、`ETag`、可缓存性） | 发布态的文本响应入口 | [internal/server/galaxy_public.go](../internal/server/galaxy_public.go) |
| 一个文件是文本还是资产（扩展名白名单，**取值按内容槽不同**） | 文本类型白名单（唯一入口，命令行与服务端共用同一张表） | [internal/galaxy/content_slot.go](../internal/galaxy/content_slot.go) |
| 文档站的导航（取哪些文件、按什么序、标题从哪里来） | 导航的派生入口（只取 markdown） | [internal/galaxy/doc_render.go](../internal/galaxy/doc_render.go) |
| 一个工程有哪些内容槽、每个槽的入口路径与地址，以及哪一段路径是保留段 | 内容槽的解析入口（槽的合法取值、入口、保留段共用这一处判断） | [internal/galaxy/content_slot.go](../internal/galaxy/content_slot.go) |
| 一个工程的发布根（构建时注入与发布态地址共用，**按槽派生**；槽根带结尾斜杠，不带时由交付入口 301 过去） | 发布根的派生入口 | [internal/galaxy/public_origin.go](../internal/galaxy/public_origin.go) |
| 一个已发布槽的**分享地址**（主站包装）与**内容地址**（发布域），以及主站壳解析它时用的那一处判断 | 发布地址的派生入口（两者同一处派生，客户端不拼） | [internal/galaxy/public_origin.go](../internal/galaxy/public_origin.go) |
| 一个槽**当前发布**的产物清单，以及每一条在**访客路径**上的地址（回读发布态） | 发布态的读取入口（清单取自发布记录、不重算；地址与主站壳同一处拼接） | [internal/galaxy/publish.go](../internal/galaxy/publish.go) / [internal/galaxy/public_origin.go](../internal/galaxy/public_origin.go) |
| markdown 到 HTML 的渲染（`docs` 槽） | 文档渲染入口（确定性的唯一实现，发布时使用） | [internal/galaxy/doc_render.go](../internal/galaxy/doc_render.go) |
| 发布产物交付时的内容安全策略 | CSP 响应头的构造入口 | [internal/galaxy/csp.go](../internal/galaxy/csp.go) |
| 上传方声明的类型能不能作为头像 | 头像的类型白名单（唯一入口；声明不等于验证，见直传） | [internal/profile/avatar.go](../internal/profile/avatar.go) |
| 上传方声明的类型能不能作为资产 | 资产的类型白名单与分档上限（唯一入口；声明不等于验证，见直传） | [internal/galaxy/asset.go](../internal/galaxy/asset.go) |
| 一份内容能不能发布（引用完整性 + 体积与文件数上限） | `galaxy` 的校验入口（**编辑器提示与发布前置校验共用**，不得在前端复写） | [internal/galaxy/validate.go](../internal/galaxy/validate.go) |
| 产物里的一处取资源引用落在哪一条条目上、产物里有没有残留没解开的记号（**校验与回读发布态之后的复核共用**） | 产物复核入口（扫的是产物，不是源） | [internal/galaxy/artifact_audit.go](../internal/galaxy/artifact_audit.go) |
| 一个版本引用了哪些资产 | 文件组里资产条目的读取入口（不解析正文） | [internal/galaxy/content_set.go](../internal/galaxy/content_set.go) |
| 一个资产标识是否属于某个工程 | 资产的归属查询（唯一入口，发布校验与删除拦阻共用） | [internal/galaxy/asset.go](../internal/galaxy/asset.go) |
| 一个标签串的归一化与合法性（trim、小写、去重、长度与数量上限） | `tagging.Normalize`（galaxy 资产的标签与 skill 目录的标签共用；三端只能消费它的结论，不得各自再判一份） | [internal/tagging/tagging.go](../internal/tagging/tagging.go) |
| 一个工程是不是该主体的（资源归属） | galaxy 的归属校验唯一入口 | [internal/galaxy/ownership.go](../internal/galaxy/ownership.go) |
| 发布态允许从哪个来源取资源、以及允许谁嵌入（记号解析出的地址与内容安全策略同源；含发布根与帧祖先的派生） | 公开域的派生入口 | [internal/galaxy/public_origin.go](../internal/galaxy/public_origin.go) |
| 一份短码或设备码是否已获批准、是否已被交付 | `deviceLogins`（状态流转与交付） | [internal/server/device_logins.go](../internal/server/device_logins.go) |
| 一个远端地址是不是一个可纳管的仓库（**只认 github.com 的仓库根形状**，决定出站请求的目标） | `skill.ParseRepositoryURL`（引用与子路径各有一处：`ParseRef` / `ParseSubPath`） | [internal/skill/source.go](../internal/skill/source.go) |
| 一份远端内容是不是一个合法的技能包（必需文件、路径、文本、上限） | `skill.BuildPackage` | [internal/skill/package.go](../internal/skill/package.go) |
| 一个技能对外的有效标题与有效简介（说明层留空时回退到当前版本 SKILL.md 的两项） | `Skill.EffectiveTitle` / `Skill.EffectiveSummary` | [internal/skill/skill.go](../internal/skill/skill.go) |
| 本机二进制相对最新发布是旧是新（要不要升级） | 严格版本的解析与比较入口 | [internal/upgrade/version.go](../internal/upgrade/version.go) |

> **配置不得成为权限的来源**。主体、角色、权限码、作用域一律不得由配置提供；默认作用域只能来自主体的绑定关系。见 [docs/design/config/README.md](design/config/README.md)。

> **唯一例外是引导**：系统里还没有任何角色绑定时，用引导配置建立第一个管理员。它是初始化的输入，不是判定的输入——判定路径只读存储、从不读配置。四条边界（物化 / 一次性 / 留痕 / 按不可变标识）见 [AGENTS.md](../AGENTS.md) 第 7 条，生效入口见 [internal/server/bootstrap.go](../internal/server/bootstrap.go)。

> **前端权限判断不是安全边界**。前端 `usePermission` 只决定"要不要渲染"，服务端 `rbac.Engine.Check` 才是唯一有约束力的判定。两者判定逻辑必须一致，见 [docs/design/rbac/frontend-permissions.md](design/rbac/frontend-permissions.md)。

## 加载 / 合并 / 规范化类（Load & Transform）

> 同一次数据加载或规范化只能有一个实现入口。

| 操作内容 | 唯一入口 | 所在文件 |
|---------|---------|---------|
| 服务端配置的五层合并与校验 | `config.LoadServer` | [internal/config/load.go](../internal/config/load.go) |
| CLI 配置的五层合并与校验 | `config.LoadCLI` | [internal/config/load.go](../internal/config/load.go) |
| 分层顺序（默认值 < 配置文件 < 本地覆盖 < 环境变量 < 命令行参数） | 两端展开时共用同一条语义与同一批来源读取函数 | [internal/config/load.go](../internal/config/load.go) |
| CLI 内置默认目标地址的解析（源码常量，或发布构建注入值） | `config.DefaultCLI` 内的解析入口；注入值的生产者是 `make release-build` / `release.yml` | [internal/config/config.go](../internal/config/config.go) |
| CLI 凭证解析（参数 > 环境变量 > 凭证文件） | `auth.Resolve` | [internal/auth/credentials.go](../internal/auth/credentials.go) |
| 日志 logger 构建 | `observability.NewLogger` | [internal/observability/logger.go](../internal/observability/logger.go) |
| trace_id / span_id 的生成与继承 | OTel 传播器，经 `observability.StartServerSpan` / `StartClientSpan` | [internal/observability/tracing.go](../internal/observability/tracing.go) |
| 日志与链路的关联（`trace_id` / `span_id` 字段） | `observability.SpanLogger` | [internal/observability/tracing.go](../internal/observability/tracing.go) |
| 前端链路标识的生成、校验与从响应头取值 | `newTraceparent` / `parseTraceparent` / `traceIdFromHeaders` | [web/src/api/trace-context.ts](../web/src/api/trace-context.ts) |
| 一次前端调用取回服务端的 trace_id，以及一条客户端事件该带哪一次的（成功与失败的取值规则） | `captureTrace` / `traceIdForAction` | [web/src/api/call-trace.ts](../web/src/api/call-trace.ts) |
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
| 超出保留期的客户端事件行的回收时机 | 服务端启动路径上的回收调用 | [internal/server/server.go](../internal/server/server.go) |
| 读侧时间窗折算成半开区间 `[from, to)` | `telemetry.WindowRange` | [internal/telemetry/window.go](../internal/telemetry/window.go) |
| 一棵仓库目录树折成"取哪些字节、跳过哪些"的计划（子路径、链接与子模块的拒绝、超单文件上限的跳过） | `planRepoTree` | [internal/skill/repo_tree.go](../internal/skill/repo_tree.go) |
| `SKILL.md` 的 frontmatter 解析（name / description） | `skill.ParseManifest` | [internal/skill/package.go](../internal/skill/package.go) |
| 使用统计里"哪一天"的折算（两个存储实现共用） | `skill.UsageDay` | [internal/skill/skill.go](../internal/skill/skill.go) |

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
| 发布各阶段的留痕（校验 / 上架 / 落库 / 生效，**不含文本全文**） | `galaxy` 发布流程的埋点（四阶段的唯一入口） | [internal/galaxy/publish.go](../internal/galaxy/publish.go) |
| 一个工程的变更通知谁（按主题扇出、变更合并） | 进程内总线（`Hub` 实例由服务端装配处唯一持有） | [internal/watch/hub.go](../internal/watch/hub.go) |
| 一个主题能不能被订阅（类型前缀、权限码、归属判定） | 主题类型注册表（属主模块注册，通道只查表） | [internal/watch/topic.go](../internal/watch/topic.go) |
| 资产字节上架到公开区 | 上架入口（按内容摘要幂等） | [internal/galaxy/promote.go](../internal/galaxy/promote.go) |
| 客户端把字节直传到对象存储（资产与内容对象共用） | 两端的直传实现；**共享的是凭证形状（`DirectUploadCredential`），代码因跨语言各一份，不得出现第三种形状** | [web/src/upload/direct-upload.ts](../web/src/upload/direct-upload.ts) / [cmd/aladdin/direct-upload.go](../cmd/aladdin/direct-upload.go) |
| 拒绝结论到 RPC 错误码与错误详情的转换 | `Reject` / `DenyByAnnotation`（鉴权拦截器与事件通道的逐主题判定共用） | [internal/server/interceptor/rejection.go](../internal/server/interceptor/rejection.go) |
| 按客户端协议写出错误响应（中间件层） | `connect.ErrorWriter` | [internal/server/middleware.go](../internal/server/middleware.go) |
| 服务端为每个请求起 span、回写 `traceparent` 与 `x-trace-id` 响应头 | `observability.StartServerSpan` / `WriteTraceHeaders` | [internal/observability/tracing.go](../internal/observability/tracing.go) |
| 每个请求留一行可检索的日志（含 trace_id / 过程名 / 结果码 / 耗时） | `telemetryMiddleware.logRequest` | [internal/server/middleware.go](../internal/server/middleware.go) |
| 遥测数据的导出与退出前冲刷 | `observability.Provider` 的 `Shutdown`（导出失败不影响业务） | [internal/observability/provider.go](../internal/observability/provider.go) |
| 指标名、属性键与记录入口 | 常量定义 + `observability.Metrics` 的方法 | [internal/observability/metrics.go](../internal/observability/metrics.go) |
| 客户端事件的校验、脱敏、限流与落盘（日志与库两个去处） | `telemetry.Recorder.Report`（RPC 那一层只是薄壳） | [internal/telemetry/recorder.go](../internal/telemetry/recorder.go) |
| 上报端标识 `x-aladdin-client` 的注入 | Web 传输层的拦截器 / CLI 客户端的请求头注入处 | [web/src/api/transport.ts](../web/src/api/transport.ts) / [pkg/client/client.go](../pkg/client/client.go) |
| 前端客户端事件的上报入口（攒批、失败即时发送、页面隐藏冲刷） | `track`（`startTimer().end` 是它的薄封装，不另起一条上报链路） | [web/src/telemetry/track.ts](../web/src/telemetry/track.ts) |
| 一次客户端动作的耗时怎么量（起点、取整、下限） | `startTimer()` | [web/src/telemetry/track.ts](../web/src/telemetry/track.ts) |
| 命令行本地失败的上报（配置/凭证/用法，退出前发出） | `recordLocalFailure` + `flushClientEvents`（只在退出路径调用） | [cmd/aladdin/telemetry-events.go](../cmd/aladdin/telemetry-events.go) |
| 发布产物的构建与打包（跨平台二进制、前端包、校验和） | `make release-build` | [Makefile](../Makefile) |
| 把产物部署到生产（拉取、校验、替换、重启、回滚） | `deploy/deploy.sh`（产物来源由 `--from` 给出，默认是 Release；**中转不另起一份部署实现**） | [deploy/deploy.sh](../deploy/deploy.sh) |
| 把发布产物放到中转地址（上传 + 逐个对象置公开读；用完删掉） | `deploy/relay` | [deploy/relay/main.go](../deploy/relay/main.go) |
| 本地开发环境的拉起（服务端 + 前端，同起同停） | `make dev`；两边的命令与种子配置各只有一处来源（`DEV_SERVER_CMD` / `WEB_DEV_CMD`），`dev` 与 `dev-server` / `web-dev` 都引用它们 | [Makefile](../Makefile) |
| 技能纳管 / 同步 / 回滚 / 删除的留痕（含来源与提交标识，**不含正文**） | `skill.Service` 的对应方法 | [internal/skill/catalog.go](../internal/skill/catalog.go) |
| 超出保留期的技能使用日次的回收时机 | 服务端启动路径上的回收调用 | [internal/server/server.go](../internal/server/server.go) |
| 本机命令行的替换（原子、按符号链接指向的真实文件） | `upgrade` 的替换入口 | [internal/upgrade/replace.go](../internal/upgrade/replace.go) |

## 数据源类（Data Sources）

> 同一份数据（配置、清单、映射表等）只能有一个读取入口。

| 数据内容 | 唯一入口 | 所在文件 |
|---------|---------|---------|
| 接口契约与消息定义（服务端 + 前端类型） | proto 定义，经 `buf generate` 同时派生出 Go 与 TS 两侧代码 | [api/proto/](../api/proto/) |
| 拒绝原因枚举（Go / TypeScript / CLI 三端同源） | [api/proto/aladdin/rbac/v1/errors.proto](../api/proto/aladdin/rbac/v1/errors.proto) | 由 `buf generate` 派生，三端均引用生成常量 |
| 文档页 ↔ 宿主之间消息的形状（通道名、协议版本、消息类型；页面侧那一段由服务端渲染进产物） | 接入桥协议；**共享的是形状，代码因跨语言各一份，不得出现第三种形状** | [internal/galaxy/doc_frame_bridge.go](../internal/galaxy/doc_frame_bridge.go) / [web/src/pages/galaxy/frame-channel.ts](../web/src/pages/galaxy/frame-channel.ts) |
| 权限码全集（Go 常量与前端常量同源） | 权限目录，经 `make gen` 双向派生 | [api/permissions/catalog.yaml](../api/permissions/catalog.yaml) |
| 客户端事件的类型、动作允许清单与字段（Go / TypeScript 两端同源） | proto 定义，经 `buf generate` 派生 | [api/proto/aladdin/telemetry/v1/telemetry.proto](../api/proto/aladdin/telemetry/v1/telemetry.proto) |
| 一个动作的日志取值、是否允许匿名、可携带哪些属性 | 动作策略表 | [internal/telemetry/actions.go](../internal/telemetry/actions.go) |
| 客户端事件（有界取值）的持久化数据 | telemetry 的存储接口（内存实现与 SQL 实现并存，语义由同一套契约测试守着） | [internal/telemetry/store.go](../internal/telemetry/store.go) / [internal/telemetry/gormstore/](../internal/telemetry/gormstore/) |
| 落库的稳定取值折回协议枚举（读侧） | 由写侧那张表的反向映射派生，不另抄一份 | [internal/telemetry/enums.go](../internal/telemetry/enums.go) |
| 角色 / 权限 / 主体关系的持久化数据 | `rbac.Store` 接口（内存实现与 SQL 实现并存，语义由同一套契约测试守着） | [internal/rbac/store.go](../internal/rbac/store.go) / [internal/rbac/gormstore/](../internal/rbac/gormstore/store.go) |
| 会话（谁、到什么时候为止、作用域）的持久化数据 | 会话存储接口（内存实现与 SQL 实现并存，语义由同一套契约测试守着） | [internal/identity/session.go](../internal/identity/session.go) / [internal/identity/gormstore/](../internal/identity/gormstore/store.go) |
| 身份别名（（来源，身份标识）→ 主体）的持久化数据 | 身份别名的存储接口（内存实现与 SQL 实现并存，语义由同一套契约测试守着） | [internal/identity/identity.go](../internal/identity/identity.go) / [internal/identity/gormstore/identity.go](../internal/identity/gormstore/identity.go) |
| 档案（昵称、简介、头像对象键）的持久化数据 | 档案的存储接口（内存实现与 SQL 实现并存，语义由同一套契约测试守着） | [internal/profile/profile.go](../internal/profile/profile.go) / [internal/profile/gormstore/profile.go](../internal/profile/gormstore/profile.go) |
| 上传的字节怎么进对象存储（签发直传凭证、核对提交结果） | 直传的公共契约（生产实现是 COS，测试注入假实现） | [internal/objectstore/upload.go](../internal/objectstore/upload.go) / [internal/objectstore/cosupload/](../internal/objectstore/cosupload/) |
| 工程、文件清单（草稿与版本）、资产与发布产物清单的持久化数据 | galaxy 的存储接口（内存实现与 SQL 实现并存，语义由同一套契约测试守着）；**字节不在库里** | [internal/galaxy/content_set.go](../internal/galaxy/content_set.go) / [internal/galaxy/gormstore/](../internal/galaxy/gormstore/) |
| 直传凭证里那条策略长什么样（动作、资源、类型与长度条件） | 策略的构造入口 | [internal/objectstore/cosupload/policy.go](../internal/objectstore/cosupload/policy.go) |
| 公开区的对象与它的地址 | 上架入口（公开区唯一的写入口，按（工程，内容摘要，类型）幂等） | [internal/galaxy/promote.go](../internal/galaxy/promote.go) |
| 工程标识与资产标识的分配 | galaxy 的创建入口（分配即冻结、不可猜、不复用）；随机部分取自 `idgen.New` | [internal/galaxy/project.go](../internal/galaxy/project.go) |
| 表结构与迁移清单（库里长什么样） | 迁移清单，由 `migrate.Run` 执行 | [internal/database/schema.go](../internal/database/schema.go) / [internal/database/migrate/migrations.go](../internal/database/migrate/migrations.go) |
| 服务端配置（监听地址、日志级别与路径） | 服务端 `config.yml` + `config.local.yml`，经 `config.LoadServer` 读取 | [internal/config/load.go](../internal/config/load.go) |
| CLI 配置（目标地址、超时、输出详细度） | CLI `config.yml` + `config.local.yml`，经 `config.LoadCLI` 读取 | [internal/config/load.go](../internal/config/load.go) |
| 两端各自认识的配置键 | `serverKeys` / `cliKeys` | [internal/config/file.go](../internal/config/file.go) |
| CLI 凭证（令牌、绑定作用域、过期时间） | 用户配置目录下的 `credentials.json`，经 `auth.Resolve` 读取 | [internal/auth/credentials.go](../internal/auth/credentials.go) |
| 环境变量名（`ALADDIN_` 前缀） | 各模块内集中定义：配置项在 config、凭证在 auth、开发旁路在 server；调用方不得手写字符串字面量 | [internal/config/config.go](../internal/config/config.go) |
| 发布产物清单与校验和（自更新的唯一来源） | `upgrade` 的发布源读取入口 | [internal/upgrade/release.go](../internal/upgrade/release.go) |
| 发行版产物的命名与平台清单（**自更新与界面上的下载命令两处都消费**） | [docs/release.md](release.md) 记录的对外契约；平台取自 Makefile 的 `PLATFORMS` | [Makefile](../Makefile) |
| 界面里的等宽字体栈（代码块、可编辑正文） | `MONOSPACE` | [web/src/theme/monospace.ts](../web/src/theme/monospace.ts) |
| 品牌标识的形状与颜色（页签图标、主屏图标、侧边栏品牌区用的都是它的产物；**组件里不内联路径**） | 标识源文件，各尺寸产物由同一处脚本派生 | [docs/design/web/brand/aladdin-mark.svg](../docs/design/web/brand/aladdin-mark.svg) / [generate-icons.sh](../docs/design/web/brand/generate-icons.sh) |
| 弹窗的高度约束、滚动行为与滚动条落在哪一侧（内容比窗口高时滚的是内容区，不是整屏遮罩；头尾与内容三块的横向边界重合） | `AppModal`（**不再从 antd 直接引 `Modal`**） | [web/src/ui/AppModal.tsx](../web/src/ui/AppModal.tsx) |
| 技能、版本、标签、展示图集、收藏与使用日次的持久化数据 | `skill.Store` 接口（内存实现与 SQL 实现并存，语义由同一套契约测试守着）；**字节不在库里** | [internal/skill/store.go](../internal/skill/store.go) / [internal/skill/gormstore/](../internal/skill/gormstore/store.go) |
| 远端技能内容的拉取出口（解析引用、取回一棵树；**测试注入假实现**） | `skill.Remote` 接口 | [internal/skill/remote.go](../internal/skill/remote.go) |
| 技能目录的接口契约（读面与维护面的划分、权限码、作用域来源） | proto 定义，经 `buf generate` 派生两端代码 | [api/proto/aladdin/skill/v1/skill.proto](../api/proto/aladdin/skill/v1/skill.proto) |
| 一份字节能不能当**展示小图**（收哪些格式、上限多大、扩展名与文件头怎么判） | `imagetype`（**头像与技能展示图共用一份**；它与 galaxy 资产的素材白名单**不是同一个判断**，不合并，理由见该包说明） | [internal/imagetype/imagetype.go](../internal/imagetype/imagetype.go) |
| 技能展示图的对象键（按技能标识与图标识，一张一个键） | `skill.ImageKey` | [internal/skill/image.go](../internal/skill/image.go) |
| 一个技能最多几张展示图、合计多少字节 | `skill.MaxImages` / `skill.MaxImageTotalBytes` | [internal/skill/image.go](../internal/skill/image.go) |
| 一次图集重排给出的顺序是不是当前图集的一个排列（两个存储实现共用） | `skill.IsImagePermutation` | [internal/skill/image.go](../internal/skill/image.go) |
| 代码生成与静态检查工具的版本（本机安装与 CI 缓存 key 都由此派生） | Makefile 的 `TOOLS` 清单 | [Makefile](../Makefile) |
| 门禁里 Go 环境的准备（Go 版本的来源，以及模块 / 构建缓存与工具缓存的 key 与回退策略） | 复合动作（三个 Go job 共用一份） | [.github/actions/setup-go/action.yml](../.github/actions/setup-go/action.yml) |
