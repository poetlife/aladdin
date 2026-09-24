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

> **前端权限判断不是安全边界**。前端 `usePermission` 只决定"要不要渲染"，服务端 `rbac.Engine.Check` 才是唯一有约束力的判定。两者判定逻辑必须一致，见 [docs/design/rbac/frontend-permissions.md](design/rbac/frontend-permissions.md)。

## 加载 / 合并 / 规范化类（Load & Transform）

> 同一次数据加载或规范化只能有一个实现入口。

| 操作内容 | 唯一入口 | 所在文件 |
|---------|---------|---------|
| 服务端 / CLI 配置加载与默认值合并 | `config.Load` | [internal/config/config.go](../internal/config/config.go) |
| 日志 logger 构建 | `observability.NewLogger` | [internal/observability/logger.go](../internal/observability/logger.go) |
| trace_id 的生成 | `observability.NewTraceID` | [internal/observability/logger.go](../internal/observability/logger.go) |
| trace_id 的注入与继承 | `observability.EnsureTraceID` | [internal/observability/logger.go](../internal/observability/logger.go) |

## 副作用类（Side Effects）

> 同一类副作用（写日志、发通知、apply 资源等）只能有一个实现入口。

| 副作用内容 | 唯一入口 | 所在文件 |
|---------|---------|---------|
| 结构化日志输出 | `observability.NewLogger` 返回的 `*zap.Logger` | [internal/observability/logger.go](../internal/observability/logger.go) |
| 前端出站请求：凭证注入、链路标识注入、线格式选择、会话失效处理 | `createTransport`（前端出站请求的唯一出口） | [web/src/api/transport.ts](../web/src/api/transport.ts) |
| 鉴权决策留痕（含 subject/permission/decision/reason） | `rbac.Engine.Check` 内部统一埋点 | [internal/rbac/engine.go](../internal/rbac/engine.go) |
| 拒绝结论到 RPC 错误码与错误详情的转换 | `reject` / `DenyByAnnotation` | [internal/server/interceptor/rejection.go](../internal/server/interceptor/rejection.go) |
| 按客户端协议写出错误响应（中间件层） | `connect.ErrorWriter` | [internal/server/middleware.go](../internal/server/middleware.go) |

## 数据源类（Data Sources）

> 同一份数据（配置、清单、映射表等）只能有一个读取入口。

| 数据内容 | 唯一入口 | 所在文件 |
|---------|---------|---------|
| 接口契约与消息定义（服务端 + 前端类型） | proto 定义，经 `buf generate` 同时派生出 Go 与 TS 两侧代码 | [api/proto/](../api/proto/) |
| 拒绝原因枚举（Go / TypeScript / CLI 三端同源） | [api/proto/aladdin/rbac/v1/errors.proto](../api/proto/aladdin/rbac/v1/errors.proto) | 由 `buf generate` 派生，三端均引用生成常量 |
| 权限码全集（Go 常量与前端常量同源） | 权限目录，经 `make gen` 双向派生 | [api/permissions/catalog.yaml](../api/permissions/catalog.yaml) |
| 角色 / 权限 / 主体关系的持久化数据 | `rbac.Store` 接口 | [internal/rbac/store.go](../internal/rbac/store.go) |
