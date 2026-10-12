# npm publish 报 403 且**不问** OTP：npm 只对 EOTP / E401 提示口令

## 症状

`make sdk-publish SDK_VERSION=v0.7.3`（内部就是 `npm publish`）把包打好了——`@poetlife/aladdin-sdk@0.7.3`、153 个文件、241.5 kB——在最后一步被拒：

```
npm error code E403
npm error 403 403 Forbidden - PUT https://registry.npmjs.org/@poetlife%2faladdin-sdk
npm error     - Two-factor authentication or granular access token with bypass 2fa enabled
npm error       is required to publish packages.
```

**关键症状是它不问一次性口令。** 账号是登录着的（`npm whoami` 正常），在交互式终端里跑也一样，连一次 OTP 提示都没有。

## 收敛过程

1. 身份与归属都对：`npm whoami` 有账号，`npm org ls poetlife` 显示 owner。不是"没权限"这一层。
2. 包本身没问题：同一条命令里的打包检查、`npm pack` 全过（四个入口的产物与类型声明、LICENSE 都在包里）。不是"发的东西不对"这一层。
3. 先按"凭证类型不对"假设走：`~/.npmrc` 里只有一项 `//registry.npmjs.org/:_authToken`，值是 `npm_` 前缀的 40 位。**这条线索不足以判类型——重新 `npm login` 之后写的仍是 `npm_` 前缀的 40 位**（实测：`~/.npmrc` 的 mtime 与那次登录吻合，随后重跑发布，**仍是同样的 E403**）。所以"重新登录就会提示输口令"这个假设被证伪了。
4. 去看 npm 自己的实现——判据在这里，不在凭证的形状上。

```bash
# 只看键名、长度与前缀，不打印值
awk -F'=' 'NF>1 {v=$2; gsub(/[ \t\r]/,"",v); printf "%-40s 长度=%d 前缀=%s\n", $1, length(v), substr(v,1,4)}' ~/.npmrc
```

## 根因

npm 的 `otplease`（`lib/utils/auth.js`）只在两种响应上提示口令：

| 响应 | npm 的行为 |
|------|-----------|
| `EOTP`（带 `authUrl` / `doneUrl`） | 打开浏览器走一次 web 验证，再带口令重试 |
| `E401` 且正文含 `one-time pass` | 在终端里提示 `Enter OTP:` |
| **`E403`** | **直接抛出，不问** |

而账号开了 2FA 时，注册表对"令牌"这条路回的正是 **E403**（正文就是上面那句"要 2FA 或要一把开了 Bypass 2FA 的 granular token"）。于是：**npm 永远不会替你问口令**，这一条请求从第一步就被判死。

**判据因此是"问不问"，而它的含义与直觉相反**：

| 现象 | 含义 | 出路 |
|------|------|------|
| 问了 OTP，你没输 | 缺口令 | 输进去（或 `--otp=`） |
| **压根没问**，直接 403 | 注册表要 2FA，而 npm 不会替你问 | **自己把口令递过去**：`--otp=`；或换一把勾了 Bypass 2FA 的 granular token |

注意第三条对照：`npm login` 换凭证**不解决**这件事（实测过），它写出的同样是令牌，走的同样是 E403 那条路。

## 结论与边界

- **发布命令支持把口令递过去**：`make sdk-publish SDK_VERSION=v0.7.3 SDK_OTP=123456`（`--otp=` 会在第一次请求就带上，不依赖 npm 的提示）。这条是本仓库的默认用法，见 [docs/release.md](../../release.md) 的"npm 包（SDK）"。
- **不想每次输口令**：在 npmjs.com 上建一把勾了 **Bypass 2FA** 的 granular token（权限 `Read and write`、范围 `@poetlife`，创建时确认一次口令），写进 `~/.npmrc`。代价要说清楚：那是一把长期可发布的凭证，放在本机就等于"这台机器上的文件能发版"。
- **仓库侧不用改**：`make sdk-publish` 的两条守卫（版本来源、工作区干净）与打包检查都按预期工作；失败时仓库里没有残留（占位值与 lockfile 已还原），npm 上也没有半成品——**预支的那个号因此没有被烧掉**，修好凭证重跑即可。

## 相关

- 发布形态与守卫：[docs/release.md](../../release.md) 的"npm 包（SDK）"
- 为什么发布在本地而不是流水线：[docs/design/galaxy/sdk.md](../../design/galaxy/sdk.md) 的"包的分发与版本"
