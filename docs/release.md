# 发布与 CI

## 触发方式

| 工作流 | 触发 | 做什么 |
|--------|------|--------|
| [ci.yml](../.github/workflows/ci.yml) | PR、push `main` | 跑门禁 |
| [release.yml](../.github/workflows/release.yml) | push tag `vX.Y.Z` | 门禁 → 构建 → 发布 Release → 发布 npm 包 → 清理旧版本 |

两者都不自己定义"什么算通过"，而是共同调用 [gate.yml](../.github/workflows/gate.yml)。

**tag 必须严格形如 `vX.Y.Z`，且不接受前导零**（`v01.2.3` 不合法）。`tags: ['v*']` 只是粗筛，`release.yml` 的 `validate` 步骤用 `^v(0|[1-9][0-9]*)(\.(0|[1-9][0-9]*)){2}$` 再拦一道：形如 `v1.0.0-rc.1` 的 tag 会被拒绝，什么都不会产出。

这个形状**必须与客户端解析能接受的一模一样**（[internal/upgrade/version.go](../internal/upgrade/version.go) 的 `ParseVersion`，见 [design/cli/self-update.md](design/cli/self-update.md)）。放开一点就会发布一个客户端解析不了的版本号，而它的表现是：`releases/latest` 指向它之后，**全体用户的自更新一直失败**，直到出现一个形态干净的 tag。前导零正是这样一处——它让同一个版本有两种写法，严格比较不接受它。

## 门禁（Gate）

门禁的范围只有一处定义：`.github/workflows/gate.yml`。CI 与发布都调它，**不复制步骤**——否则两份定义迟早漂移，例如有人只给其中一份加了前端测试。

门禁是**五个并行 job**，它们是五段互不相干的检查：

| job | 依次执行 |
|-----|---------|
| 静态检查与生成同步 | `make tools`（必须先于本 job 其余几步）→ 断言 `golangci-lint` 存在 → `make lint` → `make check-gen` → `make check-api-docs` |
| 后端单元测试 | `make test` |
| 端到端测试 | `make test-e2e` |
| 前端 | `make web-ci`（按 lockfile 装依赖）→ `make test-web` → `make web-build` |
| SDK | `make sdk-ci`（按 lockfile 装依赖）→ `make test-sdk` → `make sdk-build` → `make sdk-check-package` |

五段之间没有依赖，串行的总时长是它们相加，并行是最慢的那一段。代价是每个 job 各自检出、各自恢复一遍缓存：**机器时间变多，墙钟时间变少**（见下面的"时间花在哪里"）。另一处收益是原先"所有 Go 步骤必须先于 `make web-ci`"这条脆弱约束消失了——前端独立成 job 之后，Go 的 job 里根本不存在 `node_modules`。

一处有意的行为变化：并行的 job 默认不互相取消，**某个 job 失败时其余照常跑完**。这是要的——一次推送就能看到全部结论，而不是"修完 lint 才发现测试也红"。取消仍然发生在推送这一层：`ci.yml` 的 `concurrency` 会取消同一分支上过期的那次运行。

三个 Go job 共用的"装 Go + 恢复缓存"抽在 [.github/actions/setup-go/action.yml](../.github/actions/setup-go/action.yml)：缓存的取法是**同一处判断**，抄三份就一定会各自漂移，而这里漂移的表现是"某天起只有其中一个 job 变慢"，没有任何一步会报错。

工具版本只有一处来源：Makefile 的 `TOOLS`。全部钉到具体版本，不留 `@latest`——留一个，本机与 CI 就可能装到不同版本，失败还会发生在没人动过 proto 的日子里。`make tools` 逐个工具比对"二进制在不在 + 版本一不一致"，一致就跳过：`buf` 与 `golangci-lint` 都是大二进制，从源码编译合计 100s 上下，占了门禁一半的时间。

### 时间花在哪里

门禁的成本大头是**编译**，不是测试本身。同一份代码在本机热缓存下：`make test` 0.8s、`make lint` 1.5s、`make test-e2e` 0.3s、前端两步各 2s 上下。CI 冷缓存下 `make test` 是 49s、`make lint` 是 30s。所以那两处缓存比任何步骤优化都重要，它们的取法在三个 Go job 共用的 [.github/actions/setup-go/action.yml](../.github/actions/setup-go/action.yml)：

- **Go 模块与构建缓存**（`~/go/pkg/mod` + `~/.cache/go-build`）。`setup-go` 自带的缓存 key 只看 `go.sum` 的哈希、且按 git ref 隔离、又没有回退，实测五条缓存条目里只有一条被复用过，因此关掉它，改用显式的 `actions/cache` 加 `restore-keys`。
- **装好的工具**（`~/go/bin`）。key 取 Makefile 的哈希。**缓存 key 是派生物，不是第二份版本定义**；回退到旧缓存也安全，因为 `make tools` 会逐条比对版本戳记，只重装真正变了的那一个。

这两处缓存回退之所以放心，是因为它们过期只意味着"多编译一遍"：`GOMODCACHE` 是不可变的版本目录，`GOCACHE` 由输入哈希寻址，旧条目只可能 miss、不可能算错。

**有意不缓存 `~/.cache/golangci-lint`**：那一个过期了可能让一条 finding 不再出现，而"少报一条"与"没有门禁"是一回事。判据是缓存过期后损失的是时间还是正确性——只损失时间的才缓存。

**并行是照着实测数字拆的**。拆之前的实测构成（最近 20 次成功运行的平均值）：job 内 217s，加排队与收尾约 21s，合计约 3 分 58 秒。其中 Go 缓存恢复 19s、`make lint` 23s、`make test` 72s、`make test-e2e` 29s、前端三步合计 52s。`make test` 与前端三步之间没有任何依赖，却排在同一条队里——这就是拆分依据。

并行之后，关键路径换成最慢的那一个 job，另外三段与之重叠。每个 job 都要各付一次约 20s 的 Go 缓存恢复（这份 817MB 的缓存下载约 6s、解压约 11s），它买到的是冷缓存下每个 job 多出来的 40~60s 编译。

拆分的首次实测（就是拆完的那一推，一次成功运行）：**run 总时长 75s**，四个 job 分别 73s / 70s / 65s / 63s。**这组数字里没有 SDK**——那时还没有它，SDK 是后来加的第五个 job。最慢的是**端到端测试**，不是后端单元测试——关键路径落在哪一段，取决于该次运行里哪一段最慢，不是固定的。

两处"搬家之后变慢"，都在关键路径之外，且都有结构性原因：

- `make test-e2e` 44s（原先约 29s）：e2e 那个 job 不再能复用同一个 job 里 `make test` 刚编好的 race 产物。GOCACHE 只按 `go.sum` 的哈希存一份、每个 job 各恢复一次，**不跨 job 共享**。
- `make web-ci` 19s（原先约 9s）：四个 job 同时恢复各自的缓存。

反方向也有：这一次 `make test` 37s、前端单元测试 29s，都低于拆之前的均值（72s / 38s）。单次运行之间还有 runner 机型与缓存状态的抖动——拆之前 19 次的 `make test` 就在 52~82s 之间浮动。所以这里写死的结论是**关键路径**（从"九步相加"变成"最慢的那一个 job"），不是某一步的具体秒数。

一处顺序不能随手调换：**`make tools` 先于 `make lint` 与 `make check-gen` / `make check-api-docs`**（前者要 `buf lint`，后两者要 `buf generate`）。三者都在"静态检查与生成同步"这一个 job 内。

原先还有一条"所有 Go 步骤先于 `make web-ci`"：`go test ./...` 会走进 `web/node_modules`，而 npm 包里是有自带 Go 文件的（`flatted` 就带一个）。先装前端依赖，等于把第三方 JS 依赖里的 Go 代码卷进 Go 检查——今天恰好能编过，但它不该成为门禁成立的前提。**拆分之后这条不再需要**：前端在另一个 job 里，Go 的 job 里不存在 `node_modules`。

断言 `golangci-lint` 存在那一步值得单独说明：`make lint` 在本机缺 golangci-lint 时会**静默跳过**它。门禁里静默跳过等于没有门禁，所以这里显式断言，把"跳过"变成失败。golangci-lint 的版本随之收进 Makefile 的 `TOOLS`，不再由各人本机另行安装。

发布流水线里门禁排在构建**之前**：门禁失败则后续 job 不执行，不会留下半个 Release。

## 产物

`make release-build VERSION=<tag>` 是产物的唯一构建入口，工作流只调它、不重写 `go build`（否则构建命令会有第二个入口，与 `make build` 漂移）。

版本号一路来自 tag：`GITHUB_REF_NAME` → `make release-build VERSION=…` → Makefile 里既有的 `LDFLAGS`。**不新增第二处 `-X main.version`**。

CLI 产物里另有一处注入：**内置默认目标地址**——发布出去的 CLI 默认连官方服务，这样"下载下来就能登录"成立（行为见 [design/config/cli-config.md](design/config/cli-config.md)）。它与版本号是两件事，不共用同一个 `-X`，规则照旧：地址注入也**只有一处**。

- 值由仓库变量 `ALADDIN_CLI_DEFAULT_ADDRESS` 提供，构建时传给 `make release-build RELEASE_CLI_ADDRESS=…`。**它是部署实例的值，不进仓库**——与 nginx 模板里的 `__SITE_DOMAIN__` 是同一条约束（见 [deploy.md](deploy.md) 的可验证性表）。
- **只注入 CLI 二进制**：服务端产物不带这个值，它无论在哪种构建形态下都只监听回环。
- 值缺失时**构建失败**，而不是发出一个默认连本机的 CLI。守卫放在 `release.yml`，因此本机的 `make release-build` 不受影响（它不带这个变量，产物默认连本机——**本机构建的发布产物不代表发布出去的那种**）。
- 值必须是 `host:port` 形态（地址不接受 scheme 与路径）：形状不对时**构建失败**。运行时也有同一套校验，但那已经是发出去之后的事了。
- 构建后**必须校验注入真的落进了二进制**：`-X` 在符号名写错或符号不可达时会**静默失效、不报错**。没有这一步，就会发出去一个悄悄回落到本机默认值的产物，而所有门禁都是绿的。

```bash
grep -aqF -- "<注入值>" dist/aladdin || 中止
```

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
- **命名与 `SHA256SUMS` 从此是对外契约。** 命令行的自更新会解析两者（见 [design/cli/self-update.md](design/cli/self-update.md)）：包名形如 `aladdin_<tag>_<os>_<arch>.tar.gz`，校验和清单是 `<摘要>  <文件名>` 的标准两列格式。改命名、改清单格式、或把二进制从包根挪走，都会让**已经装出去的命令行**失去升级能力——而它们的表现是使用者那一侧的一句"升级失败"，不会在这里报任何错。

### npm 包（SDK）

`@poetlife/aladdin-sdk` 与上面那些产物同源：同一个 tag、同一次门禁。它由 `release.yml` 的 `publish-npm` 发，排在 Release 之后——npm 的版本**发出去就撤不回来**，因此让它发生在"这次发布确实成立"之后；真落在中间（有 Release、npm 上没有）时重跑那个 job 即可补齐。

- **走 npm 的 Trusted Publishing（OIDC）**：`id-token: write` 是它需要的全部东西。仓库里因此**没有 npm 的长期 token**，也没有谁要在发版时输一次性口令；**provenance 自动生成**，包页面上写着它来自哪个仓库的哪一次运行。这条是选它的主要理由——本地发布那条路每一次都躲不过浏览器验证，而那正是"发布"这件事里最不该由人肉承担的一环。
- **一次性前置**（不在仓库里，配一次即可）：npmjs.com → 包 → Settings → **Trusted Publisher** → GitHub Actions，填 Organization/user `poetlife`、Repository `aladdin`、Workflow filename `release.yml`、Environment 留空，**Allowed actions 勾上 `npm publish`**（只勾 `npm stage publish` 的话，每次发版都要一个维护者用 2FA 点一次批准）。包必须先存在才能配——所以**第一次发布只能走下面那条应急路**。
- **版本只有一个来源：发布 tag。** 打包前用 `npm version "${GITHUB_REF_NAME#v}"` 覆写包里的 `version`，仓库 [sdk/package.json](../sdk/package.json) 里那个值是占位（`0.0.0-development`）。**不维护第二处版本号**：包内的 proto 派生物与包来自同一个 tag，因此"SDK 版本 = 服务端版本"直接成立，不需要第二张兼容矩阵。发布发生在 tag 之后，所以也不会出现"预支了一个号、那个 tag 又不落在这份内容上"的账。
- **包里有什么由 `files` 与 `prepack` 决定**，门禁与发布前都跑打包检查（`make sdk-check-package`）。**`dist/` 不进版本库**，而 `npm pack` 在缺产物时**不报错**——它只是安静地打出一个只含 `package.json` 的包。
- **发布是幂等的**：npm 上已有该版本就跳过而不是报红，流水线因此可以重跑（npm 的版本不可回收，也没有像 Release 那样的 prune）。
- **发布后 registry 上可能先出现一个 `0.0.0-stage` 占位版本**（首次发布实测如此，约两三分钟后真版本才放行）：npm 会先占住新包名，真版本在暂存区等放行，期间 `npm install` 拿到的是那个占位版、`npm view` 看到的 `latest` 也是它。`npm stage list` 查状态。**别把它当成发布失败。**
- **许可证正文是根 [LICENSE](../LICENSE) 的一份副本**（`sdk/LICENSE`）：npm 只收包目录里的许可证文件，收不到包外的。改根许可证时要同步这一份。

#### 应急：本地发一次（`make sdk-publish`）

流水线发不成、或要跳过发版流程补一版时才用它。**它不是主路**，代价要认：这条路上没有 provenance，而且每一次都躲不过浏览器 2FA。

- 缺省取 HEAD 上那个 tag（`git describe --tags --exact-match`），**要求 HEAD 就在 tag 上**（这时包里的字节与那个 tag 逐字一致）。tag 还没打就是要用它的号：`SDK_VERSION=v0.7.3` ——**那次预支把号用掉了**，npm 不允许重发同一个版本，因此那个 tag 必须恰好落在这次发布的内容上，否则只能跳到下一个号。**包名第一次发布时只能走这里**（Trusted Publisher 要包先存在）。
- 两条守卫：工作区必须干净（发出去的字节要能对应到一次提交）、npm 上已有该版本就跳过（幂等）。发完把 `npm version` 改写过的两个文件还原，仓库里的占位号不变。
- **账号必须先开 2FA**，否则发不出去：npm 要求发布者有 2FA 或一把开了 **Bypass 2FA** 的 granular token；没开时注册表回 **E403**，而 npm 只在 **EOTP / E401** 时才提示输口令——它不问，也不说缺的是账号上的那一步。开好之后 npm 会开浏览器让你验证一次。细节与实测（含一条走错的排查路径）见 [debugging/records/2026-10-12-npm-publish-403-without-otp-prompt.md](debugging/records/2026-10-12-npm-publish-403-without-otp-prompt.md)。

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

**这条策略对命令行自更新的含义**：产物地址随 Release 一起消失（tag 还在，但 `…/releases/download/<tag>/<产物>` 会 404）。因此自更新只面向 **latest**，不提供"升级到某个指定旧版本"的形态——那会在第 6 次发版之后开始随机失败，而失败与用户的版本落差相关，是个很难复现的表象。

## 主站的 CLI 镜像（只留最新）

命令行自更新在权威发布源**暂时**不可用时会落到主站上的一份镜像（行为见 [design/cli/self-update.md](design/cli/self-update.md)）。它在本文登记，是因为它与 Release 一样承载着**跨端契约**：改了它的形态，等于让已经装出去的命令行失去兜底。

| 项 | 值 |
|----|-----|
| 位置 | 官方站点的 `/cli/latest/`——固定路径，**不含版本号字面量** |
| 内容 | 各平台 CLI 包 + 该次发布的 `SHA256SUMS` + 一份版本元数据 |
| 保留策略 | **1**：只留最新，覆盖写，不滚历史（与 Release 保留 5 个是两回事） |
| 谁写它 | **服务器上的部署动作**，在部署收尾时从 GitHub 的 latest 拉取、核对后覆盖写（见 [deploy.md](deploy.md)）。发布流水线不产出它，CI 也**不持有任何能写它的凭证** |
| 写失败怎么办 | 只告警，不影响 Release 与本次部署；客户端仍可走 GitHub |

几点：

- **命名与清单格式照搬既有契约，不另立一套。** 包名仍是 `aladdin_<tag>_<os>_<arch>.tar.gz`，清单仍是同一次发布的 `SHA256SUMS`（逐字节相同）。镜像比 Release 多的只有一份版本元数据。
- **版本元数据的形态是对外契约。** 它说明"现在是哪个 tag、主站上有哪些产物"，其中的产物表只能**收窄**可用集合、不能改名——客户端仍按上文的命名契约从 tag 推出自己要的文件名。
- **落盘前经过与 Release 同一份 `SHA256SUMS` 的核对**，因此主站上不存在"这一个包是别的版本"的中间态。
- **它只在部署时刷新。** 发版后没有部署，镜像就还是上一版——这是它的定位（兜底，不是替代），代价写在 spec 的"边界与约束"里。

## 本地怎么发版

先确认门禁在本地是绿的，再打 tag——否则推上去才发现失败，比本地发现更晚：

```bash
make web-ci && make sdk-ci && make tools && make lint && make test && make test-e2e && make check-gen && make check-api-docs && make test-web && make test-sdk && make sdk-check-package
```

本地验证产物（`VERSION` 换成等价于 tag 的值）：

```bash
make release-build VERSION=v0.0.0-test
```

本地看 npm 包里装了什么（这条不改任何东西，也不联网发布）：

```bash
cd sdk && npm pack --dry-run
```

发 SDK 到 npm（**应急才用**——主路是流水线在 tag 上发；这条路缺省要求 HEAD 已在 tag 上，包名第一次发布时也只能走它）：

```bash
make sdk-publish                          # 发完 tag 之后
make sdk-publish SDK_VERSION=v0.7.3       # 预支一个号（那次预支把号用掉了，代价见上）
make sdk-publish SDK_VERSION=v0.7.3 SDK_OTP=123456   # 只有当 npm 没提示、你手上又有码时才用
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
| 门禁跑得慢 | 日志里出现成片的 `>>> go install`，说明工具缓存没命中（正常情况下 `make tools` 全是 `>>> 已就绪`）。看 `缓存已装好的工具` 那一步是 hit 还是 miss；`setup-go` 自己的缓存只按 `go.sum` 哈希与 git ref 隔离，加个依赖或换个分支就会 miss，不必指望它 |
| Release 数量没有收敛到 5 | 看 `prune` 步骤的日志：它每次都会打印候选集与将要删除的列表 |
| `publish-npm` 报 401 / 404 | npm 上的 **Trusted Publisher 没配**，或仓库/工作流名对不上（Workflow filename 只填文件名 `release.yml`）。包必须先存在才能配，因此**包名第一次发布只能走应急路** |
| `publish-npm` 报 npm 版本太旧 | 基础镜像轮换过，runner 上的 npm < 11.5.1；job 里那一步会把这句话直接说出来 |
| 发布后 registry 上只有 `0.0.0-stage` | 不是失败：npm 处理新版本的暂存状态，过一会儿（首次实测约两三分钟）真版本才放行。`npm stage list` 查状态 |
| （应急路）npm 上出现了 `0.0.0-development`，或一个几 KB 的空包 | 版本取自 tag 那一步没生效，或包在没有构建产物的情况下被打包。两者都有守卫（`make sdk-publish` 的版本来源、打包检查），看日志里哪一条没跑到——`npm publish` 自己不会为此报错 |
| （应急路）`make sdk-publish` 报"工作区不干净" | 那是守卫，不是故障：先提交，再发（发布的内容要能对应到一次提交） |
| （应急路）`make sdk-publish` 报"HEAD 不在任何 tag 上" | 位置不对：要么先把 tag 打在这个提交上再发，要么用 `SDK_VERSION=vX.Y.Z` 显式预支一个号 |
| （应急路）`make sdk-publish` 报认证失败（401 / ENEEDAUTH） | 本机没有凭证：`npm login` |
| （应急路）`make sdk-publish` 报 403 且**没有**提示输 OTP | 账号没开 2FA（`npm profile get` 的 `tfa` 是 `false`）：npm 要求发布者有 2FA 或一把 Bypass 2FA 的 token，而注册表回的是 E403、npm 只在 EOTP / E401 时才提示口令，于是它不问也不说缺什么。先把 2FA 开起来（浏览器里），或建一把 Bypass 2FA 的 granular token。判据与实测见 [debugging/records/2026-10-12-npm-publish-403-without-otp-prompt.md](debugging/records/2026-10-12-npm-publish-403-without-otp-prompt.md) |
| npm 上出现了两个版本、而 tag 只有一个 | 预支号时把号发重了：那个 tag 的内容发不出来（npm 不允许重发同一版本），只能跳到下一个号 |
