aladdin 的命令行客户端。装在你自己机器上，用已有的账号登录，做权限与角色的日常操作。

判定发生在服务端：本工具不缓存、也不复用任何判定结果，因此服务端的角色改动无需在客户端做任何事。有哪些子命令见 `aladdin --help`。

## 安装

只有**发布产物**可以安装：升级只认发布产物，本机构建的二进制（`make build`、`go install`）日后无法自更新。

**macOS（Apple Silicon）**

```bash
tag=$(curl -fsSL -o /dev/null -w '%{url_effective}' \
  https://github.com/poetlife/aladdin/releases/latest)
tag=${tag##*/}

# 产物地址与产物名各拼一次，下面只用这两个变量
base="https://github.com/poetlife/aladdin/releases/download/$tag"
asset="aladdin_${tag}_darwin_arm64.tar.gz"

curl -LO "$base/$asset"
curl -LO "$base/SHA256SUMS"
grep "$asset" SHA256SUMS | shasum -a 256 -c -
tar xzf "$asset"

# 装进用户可写目录，这样升级不需要 sudo
mkdir -p ~/.local/bin
install -m 0755 aladdin ~/.local/bin/aladdin
```

**Linux（x86-64）**

```bash
tag=$(curl -fsSL -o /dev/null -w '%{url_effective}' \
  https://github.com/poetlife/aladdin/releases/latest)
tag=${tag##*/}

# 产物地址与产物名各拼一次，下面只用这两个变量
base="https://github.com/poetlife/aladdin/releases/download/$tag"
asset="aladdin_${tag}_linux_amd64.tar.gz"

curl -LO "$base/$asset"
curl -LO "$base/SHA256SUMS"
grep "$asset" SHA256SUMS | sha256sum -c -
tar xzf "$asset"

# 装进用户可写目录，这样升级不需要 sudo
mkdir -p ~/.local/bin
install -m 0755 aladdin ~/.local/bin/aladdin
```

命令里的 `tag` 是**运行期解析出来的**，没有一处写死版本号。产物名里带着 tag，写死一条下载命令就等于写死了当时的版本——下一个 tag 之后那条命令指向一个不存在的产物，而这里不会报任何错。

装进 `~/.local/bin`——它归你所有，所以后面的 `aladdin update` 不需要 sudo；装到 `/usr/local/bin` 这类系统目录则要先过 sudo，普通用户还会直接 Permission denied。这个目录不在 `PATH` 里时，把 `export PATH="$HOME/.local/bin:$PATH"` 加进 shell 配置并重开一个终端。装完用 `aladdin version` 确认。

## 登录

```bash
aladdin login
```

终端打印一个短码，在一个**已经登录**的浏览器里打开服务端给出的批准页，输入短码即完成。短码要由人比对，所以它不进地址、也不进访问日志。

```bash
aladdin login --token <凭证>
```

机器凭证那条路径：适合脚本与 CI，不走浏览器。凭证按「参数 > 环境变量 > 凭证文件」解析，保存在本机。

## 升级

```bash
aladdin update --check   # 只报告有没有新版本
aladdin update           # 升级到最新发布版本
```

升级换的是本机那一个文件，**当前这个进程仍是旧版本**——运行的影像是已经打开的那个文件。本机构建出来的二进制会被拒绝，理由它会说清楚。

## 服务端地址

**发布版已经带着官方服务地址**，装完直接登录即可，不用先配。从源码构建的二进制默认连本机（`127.0.0.1:9090`）。要换目标用 `--address` 或 `ALADDIN_ADDRESS`，也可以写进配置文件；**非本机地址一律走 TLS**。
