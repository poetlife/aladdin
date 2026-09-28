# Debug Registry（排障索引）

查问题前先检索本表，确认是否有同质症状的既有结论。
同一个症状不应在仓库里出现两套排查结论。

---

| 症状关键词 | 根因摘要 | 排障记录 |
|-----------|---------|---------|
| 管理员登录后进入 /forbidden；接口全放行但界面一个入口都进不去 | 会话权限码里的通配未展开，前端拿到的集合只有 `"*"` | [2026-09-27-admin-login-forbidden.md](records/2026-09-27-admin-login-forbidden.md) |
| 预览窗格启动失败、make 报 `getcwd: Operation not permitted`（同一 shell 里 npm 却能跑） | 预览沙箱的进程无法 `getcwd`，而 make 启动即调用它；launch.json 只能放不依赖 `getcwd` 的直接命令 | [2026-09-28-preview-sandbox-cannot-run-make.md](records/2026-09-28-preview-sandbox-cannot-run-make.md) |
| 前端测试在本机一条告警都没有、CI 上却刷屏 | vitest 5 的默认 reporter 不打印通过用例的 console 输出；用 `--reporter=verbose` 才复现 | [2026-09-29-local-tests-hide-console-output.md](records/2026-09-29-local-tests-hide-console-output.md) |

---

## 按端检索提示

aladdin 是三端仓库，同一个"看不到数据"的表象可能来自三个完全不同的地方。定位时先按端收敛：

| 表象 | 先查 |
|------|------|
| 前端按钮消失 / 菜单不显示 | 前端会话权限码集合是否为空 → 服务端是否返回了权限码（见 [frontend-permissions](../design/rbac/frontend-permissions.md)） |
| 前端请求 403 / PermissionDenied | 服务端鉴权拦截器的 `reason` 字段，不要先怀疑前端 |
| CLI 报权限不足 | CLI 凭证是否过期 / 目标环境是否配错；`aladdin --debug` 打印的决策链路 |
| 服务端整体拒绝所有请求 | 认证拦截器（身份提取）先于鉴权拦截器，先确认身份是否解析成功 |
| 服务端重启后角色/授权全没了 | 先确认启动日志里的数据库定位信息是不是你以为的那个库——默认是**工作目录**下的 `aladdin.db`，在不同目录启动就会连到不同的库（见 [persistence](../design/persistence/schema.md)） |
| 服务端启动即退出、报结构或版本错误 | 库结构与二进制不匹配：查看日志里的迁移结论；未知版本会被拒绝启动（见 [persistence](../design/persistence/README.md)） |
