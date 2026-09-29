# 原生 gRPC 经 nginx 丢错误响应：空正文响应的 trailers 被吞掉

**日期**：2026-09-29
**收敛到的端**：服务端入口——反向代理（nginx 1.24）对 gRPC 响应的转发，与 aladdin 的代码无关

## 症状

命令行改走「经反向代理直连生产」之后，**所有错误响应都变成一个空的 Unknown**：

```
aladdin: rpc error: code = Unknown desc =
```

而同一个端点的成功响应完全正常。服务端日志显示那条请求 `status 200`，nginx 的
error log 干净——三边看起来都没问题，只有客户端拿不到错误内容。

## 收敛过程

1. **服务端本身没问题**：同一次调用绕过反代（SSH 隧道到 9090，明文 h2c）返回的是
   正常错误——`凭证已失效，请重新登录`。
2. **看 nginx 两侧的日志**：访问日志是
   `POST /aladdin.identity.v1.IdentityService/WhoAmI HTTP/2.0" 200 0`——上游回了
   200 且**零字节**；error log 没有任何条目。
3. **抓原始响应**（curl 直接发一个 gRPC 帧，`content-type: application/grpc` +
   `te: trailers`）对比：

   | 链路 | 响应头 | trailers |
   |------|--------|----------|
   | 直连 9090 | `content-length: 0`、`trailer: Grpc-Status,…` | 有 `grpc-status: 16`、`grpc-message: …` |
   | 经 nginx | 同样声明了 `trailer: …` | **一条都没有** |

4. **换一个有正文的响应**（健康检查）再试：经 nginx 的 trailers **正常到达**
   （`grpc-status: 0`）。→ 判据是**空正文**：`content-length: 0` 且没有 DATA 帧时，
   nginx 认为响应已经结束，不再处理随后的 trailers 帧。
5. **排除自己的配置**：在发布域（不承载应用流量）上临时挂一个**无条件** `grpc_pass`
   的 location，结果一模一样 → 与 `if in location` 无关，是 `grpc_pass` 本身的处理。

## 根因

nginx 1.24（Ubuntu 24.04 的 apt 版本）对「上游在初始 HEADERS 里就声明
`Content-Length`、没有 DATA 帧、随后单独发 trailers」这种响应形状处理不当，把它当成
已经结束的响应，于是 **trailers 被丢掉，客户端只看到一个空的成功响应**。

上游是 connect-go：它的错误响应正是这个形状（响应头 + 独立 trailers），而 grpc-go
的 "trailers-only" 单帧形状不在同一分支上——这也解释了为什么"gRPC 放 nginx 后面"
这个组合在别处看着没事：多数 gRPC 服务端是 grpc-go。

nginx 上游直到 2026-08 才动这段逻辑（[nginx/nginx#1591](https://github.com/nginx/nginx/pull/1591)），
而且方向是把越界情形判成 `502`，不是修好我们的这一种。

## 结论与边界

- **不要把 gRPC 放在 HTTP 层反向代理后面**，除非验证过它的错误响应。判据很具体：
  一个「空正文 + trailers」的错误响应能不能原样到达客户端。
- 命令行因此改走 **Connect**：错误放在 HTTP 状态与响应体里，不依赖 trailers，走的正是
  浏览器每天在用的那条路径（见 AGENTS.md 的"传输方式的既定选择"）。
- 服务端仍然同时提供 gRPC / gRPC-Web。端到端测试里的 grpc-go 客户端走的是**直连
  h2c**、不经过反代，所以不受这一条影响——"三种协议结论一致"这条保证依然成立。
- 复现判据（不需要 CLI）：

  ```bash
  printf '\x00\x00\x00\x00\x00' > /tmp/frame.bin
  curl -s -D - --http2 -H 'content-type: application/grpc' -H 'te: trailers' \
    --data-binary @/tmp/frame.bin \
    https://<域名>/aladdin.identity.v1.IdentityService/WhoAmI | tail -5
  ```

  响应头之后**看不到 `grpc-status`** 就说明这个反代吞掉了 gRPC 的 trailers。
