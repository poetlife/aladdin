# 命令行 模块总览

> 本目录是 aladdin 命令行**安装与升级形态**的唯一信源。命令行读什么配置、凭证存在哪、权限怎么声明，分别见 [../config/cli-config.md](../config/cli-config.md)、[../config/credentials.md](../config/credentials.md) 与 [../rbac/cli-permissions.md](../rbac/cli-permissions.md)。

## 背景与目标

三端里只有命令行被装在**别人自己的机器**上：服务端与前端由部署者更新，命令行却散在各处，没有人能替使用者把二进制换掉。它因此是唯一需要一条升级路径的一端。

本模块只管**二进制怎么到本机、怎么换成本机的那一份**。命令怎么用、能做什么，不属于本模块。

成功的衡量标准：

1. 一条命令就能把本机二进制升到最新发布版本，不需要人工下载与解包。
2. 升级只接受校验通过的产物——校验不通过的字节永远不会落到可执行的位置上。
3. 升级失败时本机二进制**一个字节都没变**，不存在"装了一半"的状态。
4. 不是由发布产物安装的二进制（本机构建、`go install`）被明确拒绝，而不是把工作副本悄悄换掉。

## 安装

见 [install.md](install.md)。

## 升级

见 [self-update.md](self-update.md)。

三条边界：

- **升级源不可配置。** 它固定指向本仓库的发布。一个"从哪升级"的配置项，等于给了一个把任意二进制装进使用者机器的开关——与 [AGENTS.md](../../../AGENTS.md) 第 7 条禁止的"信任该来源"开关是同一类东西。
- **默认连哪由构建固定，不是用户开关。** 发布产物里编译着官方服务地址（源码构建仍默认连本机），`--address` 是唯一面向用户的覆盖点，且非回环地址一律 TLS、没有关掉它的开关。这与上一条同源——能决定"连到哪"的开关与能决定"装什么"的开关同属信任边界，见 [../config/cli-config.md](../config/cli-config.md)。
- **不自作主张。** 只有显式执行升级命令时才访问发布源；其它命令保持纯本地，不发任何出站请求。命令行是别人脚本里的一个环节，它不该在无关的调用里顺带联网。

## 相关

- 发布产物如何产生、命名与保留多少版本：[../../release.md](../../release.md)
- 命令行的配置分层：[../config/cli-config.md](../config/cli-config.md)
- 命令行的凭证存放与保护：[../config/credentials.md](../config/credentials.md)
- 命令行的权限声明与退出码：[../rbac/cli-permissions.md](../rbac/cli-permissions.md)
- 命令行的登录方式：[../identity/device-login.md](../identity/device-login.md)
