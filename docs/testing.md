# 测试指南

## 单元测试原则

- **拆分可测试逻辑**：长逻辑应拆分为职责单一的小函数，避免将复杂业务逻辑集中在一处，确保每个函数独立可测。
- **优先使用 Table Driven Tests**：通过表格驱动的方式组织测试用例，将输入、期望输出统一定义为数据表，循环执行断言，减少重复代码，便于扩展新用例。
- **无副作用（No Side Effects）**：单元测试不应依赖或修改外部状态（文件系统、数据库、网络、全局变量等），保证测试可重复执行且结果稳定。
- **最小功能化**：每个测试只验证一个最小功能点，逐步积累覆盖，确保每层逻辑稳定后再向上组合。
- **AI 不可删除**：任何已有单元测试不得由 AI 自行删除或跳过；若测试因代码变更而失败，必须由人工确认并决定修复或移除，以保证代码稳定性。

```go
tests := []struct {
    name  string
    input SomeType
    want  SomeType
}{
    {"case 1", input1, expected1},
    {"case 2", input2, expected2},
}
for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
        got := FunctionUnderTest(tt.input)
        assert.Equal(t, tt.want, got)
    })
}
```

## RBAC 的测试要求

鉴权是安全边界，测试要求高于一般模块：

- **矩阵覆盖**：角色 × 权限 × 作用域的决策结果必须以表格驱动方式穷举，不允许只测"正例通过"。
- **拒绝优先**：显式拒绝与默认拒绝必须各有用例，且验证拒绝原因（`reason`）正确。
- **引擎与接入分离**：`internal/rbac` 的决策引擎测试不得依赖 gRPC；拦截器测试只验证"身份与权限码提取正确 + 拒绝语义正确"，不重复验证引擎逻辑。

## 持久化的测试要求

库结构一旦发出去就只能追加、不能改写，因此这里的测试承担的是"防止旧库与新库长成两个样子"：

- **迁移幂等**：同一个库上连续执行两次迁移，第二次不改变任何结构、不报错。
- **未知版本拒绝**：库中存在代码里没有的版本时，迁移必须失败而不是继续。
- **结构快照**：迁移后的表、列与唯一约束与一份**冻结的快照**比对。改动表结构定义却忘了追加迁移时，这条测试必须失败——它是"迁移已冻结"这条约定唯一的执行者。快照更新时先回答"这次变更本应有配套迁移吗"，有则先补迁移。
- **实现语义一致**：内存实现与 SQL 实现跑**同一套**契约用例，同一输入返回同样的结果与**同样的错误类别**。"没找到"与"库出错"混淆时，契约测试必须失败。
- **持久化确实生效**：用同一个库文件构造两次存储，第二次能读到第一次写入的数据。

测试用临时目录下的 sqlite 文件，不依赖外部数据库服务；`t.TempDir()` 保证并行执行时互不干扰。

## 目录与命名规范

- Go：测试文件与实现文件同目录，命名 `<file>_test.go`，包名与实现包一致（内部测试，非 `_test` 外部包）
- Web：测试文件与实现文件同目录，命名 `<Component>.test.tsx` / `<module>.test.ts`；跨模块的集成夹具放 `web/src/test/`
- 端到端：`test/e2e/`，不参与 `go test ./...` 的默认执行，由独立 Makefile target 触发

## 如何运行测试

```bash
# 运行全部测试（Go，含竞态检测）
make test

# 运行单个包
go test -race ./internal/rbac/...

# 运行单个用例
go test -race -run TestEngine_Check ./internal/rbac/...

# Web 单元测试（watch 模式见 web/package.json）
make test-web

# 端到端
make test-e2e
```

## 生成产物的同步校验

`make test` 内的 `internal/rbac/catalog_test.go` 会额外校验三件事，它们失败时**不要**用改测试的方式绕过：

1. 生成产物与 `api/permissions/catalog.yaml` 同步（不一致时运行 `make gen`）；
2. proto 方法注解引用的权限码都已在权限目录中登记；
3. 每个 gRPC 方法都能被归入 public / authenticated-only / requires-permission 三类之一——没有被归类的就是漏写注解的业务方法。

## 覆盖率

```bash
make cover          # 生成 coverage.out 并打印各包汇总
make cover-html     # 生成 coverage.html 供浏览器查看
```

覆盖率是观察指标而非门禁指标。**RBAC 决策引擎（`internal/rbac`）例外**：该包的新增分支未经测试覆盖不得合并。
