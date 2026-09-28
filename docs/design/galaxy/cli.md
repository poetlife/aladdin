# galaxy — 命令行接入

> 本文档是 galaxy **在命令行上的接入方式**的唯一信源。galaxy 本身的行为见 [README.md](README.md) 与各子模块文档；命令行的通用约定（凭证来源、退出码、危险操作）见 [../rbac/cli-permissions.md](../rbac/cli-permissions.md)。

## 背景与目标

galaxy 的每一次操作都是原子的：建一个工程、存一版、发布一版。这使它天然适合被编进脚本——发布一份页面本就不该要求人先打开浏览器。

命令行接入要回答的因此不是"怎么在终端里获得一个编辑器"，而是**"怎么把发布链路编进 CI 与本地脚本"**。

成功的衡量标准：

1. 只装了 aladdin 二进制的人，不打开浏览器就能把一份本地 HTML 发布出去，并拿到地址。
2. 命令行与网页端的能力**一一对等**：不出现"网页能做、命令行做不了"。
3. 权限声明、退出码、危险操作的确认行为与既有命令完全一致，脚本可以据此分支处理。
4. 命令行不引入任何第二处判定权威、不缓存判定结果、不新增枚举入口。

## 功能行为

### 命令即能力

| 命令 | 能力 | 权限码 |
|------|------|--------|
| `galaxy capabilities` | 读取本部署下创作能力的边界（能不能传素材、能不能发布、各项上限） | 只需认证 |
| `galaxy project list` | 列出自己的工程 | `galaxy.project.read` |
| `galaxy project create` / `update` / `delete` | 创建、改名与改简介、删除工程 | `galaxy.project.write` |
| `galaxy project get` | 读取工程元数据 | `galaxy.project.read` |
| `galaxy draft get` / `save` | 读取、保存当前草稿 | `galaxy.project.read` / `write` |
| `galaxy version save` / `delete` | 把草稿存成不可变版本、删除版本 | `galaxy.project.write` |
| `galaxy version list` / `get` | 列出、读取版本 | `galaxy.project.read` |
| `galaxy validate` | 校验一段正文能不能发布 | `galaxy.project.read` |
| `galaxy asset list` | 列出工程资产库（含短时读取地址） | `galaxy.asset.read` |
| `galaxy asset upload` / `delete` | 上传、删除资产 | `galaxy.asset.write` |
| `galaxy publish` / `unpublish` | 发布一个版本、撤回发布 | `galaxy.project.publish` |

工程、版本与资产的标识一律**显式给出**，都是命令的位置参数。

### 只有原子命令

命令行**不提供**"读文件、存草稿、存版本、发布一次完成"的便捷命令。

版本模型是用户可见的：一次发布发的是**某一版**，而不是"文件现在的样子"。把四步串成一条命令，会让"我这次发的是哪一版"变成一个只有读那条命令的实现才能回答的问题；中间某一步失败时，用户也无法从命令本身判断停在了哪里。

原子命令加 shell 的 `&&` 已经足够表达"一路走到底"，且每一步的失败都能被单独处理。

### 正文的输入与输出

**输入只有一个入口**：`--file <路径>`，其中 `-` 表示标准输入。`draft save` 与 `validate` 共用它，因此"校验通过的那份字节"与"存进草稿的那份字节"必然是同一份。文件读不到按**用法错误**处理（退出码与参数不合法同类）。

**输出**：

- `draft get` 与 `version get` 在默认（text）模式下**只输出正文**，可以直接重定向成文件；
- `--output json` 时输出完整的响应消息；
- 其余命令在 text 模式下输出人可读摘要，`--output json` 时输出完整消息。

字节数一律输出**原始数值**，不做人类可读换算：文件大小文案的唯一入口在网页端（见 [../../ssot-registry.md](../../ssot-registry.md)），在命令行另写一份就是第二个实现。

**`validate` 在正文有问题时以非零状态退出**（不新占一个退出码，落在未分类失败那一档）。理由是脚本：一个校验入口的意义就是让 `validate && publish` 这样的写法成立，"有问题"必须是一个能被 shell 看见的结论，而不是一段只给人读的文字。

### 危险操作

以下命令在交互式终端下二次确认，非交互式环境下**必须显式传入 `--yes`**（否则不执行）：

- `galaxy project delete`
- `galaxy version delete`
- `galaxy asset delete`
- `galaxy publish`

**发布进这个集合**的理由：它是唯一一个让内容离开私有边界的动作——发出去之后，拿到地址的任何人都能看，且地址可能被转发（见 [README.md](README.md) 的"发布即公开"）。它是本仓库里第一条把用户内容交给未认证陌生人的路径，值得一次显式确认。

**撤回发布不在这个集合里。** 它是发布的反向操作：一步就能让地址不可达，且不产生任何新的暴露。把"收回"也加上摩擦，只会让人在发错之后不敢撤。

### 资产上传：类型是声明的，摘要与服务端同源

上传走的是**与网页端同一条链路**（签发 → 直传 → 提交，见 [../objectstore/README.md](../objectstore/README.md)）：服务端分配资产标识并签发一份短时、只允许写、只对那个键有效的凭证；命令行用凭证把字节直接传给对象存储；随后提交，由服务端核对。

两处与网页端不同，都源于"终端里没有浏览器给的 MIME 信息"：

| 项 | 命令行 | 理由 |
|----|--------|------|
| 声明的类型 | `--content-type` 优先，缺省按文件扩展名推断 | 浏览器能提供 `File.type`，终端不能。推断不出来时**报用法错误**，不做静默缺省——静默缺省的表现是"我传的是 PNG，服务端记成了别的" |
| 内容摘要 | 由命令行的同一份摘要入口计算 | 浏览器与服务端共用同一个算法；命令行也必须只想一份实现，不得就地再写一遍 |

推断用的扩展名对照表**只是省事的缺省**：能不能作为资产，权威仍是服务端那一个白名单入口。因此表里的取值必须与服务端白名单逐条一致，并由测试钉住——白名单一旦变动，测试先失败，而不是等用户撞上"服务端拒绝了命令行自己猜的类型"。

字节会**一次读进内存**：摘要是对整份字节算的，而资产上限是 100 MiB 量级。

### 未配置时

未配置私有桶时上传与资产列表由服务端拒绝；未配置发布域时发布由服务端拒绝。两者都是"这个能力没开"，不是故障，命令行的提示要照此措辞。

命令行**不先查一次能力再决定发不发请求**：那会把一次调用变成两次，而且判定权威在服务端。`galaxy capabilities` 是给人看、给脚本读的，不是命令行自己的前置判断。

## 边界与约束

- **不提供"当前工程"这类本地状态。** 工程标识每次都显式给出。本地状态会让"这条命令打到了哪个工程"变成一个需要读本地文件才能回答的问题，而这个文件一旦过期，后果是改错了工程。
- **不提供枚举入口。** 只有 `project list`，且它的范围由凭证决定——与网页端同一个面，没有"列出所有工程"的形状。
- **请求里没有作用域字段，也没有拥有者字段。** 作用域来自凭证，归属由凭证决定；命令行不为它们造参数。
- **不缓存判定结果。** 本地的早退只有两处：凭证是否存在且未明显过期、参数形状是否合法。
- **不提供一次走完全流程的命令**（理由见上）。
- **不做字节数的可读格式化**（理由见上）。

## 可验证性与长程执行

**非长程任务。** 每条命令都是一次同步往返；发布可能较慢（要搬资产），但它的长程语义属于 [publication.md](publication.md)，与网页端共用同一条链路。

| 核查项 | 判据（验证手段） |
|--------|----------------|
| 命令覆盖 | 每个可执行命令声明了权限码，或标记为只需认证，或进入公开白名单（构建期静态检查，每次执行时运行） |
| 危险集合正确 | `project delete` / `version delete` / `asset delete` / `publish` 带危险标记；`unpublish` 不带（`cmd/aladdin` 测试） |
| 位置参数是用法错误 | 参数个数不对时退出码与其它用法错误同类，而不是落进"未分类失败"（`cmd/aladdin` 测试） |
| 扩展名表与白名单一致 | 表中每个取值都能通过服务端那一个类型入口（`cmd/aladdin` 测试） |
| 正文输入 | `--file`、`-`（stdin）、文件不存在三种情形各自的行为（`cmd/aladdin` 测试） |
| 发布地址匿名可达 | 用命令行子进程跑完建工程 → 存草稿 → 存版本 → 发布，再不携带任何凭证取发布地址（端到端测试） |

> **直传的 PUT 不在自动化覆盖内。** 测试装配里的对象存储是内存假实现，凭证指向真实的存储主机——没有真桶可写。"桶真的照做了策略"只能在部署后冒烟里验，这条边界与 [../objectstore/README.md](../objectstore/README.md) 写的是同一处。**不会为了测试给生产代码加一个"换地址"的开关**：那正是 [CLAUDE.md](../../../CLAUDE.md) 第 7 条禁止的那类开关。

## 依赖关系

| 依赖对象 | 交互方式 |
|---------|---------|
| 服务端 GalaxyService | 经 Connect 调用，消费其拒绝语义；命令与权限码的对应关系必须与 proto 注解一致 |
| RBAC | 权限码取自生成常量，命令只做静态声明，不做本地判定（见 [../rbac/cli-permissions.md](../rbac/cli-permissions.md)） |
| 对象存储直传 | 资产的字节走公共直传链路；命令行侧的实现见下 |
| proto | 方法与权限码在 `api/proto/aladdin/galaxy/v1/` 中声明，经 `buf generate` 派生客户端 |
| 命令行配置与凭证 | 目标地址与凭证经统一配置入口读取（见 [../config/cli-config.md](../config/cli-config.md)） |

## 代码实现索引

| 职责 | 文件路径 |
|------|---------|
| galaxy 命令组与能力下发 | [cmd/aladdin/command-galaxy.go](../../../cmd/aladdin/command-galaxy.go) |
| 工程与草稿 | [cmd/aladdin/command-galaxy-project.go](../../../cmd/aladdin/command-galaxy-project.go) |
| 版本与校验 | [cmd/aladdin/command-galaxy-version.go](../../../cmd/aladdin/command-galaxy-version.go) |
| 资产与上传编排 | [cmd/aladdin/command-galaxy-asset.go](../../../cmd/aladdin/command-galaxy-asset.go) |
| 发布与撤回 | [cmd/aladdin/command-galaxy-publish.go](../../../cmd/aladdin/command-galaxy-publish.go) |
| 正文的读取（唯一入口） | [cmd/aladdin/galaxy-content.go](../../../cmd/aladdin/galaxy-content.go) |
| 命令行侧直传（唯一实现） | [cmd/aladdin/direct-upload.go](../../../cmd/aladdin/direct-upload.go) |
| 命令的权限声明与静态检查 | [cmd/aladdin/permission-decl.go](../../../cmd/aladdin/permission-decl.go) |

---

> spec 是功能行为的唯一信源，代码是实现的唯一信源。
> spec 中不得描述代码细节（函数签名、内部数据结构、算法步骤等）。
> 功能行为变更 → 先改 spec；实现细节变更 → 只改代码。
