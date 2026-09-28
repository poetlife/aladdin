# API 文档 模块总览

> 本目录是 aladdin **"对外怎么调这些接口"** 这一问题的唯一信源。

## 背景与目标

proto 是接口契约的唯一信源，注释也够密。但它只对能读到 proto 的人可见——外部调用方、写客户端的人、评审接口设计的人需要一份可读的文档，而不是去 clone 仓库。

生成 OpenAPI 是最省力的载体：工具链成熟，能直接喂 Swagger UI / Redoc 之类的渲染器。

**但不是所有生成方式都可以。** grpc-gateway 那类方案会为同一个方法再开一条 REST 路径（`POST /v1/login`），于是文档描述的东西与真实调用形状分叉——这正是 [buf.gen.yaml](../../../buf.gen.yaml) 一直拒绝它的理由。本模块选的生成器描述的是 **Connect 路径本身**（`POST /aladdin.identity.v1.IdentityService/Login`），与实际请求同形，因此不产生第二条路径，也就不违反那条理由。

**每条路径只给一个动词（POST）。** 只读方法在服务端**另外也接受 GET**（标了 `idempotency_level = NO_SIDE_EFFECTS`，理由与限制见 [CLAUDE.md 的"传输方式的既定选择"](../../../CLAUDE.md)），但文档不为它在同一条路径下再列一条 `get` operation——两条同名条目只是把同一个方法说了两遍。这一事实写进方法的说明里，读者一样能拿到，侧栏却不重复。

成功的衡量标准：

1. 文档从 proto 派生，**不手写**——proto 改了文档必然跟着改，或由 CI 拦下。
2. 每个 RPC 方法都标明鉴权要求：公开 / 仅需认证 / 需要哪个权限码、作用域从哪来。
3. 文档描述的调用形状与实际一致（Connect 路径），不另起一套 REST；服务端额外接受的动词（只读方法的 GET）在说明里写明。
4. 文档与 proto 的同步由 CI 保证，不依赖人记得重跑。

## 模块边界

| 问题 | 归属 |
|------|------|
| 接口契约与消息定义 | proto（[api/proto/](../../../api/proto/)） |
| 一个方法需要认证 / 需要哪个权限码 | `rbac.Resolve`（[internal/rbac/annotation.go](../../../internal/rbac/annotation.go)） |
| 对外可读的接口文档 | **本模块** |
| 站内哪个入口指向它 | web 的站内文档区（见 [../web/docs-area.md](../web/docs-area.md)）——一条链接，不是一章 |

两个方向上的边界都要守住：

- **不参与判定。** 文档是派生物，不是安全边界。改文档不改变任何一次鉴权结论。判定只发生在拦截器里。
- **不成为第二条调用路径。** 文档描述的就是 Connect 路径，不引入 REST 转码层。

## 功能行为

### 两步生成，缺一不可

```
buf generate --template buf.gen.openapi.yaml   # 产标准 OpenAPI
go run ./internal/tools/openapigen             # 补鉴权扩展
```

`make api-docs` 已把两步串好。

**为什么必须两步**：OpenAPI 生成器只认识公开的注解族（gnostic、protovalidate、`google.api.http`），看不见 aladdin 自己的鉴权注解。只跑第一步得到的文档里，`public` 与 `authenticated_only` 的方法长得一模一样——而这恰恰是调用方最先要知道的信息。

第二步**不重新解析注解**：OpenAPI 的 path 就是 RPC 过程名，直接喂给 `rbac.Resolve`。判定语义与拦截器同源，不可能漂移。

### `x-aladdin-auth` 契约

每个 operation 上挂一个扩展：

| `kind` | 含义 | 附带字段 |
|--------|------|---------|
| `public` | 免认证免鉴权 | 无 |
| `authenticated_only` | 仅需认证，任何已确认主体可调用 | 无 |
| `requires` | 需认证 + 需指定权限 | `permission`、`scope-source` |
| `denied` | 注解遗漏 | 无 |

```yaml
/aladdin.identity.v1.IdentityService/WhoAmI:
  post:
    x-aladdin-auth:
      kind: authenticated_only

/aladdin.rbac.v1.RBACService/GetRole:
  post:
    x-aladdin-auth:
      kind: requires
      permission: rbac.role.read
      scope-source: SCOPE_SOURCE_REQUEST_FIELD
```

`GetRole` 是只读方法，它在服务端**另外也接受 GET**，但文档只给上面这一种形状——那个事实写在方法的说明里（见下文的「幂等」行），不占一条并列的同名条目。

**只写出真正参与判定的字段**：`authenticated_only` 的方法拦截器在权限检查之前就放行了，不看作用域，所以不写 `scope-source`——写了会让读者以为它有约束力。`requires` 则相反，未声明作用域来源时鉴权以"作用域不符"拒绝，所以哪怕取值是 `SCOPE_SOURCE_UNSPECIFIED` 也如实写出。

`kind: denied` 表示方法没写注解。**文档里出现它说明有缺陷**：`internal/rbac` 的测试会拦在构建期（见 [服务端权限](../rbac/server-permissions.md)）。

取值 `public` / `authenticated_only` / `requires` 是**对外契约**，由 `rbac.Kind.String()` 单点给出，不随日志文案调整而变。

同一个结论还有**第二种呈现**：每个方法的 `description` 开头会多一行可读的鉴权说明，例如"**鉴权**：需要权限 `rbac.role.read`；作用域取自请求字段。"。

只读方法还会再多一行：「**幂等**：本方法无副作用，可安全重试；服务端在同一路径上也接受 GET。」——读者不必自己知道 Connect 里"GET ⇒ 无副作用"这条映射，也不必因为文档只画了 POST 就以为 GET 不可用。它的事实来源是 proto 的 `idempotency_level`，经 `rbac.MethodDescriptor` 从**与鉴权注解同一份方法描述符**读回，本模块只做呈现。刻意不从"文档里有没有 `get` operation"反推：那是让文档的形状去决定文档的内容。

两种都要。标准渲染器只渲染 `description`：自定义扩展要么根本不显示，要么以原始 JSON 显示——对读文档的人，`{"kind":"requires","permission":"rbac.role.read"}` 等于没说。扩展留给机器，文字留给人，两者由同一次生成产出，不会不一致。

### 产物管理

产物有两处，都**进版本库**——与 `api/gen` 同模式：文档变更随 PR 可评审。

| 产物 | 用途 |
|------|------|
| [api/openapi/](../../../api/openapi/) | 分文件，每个 proto 一份 |
| [web/public/api-docs/openapi.yaml](../../../web/public/api-docs/openapi.yaml) | 合并版，渲染页用——渲染器只认单份 spec |

`make check-api-docs` 校验两者与 proto 同步：重新生成后它们相对已提交版本必须无差异。

- 用 `git status` 而非 `git diff`：新增服务会多出**未跟踪**的文档文件，而 `git diff` 看不见未跟踪文件，那会让"忘了为新服务生成文档"逃过校验。
- 已接入 CI 门禁（[.github/workflows/gate.yml](../../../.github/workflows/gate.yml) 的 `OpenAPI 文档同步校验`）。

### 渲染页

[web/public/api-docs/index.html](../../../web/public/api-docs/index.html) 是一个静态页，用 Redoc 渲染合并文档，前端构建后通过 `/api-docs/` 访问。

- **选 Redoc 而不是 Swagger UI**：它是三栏阅读布局，排版更适合连续阅读；Swagger UI 的 "Try it out" 在这里用不上——Connect 需要 `Connect-Protocol-Version` 头与鉴权凭证，从页面上直接发请求成功率很低，而用不上的功能却要一直看着。
- `redoc.standalone.js` 由 `npm run sync-redoc` 从 node_modules 复制（`predev` / `prebuild` 会自动跑），**不进版本库**。
- 页面是静态资源，与业务前端一样对匿名可访问。见"待定决策"里加访问控制的那一条。

## 边界与约束

本模块**明确不处理**：

- 权限码的取值与含义：[权限目录](../../../api/permissions/catalog.yaml)。
- 判定的正确性：由 `internal/rbac` 负责，本模块只把结论抄进文档。
- REST 转码：不做，理由见上文。

约束：

- **文档不得手改。** 它是派生物。手改会在 CI 被 `check-api-docs` 拦下，且下次生成即被覆盖。
- **每条路径只给一个动词。** 工具按此产出契约工作，并在路径下出现多个动词时**硬失败**而不是只注入其一——只注入其一会让另一个动词悄悄没有鉴权信息，而 CI 拦不住。真要并列展示多个动词，先扩展工具使其逐个注入。
- **新增服务必须在 [internal/tools/openapigen/descriptors.go](../../../internal/tools/openapigen/descriptors.go) 登记一行**，否则该服务的方法会以"未找到服务描述符"报错。这是刻意的：报错好过静默产出一份缺扩展的文档。
- **扩展开关不得有默认值。** 方法三选一（public / authenticated_only / requires）是硬约束，不提供"未声明即公开"这类缺省。

## 依赖关系

| 依赖对象 | 交互方式 |
|---------|---------|
| proto | **只读**：文档的接口、消息、注释都来自它 |
| RBAC | **只读**：调 `rbac.Resolve` 取鉴权结论、调 `rbac.MethodDescriptor` 取方法描述符（幂等等标准选项由此读回） |
| buf / OpenAPI 生成器 | 第一步用它产原始文档；版本钉在 Makefile 的 `TOOLS` |
| CI | 校验文档与 proto 同步 |

依赖方向单向：本模块读 proto 与 rbac，两者都不感知本模块。

## 待定决策

| 决策 | 推荐默认 | 替换影响范围 |
|------|---------|-------------|
| 渲染页是否加访问控制 | 暂不加，与业务界面一样公开 | 要改 nginx 或加登录态判断；文档内容不受影响 |
| 单份文档的合并方式 | 自研合并（schema 名是全限定的，跨文件不撞名） | 上游若支持单文件输出，可去掉自研合并 |
| 是否生成客户端 SDK | 暂不做 | 与本模块共用同一份 OpenAPI；需评估生成的 SDK 与 `api/gen` 的关系 |

## 代码实现索引

> 以下为代码位置索引，便于在 spec 与实现之间导航。索引内容随代码变化更新。

| 职责 | 文件路径 |
|------|---------|
| 第一步的 buf 模板 | [buf.gen.openapi.yaml](../../../buf.gen.openapi.yaml) |
| 文档的加载、注入与写回 | [internal/tools/openapigen/main.go](../../../internal/tools/openapigen/main.go) |
| 鉴权信息的两种呈现（扩展 + 描述行） | [internal/tools/openapigen/auth_ext.go](../../../internal/tools/openapigen/auth_ext.go) |
| 多份文档合并成单份 | [internal/tools/openapigen/merge.go](../../../internal/tools/openapigen/merge.go) |
| 服务描述符的注册登记 | [internal/tools/openapigen/descriptors.go](../../../internal/tools/openapigen/descriptors.go) |
| 方法描述符与鉴权注解的唯一读取入口 | [internal/rbac/annotation.go](../../../internal/rbac/annotation.go) |
| 渲染页 | [web/public/api-docs/index.html](../../../web/public/api-docs/index.html) |
| 生成与校验入口 | [Makefile](../../../Makefile) 的 `api-docs` / `check-api-docs` |
| 产物 | [api/openapi/](../../../api/openapi/) 与 [web/public/api-docs/openapi.yaml](../../../web/public/api-docs/openapi.yaml) |

---

> spec 是功能行为的唯一信源，代码是实现的唯一信源。
> 功能行为变更 → 先改 spec；实现细节变更 → 只改代码。
