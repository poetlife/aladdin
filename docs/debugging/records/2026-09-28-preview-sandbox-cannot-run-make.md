# 预览窗格起不来：make 在预览沙箱里报 `getcwd: Operation not permitted`

**日期**：2026-09-28
**收敛到的端**：不在三端之内——是桌面 App 预览窗格的沙箱限制，与业务代码无关

## 症状

把 [.claude/launch.json](../../../.claude/launch.json) 的启动命令从 `cd web && npm run dev`
改成 `make dev`（想让预览窗格也走统一入口），预览启动即失败：

```
make: getcwd: Operation not permitted
make: *** No rule to make target `dev'.  Stop.
```

## 收敛过程

三次排除，逐次缩小：

1. **是否相对路径的问题**：改成 `bash -lc "cd web/.. && make dev"`——仍然失败，并额外暴露出
   `shell-init: error retrieving current directory: getcwd ...`。
2. **是否 cwd 的问题**：改成 `bash -lc "make -C <仓库绝对路径> help"`——**仍然失败**。
   `-C` 直接给定绝对路径，说明失败与"进程启动时在哪"无关，是 make 自己调 `getcwd` 被拒。
3. **是否我改坏了配置**：把 launch.json 还原成原来的 `cd web && npm run dev`——正常启动。
   所以沙箱本身没坏，是 make 跑不了。

另外确认了不含命令、只给 `url` 的"附加到已在跑的服务器"写法在本机**未启用**
（提示需要 in-app Browser preview），因此 launch.json 必须自己带一条启动命令。

## 根因

预览窗格启动的进程**无法解析自己的当前工作目录**（`getcwd` 被沙箱拒绝，父目录不可读），
而 `make` 在读取 Makefile 之前就会调用 `getcwd`，于是在做任何事之前就失败了。
`node` / `vite` 不依赖这一步，所以 `npm run dev` 照样能起来。

**这条症状的辨识特征是"make 怎么包装都报 getcwd、而同样的 shell 里 npm 能跑"**：
看到它就不要再尝试换 cwd、换 `-C`、换包装层，也不要去怀疑 Makefile 本身。

## 结论与边界

- **`.claude/launch.json` 里只能放不调用 `getcwd` 的直接命令**。它因此必须自带
  `cd web && npm run dev`，与 Makefile 的 `WEB_DEV_CMD` 是**一份刻意保留的重复**——
  受平台限制，不是可以合并的重复。改动时不要再去"顺手统一"这两处。
- **开发环境的唯一入口是终端里的 `make dev`**（见 [Makefile](../../../Makefile) 的运行段）：
  服务端与前端同起、日志同屏、Ctrl-C 一并停止。预览窗格只负责给前端一个地址，
  不是 dev 环境的启动方式。
- 若哪天预览沙箱能跑 `make`，或本机启用了 url 附加模式，可以直接把 launch.json
  换成不带命令的 url 配置，那时这份重复才该消失。
