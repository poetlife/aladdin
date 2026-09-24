# 发布与 CI

## 触发方式

| 工作流 | 触发 | 做什么 |
|--------|------|--------|
| [ci.yml](../.github/workflows/ci.yml) | PR、push `main` | 跑门禁 |
| [release.yml](../.github/workflows/release.yml) | push tag `vX.Y.Z` | 门禁 → 构建 → 发布 Release → 清理旧版本 |

两者都不自己定义"什么算通过"，而是共同调用 [gate.yml](../.github/workflows/gate.yml)。

**tag 必须严格形如 `vX.Y.Z`**。`tags: ['v*']` 只是粗筛，`release.yml` 的 `validate` 步骤用 `^v[0-9]+\.[0-9]+\.[0-9]+$` 再拦一道：形如 `v1.0.0-rc.1` 的 tag 会被拒绝，什么都不会产出。

## 门禁（Gate）

门禁的范围只有一处定义：`.github/workflows/gate.yml`。CI 与发布都调它，**不复制步骤**——否则两份定义迟早漂移，例如有人只给其中一份加了前端测试。

门禁依次执行：

1. `make tools` —— 装代码生成与静态检查工具（必须先于下面两项）
2. 断言 `golangci-lint` 存在
3. `make lint`
4. `make test`
5. `make test-e2e`
6. `make check-gen`
7. `make web-ci` —— 按 lockfile 装前端依赖
8. `make test-web`
9. `make web-build`

两处顺序不能随手调换：

- **`make tools` 先于 `make lint` 与 `make check-gen`**：前者要 `buf lint`，后者要 `buf generate`。
- **Go 步骤全部先于 `make web-ci`**：`go test ./...` 会走进 `web/node_modules`，而 npm 包里是有自带 Go 文件的（`flatted` 就带一个）。先装前端依赖，等于把第三方 JS 依赖里的 Go 代码卷进 Go 检查——今天恰好能编过，但它不该成为门禁成立的前提。

第 2 步值得单独说明：`make lint` 在本机缺 golangci-lint 时会**静默跳过**它。门禁里静默跳过等于没有门禁，所以这里显式断言，把"跳过"变成失败。golangci-lint 的版本随之收进 Makefile 的 `TOOLS`，不再由各人本机另行安装。

发布流水线里门禁排在构建**之前**：门禁失败则后续 job 不执行，不会留下半个 Release。

## 产物

`make release-build VERSION=<tag>` 是产物的唯一构建入口，工作流只调它、不重写 `go build`（否则构建命令会有第二个入口，与 `make build` 漂移）。

版本号一路来自 tag：`GITHUB_REF_NAME` → `make release-build VERSION=…` → Makefile 里既有的 `LDFLAGS`。**不新增第二处 `-X main.version`**。

产物落在 `dist/`：

| 产物 | 说明 |
|------|------|
| `aladdin-server_<tag>_linux_amd64.tar.gz` | 服务端 |
| `aladdin-server_<tag>_darwin_arm64.tar.gz` | 服务端 |
| `aladdin_<tag>_linux_amd64.tar.gz` | CLI |
| `aladdin_<tag>_darwin_arm64.tar.gz` | CLI |
| `aladdin-web_<tag>.tar.gz` | 前端静态资源（内含 `dist/`） |
| `SHA256SUMS` | 以上各包的校验和 |

发布时另由 `gh release create --generate-notes` 生成变更日志。

几点取舍：

- **平台只覆盖 `linux/amd64` 与 `darwin/arm64`**。要加平台改 Makefile 的 `PLATFORMS` 即可，工作流不用动。
- **前端单独打包**。服务端不内嵌前端（Go 侧没有 `go:embed`），三端独立部署，所以前端有自己的产物。
- **交叉编译带 `-trimpath` 与 `CGO_ENABLED=0`**，产物可复现且不依赖动态库。
- 前端产物名带版本号：前端界面不显示版本，`web/dist` 也不进 Go 二进制，文件名是它唯一的版本载体。

## 保留最近 5 个版本

`release.yml` 的 `prune` 步骤在发布成功后清理更早的 Release，只留最近 5 个。

这是全流程唯一**不可逆**的操作，防护都写在步骤注释里，要点：

- 只处理 tag 形如 `vX.Y.Z` 的 Release。人工创建的、名字不匹配的一律不进候选集——这同时是"不碰非本流水线产物"的边界。
- **只删 Release，绝不删 git tag**（不带 `--cleanup-tag`）。tag 是不可变历史，且 `make` 的版本推导依赖它；删了会连带破坏下一次发版的版本号。
- 候选数未超过 5 时什么都不做。
- 被标为 `isLatest` 的那个无论如何不删。
- 读不到列表就中止，绝不把"什么也没看见"当成"全都该删"。
- 删除量有上界：只删溢出区间，单个删除失败也不中断整体。

两次发版由 `concurrency: group: release` 串行，避免清理时读到过期的列表。

> 清理写在 `release.yml` 里，而不是单独做一个 `on: release: published` 的工作流：用 `GITHUB_TOKEN` 创建的 Release **不会**触发其他工作流（GitHub 防递归的规定），那种写法对本流水线自己发的 Release 根本不生效。

## 本地怎么发版

先确认门禁在本地是绿的，再打 tag——否则推上去才发现失败，比本地发现更晚：

```bash
make web-ci && make tools && make lint && make test && make test-e2e && make test-web && make check-gen
```

本地验证产物（`VERSION` 换成等价于 tag 的值）：

```bash
make release-build VERSION=v0.0.0-test
```

然后打 tag 并推送：

```bash
git tag v0.1.0 && git push origin v0.1.0
```

## 排查

| 症状 | 先看哪里 |
|------|---------|
| 门禁在 CI 失败但本机通过 | golangci-lint 版本（CI 用 `make tools` 装的 v2.x，本机可能是 v1）、Node 版本（CI 固定 24） |
| `make check-gen` 报生成产物不同步 | 忘了跑 `make gen` 并提交；也可能是 `buf.gen.yaml` 的远程插件版本变了 |
| 门禁卡在 `buf` 相关步骤 | `buf.gen.yaml` 用了远程插件 `buf.build/bufbuild/es`，需要能访问 Buf Schema Registry |
| tag 推上去了但没产出 Release | 先看 `validate` 是否因 tag 形状被拒 |
| 门禁跑得慢 | `make tools` 每次要从源码装 buf 与 golangci-lint。Go 构建缓存由 setup-go 复用，首次最慢，之后明显变快 |
| Release 数量没有收敛到 5 | 看 `prune` 步骤的日志：它每次都会打印候选集与将要删除的列表 |
