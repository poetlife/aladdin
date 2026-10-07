# `skill add` 在 30 秒处超时：客户端默认超时取错了量级

**日期**：2026-10-07
**收敛到的端**：命令行。服务端的取回逻辑本身正常，"远端"也没有任何故障

## 症状

按 spec 的"首批纳管清单"纳管三个真实仓库，**三件里有两件以超时收场**：

| 来源 | 要收的文件数 | 结果 |
|------|-----------|------|
| `Leonxlnx/taste-skill --path skills/taste-skill` | 1 | 约 4 秒，成功 |
| `yanliudesign/mono-color-skill` | 41 | 30 秒处被掐掉，服务端日志 `status: 503, duration_ms: 29431`，技能未落地 |
| `pbakaus/impeccable --path .agent/skills/impeccable` | 56 | 加 `--timeout 10m` 后 51 秒成功 |

客户端拿到的是一句裸的 `deadline_exceeded`：既看不出这是超时**设置**的问题，也看不出
该动哪个参数。而服务端日志里那行 503 会把排查引向 GitHub——**那条路是断的**。

## 收敛过程

四步，每步都排掉一类解释：

1. **数清请求数。** 取回不是一次请求：先问默认分支、再解析引用、再问一次目录树，然后
   **每个要收的文件各取一次字节**（`internal/skill/remote.go`）。因此请求数 = `3 + N`。
   两件失败的分别是 44 次与 59 次，成功那件只有 4 次。
2. **把实测套上去。** 29.6 ÷ 44 ≈ 0.67 秒/次，51 ÷ 59 ≈ 0.86 秒/次——**两边都等于
   "次数 × 每次往返"**，与文件大小无关（mono-color 留下的正文才几十 KB）。于是耗时的
   自变量只有一个：请求数。这也解释了为什么"只有一件成功"——它恰好只有一个文件。
3. **找出是谁设的 30 秒。** 服务端**没有**整条流程的总超时（`internal/server` 里只有
   `ReadHeaderTimeout` / `IdleTimeout` 这类连接级设置，以及单次远端请求 30 秒的
   `http.Client.Timeout`）。给 deadline 的是客户端：`cfg.Timeout`（默认 30 秒）经
   `client.Context()` 变成这一次 RPC 的 deadline，服务端的 handler 上下文跟着到期。
4. **那 503 是哪来的。** 调用方的 deadline 到期 → 服务端那个在飞的
   `http.Client.Do` 返回上下文错误 → `GithubRemote.do` 把它一律包成
   `ErrRemoteUnavailable` → RPC 层折成 `CodeUnavailable` → **HTTP 503**。
   所以那行日志说的是"调用方走了"，不是"远端挂了"。剩下的 0.57 秒差额也吻合：
   客户端的 30 秒从发起时就起算，服务端的 handler 是在连接与编码之后才开始计时的。

## 根因

**默认值取错了量级。** `DefaultTimeout = 30s` 是给**一次普通调用**的数字，而这个命令的
耗时由输入规模决定（`1 + 文件数` 次出站请求，文件数上限 200）。用普通调用的量级去管它，
命令就会在自己的**正常**耗时上超时。

这不是"spec 忘了一件事"，而是**客户端这一侧否定了 spec 自己的论证**：onboarding.md 的
"超时与失败"写着"不需要为整条流程再设一个总超时"，理由是时长本就由文件数决定；而那个
30 秒的客户端默认值**就是一个总超时**，它从客户端一路传导成服务端 handler 的 deadline。

配套的三条判据（看到同质症状先查这些）：

- **`duration_ms` 紧贴超时值**（29.4s 对 30s）→ 是超时，不是远端慢。
- **服务端日志是 503，而远端侧毫无异常** → 查 `do()` 的归类：调用方走了被报成了远端故障。
- **只有文件多的仓库出问题、单文件仓库正常** → 与请求数成正比，不是某个仓库或某个地址有问题。

## 结论与边界

- **命令级默认超时**：`skill add` / `skill sync` 在内置默认值那一层用自己的常量
  （`config.DefaultSkillCatalogTimeout`，由"文件数上限 ÷ 并发上限 × 单次请求超时"推出）。
  它**只占最低那一层**——命令行参数、环境变量、本地覆盖、配置文件任一层给出即覆盖它。
- **取字节并发进行，但有上限**（`blobFetchConcurrency`）。耗时几乎全部来自逐个请求的
  往返，因此并发是这条路径上唯一有效的提速手段；它**一个请求都不少发**，配额消耗与限频
  结论都不变。上限要落在远端自己的并发上限之下。
- **调用方走了不再报成远端故障**：`do()` 与取字节的读路径在上下文已结束时交出上下文
  错误本身，RPC 层把它折成 `DeadlineExceeded` / `Canceled`。否则一次客户端超时会在留痕里
  长成一个 503，把读日志的人送到一个没有故障的远端上去。
- **超时要报得出来是超时**：客户端把 `deadline_exceeded` 转成一句点名"这次超过了多少、
  用哪个参数加长"的提示，并说明重试是安全的（失败本就不留痕迹）。

## 参考

- [internal/skill/remote.go](../../../internal/skill/remote.go)（取回、并发、上下文归类）
- [internal/config/config.go](../../../internal/config/config.go)（两个超时常量的推导）
- [internal/config/load.go](../../../internal/config/load.go)（命令级默认只占最低那一层）
- [cmd/aladdin/root.go](../../../cmd/aladdin/root.go)（`effectiveTimeout` 与超时提示）
- [docs/design/skill/onboarding.md](../../design/skill/onboarding.md)（"取回"、"超时与失败"）
- [docs/design/config/cli-config.md](../../design/config/cli-config.md)（`timeout` 那一行）
