# 部署

> 本文是 aladdin **上生产**的功能行为与操作步骤的唯一信源。产物怎么造出来由 [release.md](release.md) 定义，本文只从"产物已经存在于 Release 里"开始。

## 背景与目标

aladdin 的服务端是一个只讲 RPC 的进程：它不托管静态文件、不终止 TLS、不对外监听。前端是另一份产物，需要有人把它服务出去。因此一次部署要回答的是：**把这两样东西放到一台服务器上，用什么东西把它们接到公网上。**

成功与否只看三件事：

1. 一次发布是**一条命令**，失败时自动回到发布前的状态
2. 发布前不需要在生产机上编译任何东西
3. 部署机上有哪些文件、数据存在哪、证书由谁签，都能从一条命令确定，不需要读代码

### 非目标

- **不做多副本**。数据库是 sqlite，定位就是单机（见 [design/persistence/README.md](design/persistence/README.md)）。
- **不做容器编排**。产物是静态链接的二进制，systemd 足够。
- **不接管宿主的既有服务**。一台机器上除了 aladdin 还跑着什么、443 有没有被别人占用，是那台宿主自己的事，本文只说明 aladdin 需要什么（下一节），如何满足由部署者决定。

## aladdin 对宿主的要求

只有四条，除此之外它不关心宿主机上还有什么：

| 要求 | 为什么 |
|------|--------|
| 一个在 **443 上终止 TLS** 的反向代理 | 前端与 RPC 必须**同源**：前端传输层的 `baseUrl` 是空串（[web/src/main.tsx](../web/src/main.tsx)），服务端也不返回 CORS 头。拆成两个域名会让浏览器开始发跨域请求，表现为"页面能打开但接口全挂" |
| 把 `https://<域名>/` 指到一个**静态目录** | 前端是单独打包的静态产物，服务端不内嵌它（Go 侧没有 `go:embed`，见 [release.md](release.md)） |
| 把 `https://<域名>/aladdin.*` 转发到 **127.0.0.1:9090** | Connect 的 handler 挂在过程名本身，没有 `/api` 前缀 |
| 允许服务端**只监听回环** | 它不需要对公网监听，反代在同一台机器上 |

**443 已经被别的 TLS 服务占用时怎么办，本文不回答。** 那是那台宿主的既有约束——按 SNI 分流、换端口、还是别的方式，取决于具体是什么服务、能不能碰。为某一种共存方式提供模板，等于替所有部署者做了一次他们没做过的选择。

## 部署形态

```
   浏览器
     │ https://<域名>
     ▼
   nginx :443（终止 TLS，证书由 certbot 维护）
     ├─ location /          → /opt/aladdin/web（静态前端）
     ├─ location /aladdin.  → 127.0.0.1:9090
     └─ location /grpc.health.v1.Health/ → 127.0.0.1:9090
                                   │
                                   ▼
                        aladdin-server（systemd，仅回环 :9090）
                          /opt/aladdin/config.yml
                          /opt/aladdin/data/aladdin.db（sqlite）

   CLI ── ssh -L 9090:127.0.0.1:9090 <主机别名> ──▶ 同一个回环端口
```

nginx 在这里只做三件事：终止 TLS、服务静态文件、把两类前缀转发给服务端。它不做鉴权——判定只有一处实现，在服务端（见 [design/rbac/README.md](design/rbac/README.md)）。

### 端口

服务端用内置默认端口 **9090**，仅监听回环。端口出现在三处，改一处必须同时改另外两处：

| 位置 | 谁读它 |
|------|--------|
| `/opt/aladdin/config.yml` 的 `address` | 服务端监听 |
| `deploy/nginx-aladdin-site.conf` 的 `proxy_pass` | 反向代理的目标 |
| `deploy/deploy.sh` 的 `PORT` | 健康检查的目标 |

### 为什么健康检查端点要单独代理

服务端的健康检查挂在 `/grpc.health.v1.Health/`，**不在 `/aladdin.` 前缀下**。不显式代理它就会被 `location /` 的 SPA 兜底吃掉——POST 到一个静态文件会被 nginx 拒成 405。

后果不是报错，而是**静默的**：页面一切正常，只是从外部再也看不出后端到底活没活，监控失去意义。

### 前端是 history 路由

前端用 `createBrowserRouter`（[web/src/router.tsx](../web/src/router.tsx)），直接访问 `/roles` 这类路径必须回落到 `index.html`。少了 `try_files ... /index.html` 这一行，刷新页面就是 404。

本地开发环境复现不出这个问题：Vite 的 dev server 自带回落。**因此这一条只能在部署形态上保证，测试覆盖不到。**

## 一次性前置

以下步骤只做一次。

### 1. 域名解析

给站点域名加一条指向该主机公网 IP 的 A 记录，**必须是灰云（仅解析）**：橙云会把 TLS 终止在 Cloudflare、再以普通 HTTPS 回源，证书的签发与续期都会因此变得不可预期。

> 换域名时要同时改：`deploy/nginx-aladdin-site.conf` 的占位符、`deploy.sh` 的 `SITE_DOMAIN`、DNS、certbot 的证书名，以及本文里的 `<域名>`。

### 2. 装 nginx 与 certbot

```bash
sudo apt-get install -y -o Dpkg::Options::=--force-confold nginx certbot
```

### 3. 放站点配置（先替换占位符）

`deploy/nginx-aladdin-site.conf` 是**模板**，含占位符 `__SITE_DOMAIN__`，装上去之前要换成真实域名：

```bash
sudo mkdir -p /var/www/certbot
sudo sed 's/__SITE_DOMAIN__/<域名>/g' deploy/nginx-aladdin-site.conf \
  | sudo tee /etc/nginx/sites-available/<域名> >/dev/null
sudo ln -sf /etc/nginx/sites-available/<域名> /etc/nginx/sites-enabled/
```

未替换就装上去，nginx 会因为 `server_name` 不合法而**直接起不来**——这比"看起来装好了却谁都不匹配"早暴露得多。

### 4. 申请证书

```bash
sudo certbot certonly --webroot -w /var/www/certbot \
  -d <域名> \
  --non-interactive --agree-tos --email <你的邮箱> \
  --cert-name <域名>
sudo nginx -t && sudo systemctl reload nginx
```

证书落在 `/etc/letsencrypt/live/<域名>/`，`certbot.timer` 会自动续期——**续期依赖站点配置里那个放行 `/.well-known/acme-challenge/` 的 location，不要删。** 第 3 步配置里的证书路径要与此处的 `--cert-name` 一致。

### 5. 建系统用户与目录

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin aladdin
sudo mkdir -p /opt/aladdin/bin /opt/aladdin/data /opt/aladdin/web /opt/aladdin/backup
sudo chown -R aladdin:aladdin /opt/aladdin
```

用专用系统用户而不是让服务跑在登录用户下：一台机器上通常不止这一个服务，一个进程的身份泄漏不该顺带把它人的东西也交出去。

### 6. 写配置

把 [config.server.example.yml](../config.server.example.yml) 复制成 `/opt/aladdin/config.yml`，改这一项：

```yaml
database_dsn: /opt/aladdin/data/aladdin.db
```

`address` 保持默认的 `127.0.0.1:9090`。用**绝对路径**是因为相对路径按启动时的工作目录解析，而工作目录由部署方式决定（见 [design/config/server-config.md](design/config/server-config.md)）。用绝对路径后，"数据写到哪去了"不需要再推理。

> `/opt/aladdin/data` 必须已存在：服务端**不自动建目录**，这是刻意的——免得路径拼错时在一个奇怪的地方建出一个空库，看起来像数据全丢了。

### 7. 装部署文件与 systemd 单元

在**本机**的仓库里执行：

```bash
scp deploy/deploy.sh deploy/aladdin-server.service <主机别名>:/tmp/
```

在**服务器**上执行：

```bash
sudo install -m 0755 /tmp/deploy.sh /opt/aladdin/deploy.sh
sudo install -m 0644 /tmp/aladdin-server.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable aladdin-server
```

`deploy.sh` 是仓库里那份的**副本**，不是 git 检出——**装上之前先把顶部的 `SITE_DOMAIN` 占位符换成真实域名**，否则它会去一个不存在的路径找 nginx 站点并中止。代价是它不会自己更新：**改了 `deploy/` 下的文件要重新 scp 一次**。

单元文件里**没有** `ALADDIN_DEV_SEED` 这类环境变量，也不应该有：那是绕过真实认证的开发旁路，且它写进去的主体与绑定会**落库**，一次误开在库里留下的是长期存在的真实数据，取消环境变量并不会清除它们（见 [design/config/server-config.md](design/config/server-config.md)）。

### 8. 发布第一个版本

仓库里还没有任何 tag 时，Release 是不存在的。先按 [release.md](release.md) 打一个 `vX.Y.Z` 的 tag 推上去，等流水线产出 Release，然后：

```bash
sudo /opt/aladdin/deploy.sh
```

### 9. 首次引导管理员

系统里还没有任何角色绑定时，需要建立第一个管理员。**机器凭证在生产不可用**——它只能由开发种子旁路产生，而生产不开那个旁路。因此第一个管理员必须走真实登录：

1. 在 `/opt/aladdin/config.yml` 填入 `google_client_id`
2. **到 Google 控制台把 `https://<域名>` 加进允许的浏览器来源**
3. 重启服务，用浏览器登录一次
4. 从服务端日志里读出这次登录的**主体标识**
   ```bash
   sudo journalctl -u aladdin-server | grep -i 登录
   ```
5. 把该标识填进 `bootstrap_admin_subject`，并按需填 `bootstrap_admin_scope`，重启
6. 确认日志里出现 `warn` 级的引导留痕，然后**删掉这两行**

第 4 步填的必须是**主体标识**，不是邮箱，也不是 Google 的 `sub`：邮箱可以被改名、被回收，回收给另一个人的那天就是一次无痕提权（见 [CLAUDE.md](../CLAUDE.md) 第 7 条）。第 6 步删掉配置**不会**撤销已经建立的绑定——判定路径只读数据库、从不读配置。

> **第 2 步是本部署里唯一一处不在本仓库管理的东西。** 浏览器来源白名单只存在于 Google 控制台，仓库里刻意没有对应的配置键。换域名时改的是那里，**忘记改的表现是"换了域名之后登录按钮点了没反应"**，而不是任何一条报错。

## 日常发布

```bash
git tag v0.2.0 && git push origin v0.2.0      # 本机：触发构建
ssh <主机别名>                                 # 登录服务器
sudo /opt/aladdin/deploy.sh v0.2.0             # 省略版本号则取最新 Release
```

`deploy.sh` 依次做：拉取产物 → 校验和 → 备份当前二进制与前端 → 原子替换 → 重启 → 健康检查（最多 15 秒）→ 失败则回滚。

注意发布**不碰反向代理**：只有二进制与静态文件在变。改了 `deploy/` 下的配置才需要手工同步，那是前置步骤而不是发布步骤。

**为什么是服务器拉而不是 CI 推。** 推的模式需要把一把能登录生产的 SSH 私钥放进 GitHub secrets。单实例下，"发完版在服务器上跑一条命令"的成本可以接受，而少一把能登录生产的钥匙是实打实的收窄。

**校验和失败时不做任何改动**：`SHA256SUMS` 不匹配意味着下载不完整或产物被替换过，此时进程还在跑旧版本，是比"先换了再说"更好的状态。

## 回滚

### 自动回滚

健康检查连续 15 秒失败时，`deploy.sh` 会还原上一个二进制与前端并重启，并打印最近 40 行服务日志。

### 迁移让回滚不总是成立

持久化模块约定**库里存在代码中不存在的版本时拒绝启动**（见 [design/persistence/README.md](design/persistence/README.md)）。所以当这次发布带了新的数据库迁移，把二进制换回旧版本会**直接拒绝启动**，日志里是"未知版本"。

这是刻意的：带着一个比代码更新的库继续服务，比停机更危险。因此回滚前先确认本次发布是否包含新迁移（看 `internal/database/migrate/` 的改动）；包含时，回滚必须**连着库快照一起回**。

### 手工回滚

```bash
sudo install -o aladdin -g aladdin -m 0755 /opt/aladdin/aladdin-server.prev /opt/aladdin/bin/aladdin-server
sudo systemctl restart aladdin-server
```

上一版二进制留在 `/opt/aladdin/aladdin-server.prev`，上一版前端留在 `/opt/aladdin/web.prev.tar.gz`。

## CLI 怎么连生产

CLI 的 gRPC 客户端目前写死了明文连接（[pkg/client/client.go](../pkg/client/client.go) 用 `insecure.NewCredentials()`），**连不上 HTTPS 端点**。因此走 SSH 隧道：

```bash
ssh -L 9090:127.0.0.1:9090 <主机别名>
```

CLI 保持默认配置即可（默认地址就是 `127.0.0.1:9090`）。隧道与反向代理走的是同一个服务端入口，全程加密，不需要在防火墙上开任何端口。

> **不要把 9090 直接对公网开放**。CLI 的凭证是 `Authorization: Bearer`，明文端口等于让凭证以明文过境。
>
> 想"装了 CLI 就能直连生产"，需要给客户端加 TLS 支持（配置项 + `credentials.NewTLS`），那是一次 spec + 代码的变更，不是部署参数。

## 备份

sqlite 数据库**开了预写日志（WAL）**。因此直接 `cp` 数据库文件是**不安全**的：最近的写入还在 `-wal` 文件里。这一点在线上很容易观察到——`aladdin.db` 可能只有几 KB，而 `aladdin.db-wal` 有几十上百 KB。

用 sqlite 自己的在线备份：

```bash
sudo apt-get install -y sqlite3
sudo -u aladdin sqlite3 /opt/aladdin/data/aladdin.db \
  "VACUUM INTO '/opt/aladdin/backup/aladdin-$(date +%F).db'"
```

`VACUUM INTO` 在库正常服务时读出一份**一致且紧凑**的快照，不需要停服务。建议挂成定时任务并保留最近 7 份。

**本机快照不算异地备份**：机器没了的时候，备份和它在一起。异地那一层用对象存储或云厂商的服务器快照。

> 数据备份与恢复**不被持久化模块承诺**（[design/persistence/README.md](design/persistence/README.md) 明确把它划归部署形态）。这段就是那一层，它没有代码兜底。

## 排障

| 症状 | 先看哪里 |
|------|---------|
| 页面能打开，但接口全挂 | 前端是否与 RPC 同源；`/aladdin.` 的 `proxy_pass` 是否指向 9090 |
| 站点 502 | 服务端没起来：`systemctl status aladdin-server`、`ss -lntp \| grep 9090` |
| 刷新 `/roles` 得到 404 | 站点配置里缺 `try_files $uri $uri/ /index.html` |
| 健康检查端点返回 405 | 站点配置里缺 `/grpc.health.v1.Health/` 那条 location，被 SPA 兜底吃掉了 |
| 静态资源 404 但文件确实在 | 站点配置的 `root` 是否指向 `/opt/aladdin/web`；文件权限是否允许 nginx 用户读取 |
| 浏览器报证书错误 | 证书名与站点 `server_name` 是否一致；`certbot renew --dry-run` 是否通过 |
| 部署后健康检查失败并自动回滚 | `deploy.sh` 打印的服务端日志；若含"未知版本"，是迁移与回滚的冲突，见上文"回滚" |
| 服务端起不来且日志说端口被占 | 9090 被同机别的服务占了，换端口要同时改三处（见"端口"） |
| 换了域名后登录按钮点了没反应 | Google 控制台的浏览器来源白名单没改 |
| 管理员登录后仍然"没有权限" | 引导是否生效：`sudo journalctl -u aladdin-server \| grep -i 引导`；引导只在存储中无任何绑定时生效 |
| 数据"看起来全丢了" | 验证真实的数据路径：`sudo journalctl -u aladdin-server \| grep -i 数据库`——启动日志有脱敏后的定位信息 |
| 服务被 OOM 杀掉后自动重启 | `journalctl -u aladdin-server \| grep -i memory`；单元里的 `MemoryMax` 是保险丝，不是估算 |
| 改了 nginx 配置没生效 | 需要 `sudo nginx -t && sudo systemctl reload nginx`；反过来，**只换静态产物不需要 reload** |

## 依赖关系

| 依赖对象 | 交互方式 |
|---------|---------|
| 发布产物 | 消费 Release 里的 `linux_amd64` 二进制包与前端包，不自行编译（见 [release.md](release.md)） |
| 服务端配置 | `deploy.sh` 保证 `/opt/aladdin/config.yml` 存在后才发布 |
| 持久化 | 备份策略消费 sqlite 的 WAL 语义 |
| 身份认证 | 首次引导依赖一次真实登录产出的主体标识（见 [design/identity/google-login.md](design/identity/google-login.md)） |
| 反向代理 | 提供 TLS 终止、静态托管与到 9090 的转发；具体用什么、443 上还有没有别人，由宿主决定 |
| 可观测性 | 日志走 journald；`otel_endpoint` 留空表示不上报，链路标识照常生成与传播 |

## 可验证性与长程执行

**属于长程任务。** 部署是一次性动作，但"生产机上的状态与仓库里这几份文件一致"需要长期成立。

| 核查项 | 判据（验证手段） |
|--------|----------------|
| 部署一条命令 | 更新版本只需 `deploy.sh <tag>`，无需在生产机编译或手工改配置 |
| 失败可回滚 | 健康检查失败时二进制与前端被还原（`deploy.sh` 的 rollback 路径） |
| 校验和不匹配不落盘 | 篡改产物后 `deploy.sh` 中止且不改动任何已部署文件 |
| 端口三处一致 | `config.yml`、站点配置、`deploy.sh` 三处的 9090 相同（人工核对项） |
| 前端 SPA 可深链 | 直接访问 `/roles` 返回页面而非 404（部署后冒烟） |
| 健康检查从外部可达 | `curl -X POST https://<域名>/grpc.health.v1.Health/Check` 返回 SERVING |
| 只监听回环 | `ss -lnt \| grep 9090` 显示 `127.0.0.1:9090` 而非 `0.0.0.0:9090` |
| 证书可续期 | `certbot renew --dry-run` 通过；`:80` 的 ACME location 仍在 |
| 生产无认证旁路 | 生产机上 `ALADDIN_DEV_SEED` 未出现在 systemd 单元与环境中 |
| 备份可用 | `VACUUM INTO` 产出的快照能被一个新进程打开并读到既有数据 |
| 仓库不含实例值 | `git log --all -p \| grep -iE "真实域名\|主机别名\|公网 IP"` 无命中（长期项：**每次提交前**都要成立） |

---

> 本文是部署形态的唯一信源。`deploy/` 下的三份文件是实现，实现细节变更只改文件；**拓扑、端口、TLS 终止位置、回滚语义、备份策略**的变更必须先改本文。
