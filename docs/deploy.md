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
| 把 `https://<域名>/aladdin.*` 转发到 **127.0.0.1:9090** | Connect 的 handler 挂在过程名本身，没有 `/api` 前缀。浏览器与**命令行都走 Connect**，因此按 HTTP/1.1 转发上游即可——不要改成 `grpc_pass`，理由见下面"命令行登录"与 [debugging registry](debugging/registry.md) |
| 允许服务端**只监听回环** | 它不需要对公网监听，反代在同一台机器上 |

**443 已经被别的 TLS 服务占用时怎么办，本文不回答。** 那是那台宿主的既有约束——按 SNI 分流、换端口、还是别的方式，取决于具体是什么服务、能不能碰。为某一种共存方式提供模板，等于替所有部署者做了一次他们没做过的选择。

## 部署形态

```
   浏览器
     │ https://<域名>
     ▼
   nginx :443（终止 TLS，证书由 certbot 维护）
     ├─ location /          → /opt/aladdin/web（静态前端）
     ├─ location /cli/latest/ → /opt/aladdin/cli/latest（CLI 自更新镜像，只留最新）
     ├─ location /aladdin.  → 127.0.0.1:9090（Connect：浏览器与命令行共用）
     └─ location /grpc.health.v1.Health/ → 127.0.0.1:9090
                                   │
                                   ▼
                        aladdin-server（systemd，仅回环 :9090）
                          /opt/aladdin/config.yml
                          /opt/aladdin/secrets.env（0600，可选）
                          /opt/aladdin/data/aladdin.db（sqlite）
                                   │
                                   └─▶ COS 桶（默认私有读写：头像 + 工程资产私有区；发布物的公开区靠逐对象公开读；可选）
   浏览器 ──〈用临时凭证直传写入〉──▶ 桶（跨域写，需要桶侧 CORS）
   浏览器 ──〈用预签名地址读取私有对象〉──▶ 桶
   浏览器 ──〈发布页面的图片/视频/音频直连，公开读对象无需签名〉──▶ 桶

   浏览器 ── https://<发布域>/g/<工程标识> ──▶ 同一个回环端口（公开，不校验凭证）

   CLI ── Connect over TLS ──▶ 上面那个 nginx :443（发布产物默认走这条）
   CLI ── 自更新兜底 ──▶ https://<域名>/cli/latest/（发布源不可用时才走，只读静态文件）

   源码构建的 CLI ── ssh -L 9090:127.0.0.1:9090 <主机别名> ──▶ 同一个回环端口
```

> **发布域与主域是不同的域**，指向同一个 nginx。这不是可选的整洁问题：发布物里跑着用户写的脚本，同源（乃至同注册域）意味着它能读写应用的 cookie 与本地存储。服务端在启动时校验这一点，同源或同注册域一律拒绝启动。

nginx 在这里只做三件事：终止 TLS、服务静态文件、把两类前缀转发给服务端。它不做鉴权——判定只有一处实现，在服务端（见 [design/rbac/README.md](design/rbac/README.md)）。

### 端口

服务端用内置默认端口 **9090**，仅监听回环。端口出现在三处，改一处必须同时改另外两处：

| 位置 | 谁读它 |
|------|--------|
| `/opt/aladdin/config.yml` 的 `address` | 服务端监听 |
| `deploy/nginx-aladdin-site.conf` 的 `proxy_pass` | 反向代理的目标 |
| `deploy/deploy.sh` 的 `PORT` | 健康检查的目标 |

> CLI 的**默认目标地址不是这三个 9090 中的任何一个**：发布产物里注入的是公网站点（`<域名>:443`），见"CLI 怎么连生产"。明文连接只允许出现在回环上。

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

> **已有的站点要手工补一条 location。** 模板里新增了 `/cli/latest/`（命令行自更新的兜底镜像，见 [design/cli/self-update.md](design/cli/self-update.md)）。已经装好的站点是按当时的模板装的，**也可能是宿主机上的自有配置**（例如 443 经 SNI 分流到 4443 的形态）——两者都不会自己更新。把那条 location 加进去，再 `sudo nginx -t && sudo systemctl reload nginx`。
>
> 漏了它的表现不显眼：主站镜像整体 404，于是客户端在发布源不可用时**兜底失败**（错误信息会同时交代两路），而不是任何一条指向配置的报错。镜像的目录由 `deploy.sh` 自己创建，不必预先建。

模板里的 `/aladdin.` 与 `/grpc.health.v1.Health/` **按 HTTP/1.1 转发上游即可**：浏览器与命令行都走 Connect，它把错误放在 HTTP 状态与响应体里。

> **不要把这两条改成 `grpc_pass`。** nginx 转发 gRPC 时会丢掉空正文响应的 trailers——也就是**所有错误响应**，现象是调用没成功却只拿到一个空的 Unknown。命令行因此不走原生 gRPC，依据见 [debugging registry](debugging/registry.md)。

`/aladdin.` 那一条还带两个与**服务端推送**有关的参数，模板里已写好（见 [design/events/README.md](design/events/README.md)）：`proxy_buffering off`，以及默认的 `proxy_read_timeout`（60 秒）要大于上游心跳周期（25 秒）。缺前者时事件被攒在 nginx 里、晚来一大批；缺后者时空闲连接会被判死。两者都只在部署形态上复现——本地 vite 的开发代理不缓冲，也不按空闲超时掐连接。

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

### 7. 建对象存储桶（可选）

头像、galaxy 工程的资产私有区与发布物的公开区都存在腾讯云 COS 上（见 [design/profile/avatar-storage.md](design/profile/avatar-storage.md) 与 [design/galaxy/asset-library.md](design/galaxy/asset-library.md)）。**不做这一步服务端照样跑**：昵称与简介照常可用，只是前端不渲染头像上传区与 galaxy 的资产入口。

要做就一次做完。**半套配置会让服务端拒绝启动**——只配桶地址不给密钥、或只给密钥不配桶地址，都在启动时报错并指出缺的是哪一项。

1. **建一个桶，读写权限设为私有读写。** 公开读的桶等于把所有人的头像与工程素材公开可列举，而"预签名地址"这个设计的前提正是桶私有。三处内容靠键前缀区分，前缀都是常量：`avatars/<主体标识>`、`galaxy/<工程标识>/`（私有区，其下是 `assets/` 与 `text/`）与 `galaxy/<工程标识>/release/<内容摘要>`（公开区）。**公开区也在这个桶里**，靠逐对象的公开读与其余对象区分，见下面"发布域与公开区"。
2. **建一个子账号（CAM），只授予这一个桶的权限，且尽量收窄到这两段前缀（`avatars/` 与 `galaxy/`）。** 不要用主账号密钥：主账号密钥能操作该账号下的全部云资源，而服务端只需要碰一个桶里的两段前缀。这个子账号还需要 **`sts:GetFederationToken`**——直传凭证由服务端用长期密钥换出来（见 [design/objectstore/README.md](design/objectstore/README.md)）；不给这一项时上传会以"换取直传凭证失败"报错。
3. **把密钥写进一个仅属主可读的文件**，交给 systemd 读：

```bash
sudo install -o aladdin -g aladdin -m 0600 /dev/null /opt/aladdin/secrets.env
sudo tee /opt/aladdin/secrets.env >/dev/null <<'EOF'
ALADDIN_COS_SECRET_ID=<子账号 SecretId>
ALADDIN_COS_SECRET_KEY=<子账号 SecretKey>
EOF
```

先建空文件再写，而不是直接 `tee` 出去再 `chmod`：后者在创建与收紧之间留了一个宽权限窗口，而窗口期里文件可能已经被复制走了。这与 CLI 侧凭证文件"创建即受限"是同一条要求。

密钥**不进 `config.yml`**：那个文件会进版本库、进镜像、被贴给别人排查问题（见 [design/config/credentials.md](design/config/credentials.md)）。这与"生产机上不出现 `ALADDIN_DEV_SEED`"是同一条理由的两面。

**所有密钥放这一个文件，一个功能一份 env 是不要的。** 约束只到"密钥走环境变量"，没说它们要分几份；而分开只会多出漂移面——多一个文件就多一行 `EnvironmentFile=`，多一处"重装单元时漏掉"的机会。分开也换不来隔离：读它们的是同一个进程、同一个 uid，`EnvironmentFile` 机制也一样，信任域完全重合。第 11 步的 GitHub 密钥因此**追加到同一个文件**。

4. 在 `/opt/aladdin/config.yml` 填 `cos_bucket_url`——**完整桶主机名**（含 APPID 与地域，形如 `https://<桶名>-<APPID>.cos.<地域>.myqcloud.com`），然后重启。

> **桶上必须配 CORS，且这是必做的一步。** 允许主应用的源，方法包含 `PUT` 与 `GET`，以及 `Content-Type`、`Authorization` 之类的请求头。
>
> `PUT` 是浏览器直传写入。桶侧不放行时，控制台报 CORS 错误、上传一直失败。
>
> `GET` 是工作台源码视图要的：它用脚本读取私有区文本的预签名地址（`fetch` 取正文），浏览器会发跨域读请求，**响应体必须能被脚本读到**。只放行 `PUT`、或预检过了却不暴露 body 时，上传正常、切到源码却拿不到正文。
>
> `<img>` / `<video>` 把预签名地址当资源地址用时**不需要** CORS——那些标签不发跨域读请求。头像与资产缩略图走的是这条。

> **换桶等于所有存量对象在新桶里都不存在**（表现是所有人头像与全部工程素材都不显示），而档案与工程本身没被动过——头像重新上传即可，galaxy 的资产重新上架即可。

#### 发布域与公开区（只有要启用 galaxy 发布时才需要）

galaxy 的发布把该工程引用的资产上架到上面那个桶里**这个工程自己的** `galaxy/<工程标识>/release/` 下（段内按内容摘要寻址），并在上架时把那些对象**逐个设成公开读**；发布页面本身由服务端在一个**独立域**上返回。

> **不需要第二个桶，也不需要第二份授权。** 公开区与私有区共用第 1 步那个桶：桶本身保持默认私有读写，公开读**不由桶级策略给出**——上架过程逐个对象设置它。子账号的权限范围不变。

> **上架不用浏览器直传。** 服务端用长期密钥把字节从私有区读出来再写进公开区，因此那条路径不新增 CORS 规则。

5. **给发布域做解析与证书**，方式与第 1–4 步的主域完全相同（两条域名指向同一台机器、同一个回环端口，由 SNI 分流），但**域名必须与主域不同源、且不同注册域**。服务端在启动时校验这一点，同源或同注册域一律拒绝启动。

   这条不是整洁问题：发布物里跑着**用户写的脚本**，同源意味着它能读写应用的 cookie 与本地存储、能代表访问者向应用发请求。只判"不同源"不够——同注册域下的两个主机可能共享一张按域设置的 cookie，所以同注册域也拒。
6. 在 `/opt/aladdin/config.yml` 填 `galaxy_publish_base_url`（形如 `https://pages.example.com`），然后重启。桶复用第 4 步那一项——**只给发布域不给桶地址会被拒绝启动**。

> **发布域上不需要额外的鉴权。** 发布态是公开匿名的——拿到地址的人就能看。因此工程标识不可猜、且系统里不存在列出已发布工程的入口。反过来，**发布物内不得承载任何秘密**。

### 8. 装部署文件与 systemd 单元

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

单元文件里有 `EnvironmentFile=-/opt/aladdin/secrets.env`。**`-` 前缀是有意的**：文件不存在时 systemd 不报错，因此没做第 7 步的部署照常启动，头像功能保持未启用。密钥是这里唯一一类需要走环境变量的输入——它们不能进配置文件（理由同上），而 `secrets.env` 是 0600、属主是服务账号，与 CLI 侧凭证文件的保护方式一致。

> 单元文件里**只有这一行** `EnvironmentFile=`。第 11 步的 GitHub 密钥不是第二份文件，更不是第二行。"一个功能一份 env"曾让 `github.env` 只存在于服务器上，而仓库那份单元少了它——于是"按仓库重装单元"会静默丢掉 GitHub 登录：密钥文件还在，服务读不到。一份文件一行，这类漂移没有产生的余地。

### 9. 发布第一个版本

仓库里还没有任何 tag 时，Release 是不存在的。先按 [release.md](release.md) 打一个 `vX.Y.Z` 的 tag 推上去，等流水线产出 Release，然后：

```bash
sudo /opt/aladdin/deploy.sh
```

### 10. 首次引导管理员

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

第 4 步填的必须是**主体标识**，不是邮箱，也不是 Google 的 `sub`：邮箱可以被改名、被回收，回收给另一个人的那天就是一次无痕提权（见 [AGENTS.md](../AGENTS.md) 第 7 条）。第 6 步删掉配置**不会**撤销已经建立的绑定——判定路径只读数据库、从不读配置。

> **第 2 步是本部署里唯一一处不在本仓库管理的东西。** 浏览器来源白名单只存在于 Google 控制台，仓库里刻意没有对应的配置键。换域名时改的是那里，**忘记改的表现是"换了域名之后登录按钮点了没反应"**，而不是任何一条报错。

### 11. 启用 GitHub 登录（可选）

GitHub 是**重定向型**渠道：它交给浏览器的是一个授权码，服务端要用客户端密钥去换令牌，因此比 Google 多两步配置，且多一处要在渠道侧登记。

1. 在 GitHub 建 **OAuth App**，把 **Authorization callback URL** 填成
   `https://<域名>/auth/github/callback`
2. 在 `/opt/aladdin/config.yml` 填入：
   ```yaml
   github_client_id: "<OAuth App 的 Client ID>"
   public_base_url: "https://<域名>"
   ```
3. 把客户端密钥**追加**到第 7 步那个 `secrets.env` 末尾，**不要**写进 `config.yml`，也**不要**另起一份（理由见第 7 步）：
   ```bash
   [[ -e /opt/aladdin/secrets.env ]] || sudo install -o aladdin -g aladdin -m 0600 /dev/null /opt/aladdin/secrets.env
   sudo tee -a /opt/aladdin/secrets.env >/dev/null <<'EOF'
   ALADDIN_GITHUB_CLIENT_SECRET=<Client Secret>
   EOF
   ```
   第一行的守卫让"没做第 7 步"的部署也能拿到一个创建即 0600 的文件，而不是让 `tee -a` 按 root 的 umask 建出一个宽权限的。单元文件**不动**——它那一行 `EnvironmentFile=` 已经在第 8 步装好了，且只该有那一行
4. 确认 nginx 里 `location ^~ /auth/` 转发到服务端，且这一条里带 `access_log off`（模板已含，见 [../deploy/nginx-aladdin-site.conf](../deploy/nginx-aladdin-site.conf)）——回调地址里带着授权码与登录凭据，默认的访问日志格式会把它们写进日志
5. 重启服务，登录页应出现 GitHub 入口

三项（客户端标识、客户端密钥、对外地址）**缺一即拒绝启动**，这是刻意的：半套配置的失败方式是"看起来配好了"，直到有人点了登录才失败。

> 换域名时要同时改三处：GitHub 控制台的授权回调地址、配置里的 `public_base_url`、以及 nginx 的站点域名。**只改其中一处都表现为"点登录没反应"或"回调 404"**，而不是任何一条报错。

**GitHub 登录会得到一个零权限的新主体**，不会自动并入已有的 Google 账号：把两个渠道归到同一个主体是「绑定」这个动作，而绑定重定向型渠道目前尚未支持（见 [design/identity/github-login.md](design/identity/github-login.md) 的待定决策）。

### 12. 命令行登录（设备码，可选）

命令行登录不依赖任何一个渠道：终端打印一个短码，人在**自己已经登录的浏览器**里批准，终端随后拿到一份属于那个主体的会话（见 [design/identity/device-login.md](design/identity/device-login.md)）。

只需要一个键，与 GitHub 登录是同一个：

```yaml
public_base_url: "https://<域名>"
```

服务端用它构造批准页地址。**没有它这条路径整体缺席**——`aladdin login` 会明确说未启用，而不是打印一个打不开的地址。

nginx **不需要任何改动**：批准页 `/device` 是前端路由，落在 `location /` 的 SPA 兜底里（**不要**把它转发给服务端，转发会让浏览器拿到一份 HTML 之外的响应）。

**这条路径不需要 `access_log off`**，与 `/auth/github/` 的区别正在这里：短码由人在页面上手动输入，不进 URL，因此不会进访问日志。这是刻意的——短码要由人与终端上显示的比对，把短码放进地址会消掉这次核对。

用法是 `aladdin login`（不带 `--token`）。带 `--token` 时走的仍是机器凭证那条既有路径，两者不受彼此影响。

## 日常发布

```bash
git tag v0.2.0 && git push origin v0.2.0      # 本机：触发构建
ssh <主机别名>                                 # 登录服务器
sudo /opt/aladdin/deploy.sh v0.2.0             # 省略版本号则取最新 Release
```

`deploy.sh` 依次做：拉取产物 → 校验和 → 备份当前二进制与前端 → 原子替换 → 重启 → 健康检查（最多 15 秒）→ 失败则回滚 → **成功后刷新主站上的 CLI 自更新镜像**（见 [design/cli/self-update.md](design/cli/self-update.md)）。

镜像那一步是**尽力而为**的收尾：走到那里 Release 已经产出、服务端已经换好，没有可回滚的东西，因此任何失败都只告警、不让本次部署失败，也不碰上一版镜像。它镜像的是 **GitHub 当时的 latest**，与本次部署的版本号无关——一次服务端回滚不该把第三方 CLI 的镜像一起往回带。想单独刷新它，重新跑一次 `deploy.sh` 即可。

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

**发布产物默认就连生产**，下载下来不需要任何配置：CI 在构建时把生产地址注入 CLI 二进制（见 [release.md](release.md)），`aladdin login` 直接走 HTTPS——Connect over TLS，端口 443，由上面那套反向代理接住。这个地址**不在仓库里**：它是部署实例的值，由 CI 的仓库变量提供，与 nginx 模板里的 `__SITE_DOMAIN__` 是同一条约束。

**源码构建出来的 CLI 默认连本机**（`127.0.0.1:9090`）。注入只作用于发布构建，所以开发时的行为与从前完全一致；要连生产就显式给地址：

```bash
aladdin --address <域名>:443 whoami
```

**非回环地址一律用 TLS，没有关掉它的开关。** 回环（本机开发、下面的隧道）才允许明文——CLI 的凭证是 `Authorization: Bearer`，明文过境等于把凭证交出去。因此一个"跳过证书校验"的开关也不会存在：它与"信任该来源"是同一类东西。

### 老路仍然可用：SSH 隧道

不想让 CLI 直连、或者根本没有证书时：

```bash
ssh -L 9090:127.0.0.1:9090 <主机别名>
```

源码构建的 CLI 保持默认配置即可（默认地址就是 `127.0.0.1:9090`），隧道与反向代理走的是同一个服务端入口，全程加密，不需要在防火墙上开任何端口。**发布产物**要走上隧道就显式指回本机：`aladdin --address 127.0.0.1:9090 ...`。

> **不要把 9090 直接对公网开放。** 明文端口等于让凭证以明文过境——隧道与 nginx 就是为了避免这件事。

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
| 命令行报 `read server preface` / `frame header looked like an HTTP/1.1 header` | 对面回了 HTTP/1.1，说明目标地址上不是 RPC 端点。先确认地址：发布产物应指生产站点的 443（`aladdin --debug ...` 会打印解析出的地址）。地址被别的服务占着也会这样（默认地址的 9090 是常见位置） |
| 静态资源 404 但文件确实在 | 站点配置的 `root` 是否指向 `/opt/aladdin/web`；文件权限是否允许 nginx 用户读取 |
| 浏览器报证书错误 | 证书名与站点 `server_name` 是否一致；`certbot renew --dry-run` 是否通过 |
| 部署后健康检查失败并自动回滚 | `deploy.sh` 打印的服务端日志；若含"未知版本"，是迁移与回滚的冲突，见上文"回滚" |
| 服务端起不来且日志说端口被占 | 9090 被同机别的服务占了，换端口要同时改三处（见"端口"） |
| 换了域名后登录按钮点了没反应 | Google 控制台的浏览器来源白名单没改 |
| 点 GitHub 登录没反应或回调 404 | 三处域名只要有一处没改就会这样：GitHub 控制台的授权回调地址、配置里的 `public_base_url`、nginx 站点域名。另需确认 nginx 的 `location ^~ /auth/` 转发到了服务端。**这条路径刻意不记 nginx 访问日志**（地址里带着授权码与凭据），因此"请求有没有打到服务端"要看服务端日志：走对了会有登录成功或"登录未完成"的留痕；走错了才会在 `location /` 的访问日志里留下一条 200 |
| GitHub 登录曾正常、某次重装单元后失效 | 单元里是否丢了 `EnvironmentFile=-/opt/aladdin/secrets.env`，或密钥是否仍留在旧的 `github.env` 里（同一个键不会读两处，旧的不会再被读到）。**重新装了单元就必须核对这一行**——它是唯一会被"按仓库重装"覆盖掉的一行 |
| 管理员登录后仍然"没有权限" | 引导是否生效：`sudo journalctl -u aladdin-server \| grep -i 引导`；引导只在存储中无任何绑定时生效 |
| 数据"看起来全丢了" | 验证真实的数据路径：`sudo journalctl -u aladdin-server \| grep -i 数据库`——启动日志有脱敏后的定位信息 |
| 命令行说"未启用设备码登录" | 配置里没有 `public_base_url`；这条路径在缺它时整体缺席（见"命令行登录"） |
| 命令行打印的批准页地址打不开 | nginx 是否把 `/device` 也转发给了服务端；它必须由 `location /` 的 SPA 兜底接走 |
| 命令行登录卡在等待批准 | 批准页要输入终端上显示的短码，且这一次登录有有效期；过期后终端会提示重新发起 |
| 服务被 OOM 杀掉后自动重启 | `journalctl -u aladdin-server \| grep -i memory`；单元里的 `MemoryMax` 是保险丝，不是估算 |
| 发布页面能打开但图片/视频不显示 | 那些对象有没有被设成公开读（上架那一步是否跑完）。**发布页面的脚本发不出请求是正常的**——内容安全策略按设计挡住了一切出站请求 |
| 发布按钮不渲染 | `galaxy_publish_base_url` 与 `cos_bucket_url` 是否都配了；`sudo journalctl -u aladdin-server \| grep -i 发布` |
| 发布域打不开而主域正常 | 发布域的解析、证书与 nginx `server_name` 三处；**不要图省事把它指回主域**——服务端会因同源而拒绝启动 |
| 撤回发布后地址仍然出内容 | 撤回是把工程的发布指针置空；若内容还在，看是不是浏览器缓存了产物（服务端的返回不带长效缓存） |
| 改了 nginx 配置没生效 | 需要 `sudo nginx -t && sudo systemctl reload nginx`；反过来，**只换静态产物不需要 reload** |
| 命令行报"主站镜像也不可用" | 先看镜像本身在不在：`curl -fsS https://<域名>/cli/latest/version.json`。404 多半是站点配置里少了 `/cli/latest/` 那条 location（见"一次性前置"第 3 步），或者服务器上那份 `deploy.sh` 还是旧的（镜像那一步是后加的，改过 `deploy/` 的文件就要重新 scp 一次） |
| 镜像里的 tag 比 Release 旧 | 镜像只在部署收尾刷新。看最近一次 `deploy.sh` 有没有打印"已刷新 CLI 镜像"——没打印就往上翻它的告警（读不到元数据、下载失败、校验和不匹配都会只告警） |
| 头像不显示，昵称与简介正常 | 桶地址与密钥是否配好（`sudo journalctl -u aladdin-server \| grep -i 头像`）；预签名地址是否已过有效期——刷新页面即拿到新地址 |
| 服务端起不来且日志说缺 COS 密钥 | 半套头像配置：只配了 `cos_bucket_url` 没给密钥，或反之。这是有意拒绝启动，不是故障 |
| 控制台报 CORS 错误 | 分清写入还是读取。上传失败：桶是否允许主应用源的 `PUT`。工作台源码视图拿不到正文：是否允许 `GET`，且脚本能读到响应体。`<img src>` 取头像或缩略图不需要 CORS；头像不显示先看预签名是否过期、桶与密钥是否配好 |
| 换了桶之后所有人头像都不显示 | 对象键在新桶里不存在；档案本身没被动过，重新上传即可 |

## 依赖关系

| 依赖对象 | 交互方式 |
|---------|---------|
| 发布产物 | 消费 Release 里的 `linux_amd64` 二进制包与前端包，不自行编译（见 [release.md](release.md)） |
| 服务端配置 | `deploy.sh` 保证 `/opt/aladdin/config.yml` 存在后才发布 |
| 持久化 | 备份策略消费 sqlite 的 WAL 语义 |
| 身份认证 | 首次引导依赖一次真实登录产出的主体标识（见 [design/identity/channel-login.md](design/identity/channel-login.md)） |
| 反向代理 | 提供 TLS 终止、静态托管与到 9090 的转发；**按 HTTP/1.1 转发上游即可**（浏览器与命令行都走 Connect）。具体用什么、443 上还有没有别人，由宿主决定 |
| CLI 的默认目标地址 | 发布产物里带着构建期注入的生产地址，注入值由 CI 的仓库变量提供（见 [release.md](release.md)）；仓库里只有模板与占位符 |
| CLI 自更新镜像 | 官方站点 `/cli/latest/` 下的静态文件，由 `deploy.sh` 在收尾时从 GitHub 的 latest 覆盖写；命令行从中推导地址、不经配置（见 [release.md](release.md) 与 [design/cli/self-update.md](design/cli/self-update.md)） |
| 可观测性 | 日志走 journald；`otel_endpoint` 留空表示不上报，链路标识照常生成与传播 |
| 头像存储 | COS 桶；桶地址由 `cos_bucket_url` 给出，密钥由 systemd 的 `EnvironmentFile` 提供（见 [design/profile/avatar-storage.md](design/profile/avatar-storage.md)） |
| galaxy 资产私有区 | 同一个桶的 `galaxy/` 前缀；地址同样由 `cos_bucket_url` 给出（见 [design/galaxy/asset-library.md](design/galaxy/asset-library.md)） |
| galaxy 发布 | 与资产**同一个桶**的 `galaxy/<工程标识>/release/` 前缀（公开读由每个对象自己带着）与一个**独立的发布域**（`galaxy_publish_base_url`）；发布域的可注册域必须与主域不同（见 [design/galaxy/publication.md](design/galaxy/publication.md)） |

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
| 发布产物默认指向生产 | `aladdin --debug whoami` 打印的正是生产地址（人工核对项：判据里不写实值） |
| 主站只暴露最新的 CLI | `curl -fsS https://<域名>/cli/latest/version.json` 报的 tag 与最新 Release 相同；更早的 tag 的包经同一路径取不到（部署后冒烟） |
| 镜像与 Release 同源 | 从主站取回的包与它那份 `SHA256SUMS` 里的条目对得上（部署后冒烟：下载后 `sha256sum -c`） |
| 非回环强制 TLS | 指向非回环明文端点时被拒绝，而不是明文过境（`pkg/client` 单测 + 冒烟） |
| 只监听回环 | `ss -lnt \| grep 9090` 显示 `127.0.0.1:9090` 而非 `0.0.0.0:9090` |
| 证书可续期 | `certbot renew --dry-run` 通过；`:80` 的 ACME location 仍在 |
| 生产无认证旁路 | 生产机上 `ALADDIN_DEV_SEED` 未出现在 systemd 单元与环境中 |
| 备份可用 | `VACUUM INTO` 产出的快照能被一个新进程打开并读到既有数据 |
| 密钥文件仅属主可读 | `/opt/aladdin/secrets.env` 权限为 0600、属主为 aladdin（部署后核对） |
| 密钥只有一份、只有一行 | `/opt/aladdin` 下没有按功能拆开的 env 文件（`cos.env`、`github.env` 等），单元里的 `EnvironmentFile=` 也恰好一行且指向 `secrets.env`（部署后核对） |
| 桶为默认私有读写 | 去掉预签名参数直接访问私有对象地址被拒（部署后冒烟） |
| 公开区对象为公开读 | `galaxy/<工程标识>/release/` 下的对象无需签名即可取到（部署后冒烟） |
| 公开区不含私有内容 | `galaxy/<工程标识>/release/` 下只有按内容摘要命名的发布物对象；桶未开启列举权限（部署后冒烟） |
| 直传凭证不得能声明权限 | 用当前策略签发的临时凭证带 ACL 头与授权头各直传一次，两次都被存储侧拒绝（部署后冒烟，见 [design/objectstore/README.md](design/objectstore/README.md)） |
| 发布域与主域不同注册域 | 两个域的注册域不同，且配置校验在启动时通过（`internal/config` 测试 + 部署后检查） |
| 发布域上无凭证可达 | 不带任何凭证请求 `https://<发布域>/g/<工程标识>` 能取到当前产物（部署后冒烟） |
| 头像功能可缺省 | 未做第 7 步的部署照常启动，前端不渲染头像上传区（部署后冒烟） |
| 仓库不含实例值 | `git log --all -p \| grep -iE "真实域名\|主机别名\|公网 IP"` 无命中（长期项：**每次提交前**都要成立） |

---

> 本文是部署形态的唯一信源。`deploy/` 下的三份文件是实现，实现细节变更只改文件；**拓扑、端口、TLS 终止位置、回滚语义、备份策略**的变更必须先改本文。
