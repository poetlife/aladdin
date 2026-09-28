# CLI 报 `frame header looked like an HTTP/1.1 header`：目标地址上不是 RPC 端点

**日期**：2026-09-29
**收敛到的端**：命令行——传输层与目标地址解析，与业务代码无关

## 症状

在一台新机器上下载 release 后执行 `aladdin login`：

```
aladdin: rpc error: code = Unavailable desc = connection error: desc =
"error reading server preface: http2: failed reading the frame payload: http2: frame too large,
 note that the frame header looked like an HTTP/1.1 header"
```

一句话里同时出现了 HTTP/2 与 HTTP/1.1，看起来像协议协商出了问题，很容易被读成"服务端/反代把 h2c 说崩了"。

## 收敛过程

1. **先看这条报错的判据**。CLI 只讲明文 h2c（`pkg/client/client.go` 用
   `insecure.NewCredentials()`），它把对面返回的字节当 HTTP/2 的 server preface 解析。
   报错里出现 `HTTP/1.1` 字样，说明**对面回的是一份 HTTP/1.1 报文**——即那个地址上
   不是 Connect/gRPC 端点。对照实验把它坐实了：

   | 对面是什么 | 报错 |
   |-----------|------|
   | 裸 HTTP/1.1 服务（`python3 -m http.server`） | `error reading server preface: http2: frame too large`（本次这条） |
   | 什么都没监听 | `dial tcp …: connect: connection refused` |

   两类失败的措辞不同，可以据此区分"对面有服务但不是 RPC 端点"与"对面没有服务"。
   （结尾那句 `note that the frame header looked like an HTTP/1.1 header` 只在对面发的
   确实是 `HTTP/1.1 …` 状态行时出现；`python -m http.server` 发的是 `HTTP/1.0`，因此
   复现时少这一句。）

2. **排除代理**。把 `HTTPS_PROXY` / `HTTP_PROXY` 指向一个 HTTP/1.1 服务后重试，
   调用照常成功——grpc-go v1.84 对**明文**连接不走代理，因此"环境里有残留代理变量"
   不是原因。

3. **排除"明文打生产域名"**。用明文请求生产域名的 80 与 443，两种情况都是连接被直接
   关闭、不回任何 HTTP/1.1 内容。因此即便目标写成了生产域名，也不会产生这条报错
   （会是"连接关闭"那一类）。

4. **核对发布产物的默认地址**。`make release-build` 只注入 `main.version` 与
   `main.released`（`Makefile`），默认地址仍是 `config.DefaultAddress = 127.0.0.1:9090`，
   且 tarball 里只有一个裸二进制。**发布出去的 CLI 从不指向生产。**

## 根因

两层：

- **直接原因**：那次调用解析出的地址上，确实有一个讲 HTTP/1.1 的服务在应答。默认地址是
  `127.0.0.1:9090`，新机器上这个端口若被别的服务占着就会得到这条报错（Prometheus 的
  默认端口正是 9090，是常见的一例）。
- **根本原因**：发布产物的 CLI 默认只连本机，而文档把"连生产"写成需要自己开 SSH 隧道。
  于是"下载下来就能用"这件事根本不成立——现场只表现为一个与协议有关的怪错。

## 结论与边界

- 再见到 `server preface` / `frame header looked like an HTTP/1.1 header`：**先确认目标
  地址**（`aladdin --debug …` 会打印解析出的地址，本次一并把 login 路径也纳入了这行输出），
  再去怀疑反代。地址对了才轮到"反代把原生 gRPC 当 HTTP/1.1 转发了"这条。
- 与 `connection refused` 是两回事：它说明对面**有**服务，只是不是 RPC 端点。
- 修复方向（发布版默认指向生产、非回环强制 TLS、命令行改走 Connect 而不是原生 gRPC）
  见 [deploy.md](../../deploy.md) 的「CLI 怎么连生产」与 [release.md](../../release.md) 的「产物」。
  命令行为什么不走原生 gRPC，见 [2026-09-29-grpc-trailers-dropped-by-nginx.md](2026-09-29-grpc-trailers-dropped-by-nginx.md)。
