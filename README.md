# aladdin

阿拉丁神灯 —— 前端、服务端与命令行一体的仓库。RBAC 权限体系是底座，之上有身份登录、个人档案，以及 galaxy：用户创作工程与资产、发布成一个可公开访问的站点。

## 简介

三端共享同一套 RBAC 权限体系：

- **服务端**（Go + Connect）：权限模型与决策引擎的唯一实现处，是唯一的安全边界。一个端口同时讲 Connect / gRPC / gRPC-Web
- **命令行**（Go + cobra）：走 Connect 的客户端，与服务端共享同一份业务实现
- **前端**（React + antd）：走 Connect，按服务端下发的权限码集合做展示裁剪

三端之间的接口契约由 `api/proto/` 单一来源派生：服务端 handler、前端类型与 service descriptor 都出自同一次 `buf generate`。

在权限底座之上，仓库已落地的核心模块：

| 模块 | 一句话 | 设计文档 |
|------|--------|---------|
| 权限 rbac | 角色、权限码、作用域；判定只在服务端 | [docs/design/rbac/](docs/design/rbac/README.md) |
| 身份 identity | 登录方式、主体标识、会话凭证 | [docs/design/identity/](docs/design/identity/README.md) |
| 档案 profile | 昵称、头像、简介 | [docs/design/profile/](docs/design/profile/README.md) |
| 对象存储 objectstore | 临时凭证 + 客户端直传 + 提交核对 | [docs/design/objectstore/](docs/design/objectstore/README.md) |
| galaxy | 工程与多版本、资产库、发布成可公开访问的站点 | [docs/design/galaxy/](docs/design/galaxy/README.md) |
| 配置 config | 配置来源分层与合并；凭证不进配置文件 | [docs/design/config/](docs/design/config/README.md) |
| 持久化 persistence | 库结构、迁移语义、后端选择 | [docs/design/persistence/](docs/design/persistence/README.md) |

完整文档索引见 [AGENTS.md](AGENTS.md)——三端各自的文档、发布与部署、测试与排障都在那里。

## 快速开始

### 环境要求

| 工具 | 版本 | 说明 |
|------|------|------|
| Go | 1.27+ | 服务端与 CLI |
| Node.js | 20+ | 前端 |
| buf | 1.73+ | proto 代码生成（`make tools` 可自动安装） |

### 安装

```bash
git clone https://github.com/poetlife/aladdin.git
cd aladdin

make tools        # 安装代码生成工具
make gen          # 由 proto 与权限目录生成代码
make build        # 构建 bin/aladdin-server 与 bin/aladdin

make web-install  # 安装前端依赖
```

### 运行

```bash
# 一键拉起本地开发环境：服务端（开发种子数据，RPC 监听 127.0.0.1:9090）
# 与前端（http://localhost:5173）。两端日志同屏，Ctrl-C 一起停。勿用于生产。
make dev
```

只想单独起一边时用下面两个，`make dev` 就是它们的组合：

```bash
make dev-server   # 只起服务端，同时服务 Connect / gRPC / gRPC-Web
make web-dev      # 只起前端，http://localhost:5173
```

在另一个终端用种子凭证登录 CLI：

```bash
export ALADDIN_ADDRESS=127.0.0.1:9090
export ALADDIN_TOKEN=dev-token
export ALADDIN_SCOPE=tenant/acme

aladdin whoami
aladdin permissions
aladdin role list
```

上面三条是权限冒烟。真实登录（`aladdin login`，含设备码流程）与 galaxy 的创作、发布入口见 [docs/design/identity/](docs/design/identity/README.md)、[docs/design/galaxy/](docs/design/galaxy/README.md) 与 `aladdin galaxy --help`。

## 配置与凭证

服务端与 CLI **各自**有一份配置文件，不共用：`address` 在服务端是**监听**地址、在 CLI 是**目标**地址，共用一份会让配置在两端之间传递时静默连错地方。

| 端 | 配置文件默认位置 | 示例 |
|----|----------------|------|
| 服务端 | 启动时工作目录下的 `config.yml` | [config.server.example.yml](config.server.example.yml) |
| CLI | 用户配置目录下的 `aladdin/config.yml` | [config.cli.example.yml](config.cli.example.yml) |

取值按 **内置默认值 < `config.yml` < `config.local.yml` < 环境变量 < 命令行参数** 合并，每层只覆盖它显式写出的键。也可以用 `-config` / `--config` 或 `ALADDIN_CONFIG` 指向别的路径；个人本机改动写进 `config.local.yml`（已忽略，不提交）。

凭证**不放进配置文件**，而是单独存放（用户配置目录下的 `aladdin/credentials.json`，权限 0600，由 `aladdin login` 写入）：配置可以提交、可以共享、可以进镜像，凭证不可以。

数据默认落在 **sqlite**：不写任何数据库配置时，服务端在工作目录下建 `aladdin.db`，结构迁移随启动自动完成（幂等）。换 MySQL 只需改 `database_driver` 与 `database_dsn`，业务代码与迁移都不用动。库结构与迁移语义见 [docs/design/persistence/](docs/design/persistence/README.md)。

可观测性上报也走配置文件（`otel_endpoint` / `otel_insecure` / `otel_sample_ratio`，两端同名）。**留空只是不上报**：链路标识照常生成、经 W3C `traceparent` 传播、并在响应头回写，因此没有 Collector 的环境一样能用追踪。完整规则见 [docs/design/config/](docs/design/config/README.md) 与 [docs/observability.md](docs/observability.md)。

## 项目结构

```
aladdin/
├── api/       # 契约唯一信源：proto 接口与权限码目录
├── cmd/       # Connect 多协议服务端 与 cobra CLI 入口
├── internal/  # 各业务模块（唯一入口清单见 docs/ssot-registry.md）
├── web/       # React + antd 前端
└── docs/      # 设计文档（文档索引见 AGENTS.md）
```

这棵树只给到方向：同一个包内的唯一入口（配置与合并、代码生成、迁移等各只有一处）登记在 [docs/ssot-registry.md](docs/ssot-registry.md)，不在这里再维护一份只会更快过时的清单。

## 开发规范

- **spec 是功能行为的唯一信源，代码是实现的唯一信源**。功能行为变更先改 spec。
- **接口契约只有一个来源**：`api/proto/`，经 `make gen` 派生服务端 handler 与前端类型。
- **权限码只有一个来源**：`api/permissions/catalog.yaml`，经 `make gen` 派生两端常量。
- **判定逻辑只有一处实现**：服务端 `internal/rbac`。前端与 CLI 的本地判断只做展示裁剪。
- **配置不是权限的来源**：主体、角色、权限码、作用域不得由配置文件或环境变量提供；凭证不进配置文件。
- 完整的开发约定见 [AGENTS.md](AGENTS.md)，代码位置索引见 [docs/ssot-registry.md](docs/ssot-registry.md)。

## 许可证

[Apache License 2.0](LICENSE)
