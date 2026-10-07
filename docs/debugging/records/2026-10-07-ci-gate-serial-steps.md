# CI 门禁每次都要 4 分钟：不是变慢，是九步串行在同一个 job 里

## 症状

感觉最近每次推送都要等很久。19 次运行的实测：门禁 job 217s（平均），加排队与收尾约 21s，一次运行约 **3 分 58 秒**。

## 先证伪"变慢"

最近 10 次的 job 均值 212s，更早 9 次是 206s——**基本持平**。没有回归，是结构问题：`.github/workflows/gate.yml` 把 9 个步骤排在同一个 job 里，总时长等于**所有步骤相加**。

## 怎么量到"哪一步花在哪"

`gh run view --log` 与 `gh api .../jobs/<id>/logs` 都会先往 `~/.cache/gh` 写缓存，沙箱下会失败并交出空文件。绕开：

```bash
# job 与 step 级耗时（本次用的是这一条）
gh api repos/poetlife/aladdin/actions/runs/<run-id>/jobs \
  --jq '.jobs[] | .steps[] | "\(.name)\t\((.completed_at|fromdate)-(.started_at|fromdate))"'

# 完整日志：先把 token 取出来，再让 curl 跟 302 到 blob
T=$(gh auth token | tr -d '\n')
curl -sL -H "Authorization: Bearer $T" \
  "https://api.github.com/repos/poetlife/aladdin/actions/jobs/<job-id>/logs" -o /tmp/job.log
```

日志本身还会交代缓存与测试框架的账（vitest 会打印 `Duration … (import 33%, tests 33%, environment 32%)`，`actions/cache` 会打印 `Cache Size: ~817 MB` 与下载速率）。

## 时间账（19 次运行的均值）

| 阶段 | 均值 |
|------|------|
| 检出 + 装 Go + 恢复 Go 缓存 + 装 Node + 工具缓存 | 36s |
| `make lint` | 23s |
| `make test`（`-race`） | **72s** |
| `make test-e2e` | 29s |
| `make check-gen` + `make check-api-docs` | 1.4s |
| `make web-ci` | 9s |
| `make test-web` | **38s** |
| `make web-build` | 5s |

关键事实：`make test`(72s) 与前端三步(52s) 之间**没有任何依赖**，却排在同一条队里。

## 处置

按"哪几段互不相干"拆成四个并行 job（静态检查与生成同步 / 后端单元测试 / 端到端测试 / 前端），关键路径从"相加"变成"最慢的那一个"。三个 Go job 共用的"装 Go + 恢复缓存"抽到 `.github/actions/setup-go/action.yml`，缓存 key 只有一处。判据与实测数字写在 `docs/release.md` 的门禁一节。

一处顺带收益：原先"所有 Go 步骤必须先于 `make web-ci`"（`go test ./...` 会走进 `web/node_modules`，npm 包里自带 Go 文件）这条脆弱约束，因为前端独立成 job 而不再可能被违反。

## 结果

拆完那一推的实测：**run 总时长 75s**（3 分 58 秒 → 1 分 15 秒），四个 job 73s / 70s / 65s / 63s，四个都绿。

最慢的那一个是**端到端测试**，不是后端单元测试——"关键路径是哪一段"随每次运行变化，不是固定的。

两处"搬家之后变慢"，都在关键路径之外，且都有结构性原因：`make test-e2e` 44s（原约 29s），因为 e2e 那个 job 不再能复用同一个 job 里 `make test` 刚编好的 race 产物（GOCACHE 每个 job 各恢复一次，不跨 job 共享）；`make web-ci` 19s（原约 9s），因为四个 job 同时恢复各自的缓存。

## 看过但**没有**做的

- **`go test` 分片**：本仓库的 Go 1.27 **没有** `-shard`（`go help testflag` 里没有这个 flag，`go list -shard` 直接报未定义），分片要自己按包切，复杂度高于收益。
- **vitest 的 `isolate: false` / `pool: 'vmThreads'`**：本机实测分别把 30 个用例与 1 个用例打挂（后者是 `content-digest.test.ts` 在 vm 上下文里没有 `crypto.subtle`），属于用测试可靠性换时间。
- **按路径跳过文档类提交**：只占 3/40，且会让"门禁"变成有条件的东西。
