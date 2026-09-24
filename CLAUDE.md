# aladdin

## 项目概览

阿拉丁神灯。前后端与命令行一体的仓库：

| 端 | 技术栈 | 入口 |
|----|--------|------|
| 服务端 | Go + grpc-go | [cmd/aladdin-server](cmd/aladdin-server) |
| 命令行 | Go + cobra | [cmd/aladdin](cmd/aladdin) |
| Web 前端 | React 19 + antd 6 + Vite | [web/](web) |

三端之间的传输是 **Connect RPC**（connect-go）：服务端一个端口同时讲 Connect / gRPC / gRPC-Web，浏览器走 Connect、CLI 走原生 gRPC，共享同一份业务实现。参考实现 usememos/memos。

三端共享同一套 RBAC 权限体系：一份权限模型、一个决策引擎、一套权限码定义。

## 文档索引

| 文档 | 路径 | 说明 |
|------|------|------|
| RBAC 权限体系 | [docs/design/rbac/](docs/design/rbac/README.md) | 权限模型、决策语义、三端接入方式 |
| 全局配置与凭证 | [docs/design/config/](docs/design/config/README.md) | 配置来源分层与合并语义、服务端启动配置、CLI 配置与凭证保护 |
| 可观测性总览 | [docs/observability.md](docs/observability.md) | 日志、指标、追踪的接入方式与规范 |
| 测试指南 | [docs/testing.md](docs/testing.md) | 测试目录规范与运行命令 |
| SSOT 注册表 | [docs/ssot-registry.md](docs/ssot-registry.md) | 同一件事唯一入口的登记表 |
| 历史排障记录 | [docs/debugging/registry.md](docs/debugging/registry.md) | 查问题前先检索同质症状 |

> 功能设计文档位于 `docs/design/`，每个模块一个文件或目录。新增模块时在此表格追加一行。

## 常用命令

```bash
make gen        # 由 proto 与权限目录生成两端代码（唯一生成入口）
make build      # 构建服务端与 CLI
make test       # Go 全量测试（含竞态检测）
make test-e2e   # 端到端测试
make test-web   # 前端测试
make lint       # gofmt + go vet + buf lint + golangci-lint
make dev        # 以开发种子数据启动服务端
make web-dev    # 启动前端开发服务器
```

## 开发规范

- spec 是功能行为的唯一信源；代码是实现的唯一信源
- spec（`docs/design/`）中只描述"做什么"和"为什么"，不描述函数签名、数据结构或算法步骤
- 功能行为变更必须先更新 spec，再修改代码

## 权限相关的硬性约定

这几条一旦违反就会产生安全或一致性问题，不属于"风格偏好"：

1. **权限码只有一个来源**：`api/permissions/catalog.yaml`。Go 常量与前端常量都由它经 `make gen` 派生，任何一端都不得手写权限码字符串字面量。
2. **判定逻辑只有一处实现**：服务端的 `internal/rbac`。前端与 CLI 的本地判断只做权限码集合的成员测试，**不是安全边界**。
3. **受控接口的权限声明写在 proto 里**（方法注解），不在拦截器里逐条硬编码。方法必须三选一：`public` / `authenticated_only` / `required_permission`。
4. **前端不缓存判定结果**，CLI 也不缓存判定结果来决定是否发起调用——两者都无法感知服务端的角色变更。
5. **"无权限"与"未认证"不得混为一谈**，否则客户端会把一次权限配置错误放大成登录风暴。
6. **前端不得手写接口类型或拒绝原因枚举**——两者都由 `buf generate` 从 proto 生成。手写一份就会与 proto 漂移，而漂移的表现形式是"能点但点了报错"。
7. **配置不得成为权限的来源**：主体、角色、权限码、作用域一律不得由配置文件或环境变量提供，默认作用域只能来自主体的绑定关系。新增一个配置项前先问一句"这个键能让谁获得什么"，答案不是"什么也不能"就不该存在。同理，任何以配置项形式出现的"跳过鉴权""信任该来源"开关都不允许存在——这类开关一旦存在就一定会被用在生产环境。

违反 1–3 中的任何一条，构建期的 `internal/rbac/catalog_test.go` 会失败。

## 传输方式的既定选择

前端到服务端走 Connect，已经定下来了，不再是开放问题。它的两个直接后果：

- **只有一份业务实现**：判定发生在协议无关的鉴权拦截器里，三种协议共用。改了判定逻辑不会出现"gRPC 生效、Connect 没生效"。
- **改判定必须三种协议都对**：端到端测试同时用 grpc-go 客户端与 Connect 客户端打同一个端口，断言结论一致（`test/e2e/`）。只测一种协议等于没测。

## 模块化设计原则

AI 辅助开发时代，代码生成速度远超人工 review 速度。良好的模块化是让代码库保持可理解、可审查的关键防线。

**目录结构即架构**：目录层级应反映职责边界，而不是随意堆放。新建文件前先想清楚它属于哪个模块，放错位置比命名错误更难纠正。

**文件名必须有实际意义**：

- 文件名应能独立表达其职责，不依赖上下文才能理解
- 避免：`utils.ts`、`helper.go`、`common.go`、`misc/`
- 推荐：`role-model.md`、`credential-resolver.go`、`permission-gate.tsx`

**单一职责**：一个文件只做一件事。文件开始"又做 A 又做 B"时，是拆分的信号，而不是继续往里加的理由。

**依赖方向显式化**：模块间依赖应单向、可见。循环依赖是模块边界画错的症状，发现时应重新审视边界，而不是绕开它。

**Go 侧的额外约定**：

- `internal/` 放不对外暴露的实现；`pkg/` 放可被外部复用的包
- 判定路径（`internal/rbac`）依赖只读的 `Store` 接口；写能力在单独的 `MutableStore` 上，从类型上排除"判定过程中顺手改数据"
- 生成代码放 `api/gen/` 与 `*_gen.go`，不手工修改

## Single Source of Truth 原则

同一件事在整个仓库里**必须只有一个事实标准来源**。优先级高于"就近实现"和"临时便捷"。

判定标准（满足任意一条就视为"同一件事"）：

- 同一个**判断**（例：某工具是否可用、某资源是否存在、某版本是否符合要求）
- 同一次**加载 / 合并 / 规范化**（例：读取并合并配置、规范化字段、解析引用）
- 同一类**副作用**（例：写结构化日志、发通知、apply 资源）
- 同一个**数据源**（例：服务映射、配置清单、状态数据）

禁止行为：

- 脚本 A 用方法 1 做了判断，脚本 B 做**同样**判断时另写方法 2（即使"两行就搞定"）
- 把公共函数复制一份做"轻微定制"，而不是回流到公共模块
- 文档里就同一件事给出与代码实现不一致的描述

改动前必做：

1. 查 [`docs/ssot-registry.md`](docs/ssot-registry.md)，确认要做的判断 / 操作 / 数据读取是否已有登记
2. 已登记 → 直接复用；需求不满足时改那一个入口，不得在调用侧绕开
3. 未登记但会跨模块复用 → 抽到公共模块后**在注册表里登记一行**

## 排障约定

**查问题前必须先检索 [`docs/debugging/registry.md`](docs/debugging/registry.md)**，确认是否有同质症状的既有结论。同一个症状不应在仓库里出现两套排查结论（SSOT 原则的延伸）。

aladdin 是三端仓库，同一个"看不到数据"的表象可能来自三个完全不同的地方。registry 中有一张"按端检索"的收敛表，排障时先按端收敛再深入。

排查结束后，以 `<YYYY-MM-DD>-<症状简述>.md` 记录到 `docs/debugging/records/`，并在 registry 补登一行。
