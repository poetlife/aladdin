# aladdin

阿拉丁神灯 —— 前端、服务端与命令行一体的权限管理平台。

## 简介

三端共享同一套 RBAC 权限体系：

- **服务端**（Go + Connect）：权限模型与决策引擎的唯一实现处，是唯一的安全边界。一个端口同时讲 Connect / gRPC / gRPC-Web
- **命令行**（Go + cobra）：走原生 gRPC 的客户端，与服务端共享同一份业务实现
- **前端**（React + antd）：走 Connect，按服务端下发的权限码集合做展示裁剪

三端之间的接口契约由 `api/proto/` 单一来源派生：服务端 handler、前端类型与 service descriptor 都出自同一次 `buf generate`。

设计文档见 [docs/design/rbac/](docs/design/rbac/README.md)（权限体系）与 [docs/design/config/](docs/design/config/README.md)（配置与凭证）。

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
# 以开发种子数据启动服务端（本地联调用，勿用于生产）
make dev           # RPC 监听 127.0.0.1:9090，同时服务 Connect / gRPC / gRPC-Web

# 另开一个终端：用种子凭证登录
export ALADDIN_ADDRESS=127.0.0.1:9090
export ALADDIN_TOKEN=dev-token
export ALADDIN_SCOPE=tenant/acme

aladdin whoami
aladdin permissions
aladdin role list

# 启动前端
make web-dev      # http://localhost:5173
```

## 配置与凭证

服务端与 CLI **各自**有一份配置文件，不共用：`address` 在服务端是**监听**地址、在 CLI 是**目标**地址，共用一份会让配置在两端之间传递时静默连错地方。

| 端 | 配置文件默认位置 | 示例 |
|----|----------------|------|
| 服务端 | 启动时工作目录下的 `config.yml` | [config.server.example.yml](config.server.example.yml) |
| CLI | 用户配置目录下的 `aladdin/config.yml` | [config.cli.example.yml](config.cli.example.yml) |

取值按 **内置默认值 < `config.yml` < `config.local.yml` < 环境变量 < 命令行参数** 合并，每层只覆盖它显式写出的键。也可以用 `-config` / `--config` 或 `ALADDIN_CONFIG` 指向别的路径；个人本机改动写进 `config.local.yml`（已忽略，不提交）。

凭证**不放进配置文件**，而是单独存放（用户配置目录下的 `aladdin/credentials.json`，权限 0600，由 `aladdin login` 写入）：配置可以提交、可以共享、可以进镜像，凭证不可以。

可观测性上报也走配置文件（`otel_endpoint` / `otel_insecure` / `otel_sample_ratio`，两端同名）。**留空只是不上报**：链路标识照常生成、经 W3C `traceparent` 传播、并在响应头回写，因此没有 Collector 的环境一样能用追踪。完整规则见 [docs/design/config/](docs/design/config/README.md) 与 [docs/observability.md](docs/observability.md)。

## 项目结构

```
aladdin/
├── config.server.example.yml     # 服务端配置示例（键集合由测试守着，不会与实现漂移）
├── config.cli.example.yml        # CLI 配置示例
├── api/
│   ├── proto/                    # 接口契约（唯一信源，经 buf generate 派生 Go 与 TS 代码）
│   ├── permissions/catalog.yaml  # 权限码全集（唯一信源，派生 Go 与 TS 常量）
│   └── gen/                      # 生成的 Go 代码，不手工修改
├── cmd/
│   ├── aladdin-server/           # gRPC 服务端入口
│   └── aladdin/                  # cobra CLI 入口
├── internal/
│   ├── rbac/                     # 权限模型与决策引擎（服务端与 CLI 共用）
│   ├── server/                   # Connect 服务装配与服务实现
│   │   ├── middleware.go         # 链路标识与认证（HTTP 层）
│   │   └── interceptor/          # 鉴权拦截器（协议无关）
│   ├── auth/                     # CLI 侧凭证解析与持久化
│   ├── config/                   # 配置来源与合并（唯一入口）
│   │   ├── config.go             # 两端类型、默认值、校验、环境变量名
│   │   ├── file.go               # 配置文件定位与解析（键集合、未知键报错）
│   │   └── load.go               # 五层来源合并与两端加载入口
│   ├── observability/            # 日志与链路标识（唯一入口）
│   └── tools/permissiongen/      # 权限目录代码生成器
├── pkg/client/                   # 对外可复用的 gRPC 客户端
├── test/e2e/                     # 端到端测试
├── web/                          # React + antd 前端
│   └── src/
│       ├── api/                  # 传输层（唯一出口）、各服务调用、错误解码、链路标识
│       ├── auth/                 # 会话、权限码集合、路由与控件裁剪
│       ├── gen/                  # 生成的 TS 常量与 proto 类型，不手工修改
│       ├── layouts/  pages/      # 界面
│       └── router.tsx            # 路由表（基础权限的唯一声明处）
└── docs/
    ├── design/                   # 功能设计文档（spec）
    ├── debugging/                # 排障记录与索引
    ├── observability.md          # 可观测性规范
    ├── testing.md                # 测试指南
    └── ssot-registry.md          # 同一件事唯一入口的登记表
```

## 开发规范

- **spec 是功能行为的唯一信源，代码是实现的唯一信源**。功能行为变更先改 spec。
- **接口契约只有一个来源**：`api/proto/`，经 `make gen` 派生服务端 handler 与前端类型。
- **权限码只有一个来源**：`api/permissions/catalog.yaml`，经 `make gen` 派生两端常量。
- **判定逻辑只有一处实现**：服务端 `internal/rbac`。前端与 CLI 的本地判断只做展示裁剪。
- **配置不是权限的来源**：主体、角色、权限码、作用域不得由配置文件或环境变量提供；凭证不进配置文件。
- 完整的开发约定见 [CLAUDE.md](CLAUDE.md)，代码位置索引见 [docs/ssot-registry.md](docs/ssot-registry.md)。

## 许可证

[Apache License 2.0](LICENSE)
