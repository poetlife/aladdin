# npm publish 报 403 且**不问** OTP：本机那把 granular token 没开 Bypass 2FA

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
3. 去看本机凭证的**类型**（不是值）：`~/.npmrc` 里只有一项 `//registry.npmjs.org/:_authToken`，值以 `npm_` 开头——**granular access token**。`npm token list` 什么都不打印（它只列 classic token），与此一致。

```bash
# 只看键名、长度与前缀，不打印值
awk -F'=' 'NF>1 {v=$2; gsub(/[ \t\r]/,"",v); printf "%-40s 长度=%d 前缀=%s\n", $1, length(v), substr(v,1,4)}' ~/.npmrc
```

## 根因

npm 的规则：账号开了 2FA 之后，只有**会话登录**得到的凭证，或**勾了 Bypass 2FA 的 granular token**，才允许发布。本机这把是后者但没勾上——服务端直接拒，因此不会走"向你要一次性口令"的那条路。

**所以"不问 OTP"不是噪声，是判据**：

| 现象 | 含义 |
|------|------|
| 问了 OTP，你没输 | 缺口令：`--otp=` 或交互式输入即可 |
| **压根没问**，直接 403 | 缺权限：这把凭证不许发布，输多少口令都没用 |

## 结论与边界

- **账号侧修**，两条路：给那把 token 勾上 Bypass 2FA（权限 `Read and write`、范围 `@poetlife`，保存时确认一次 OTP），或改用 `npm login` 的会话凭证（每次发布提示输 OTP，且它会替换掉 `~/.npmrc` 里现有的 token）。
- **仓库侧不用改**：`make sdk-publish` 的两条守卫（版本来源、工作区干净）与打包检查都按预期工作；失败时仓库里没有残留（占位值与 lockfile 已还原），npm 上也没有半成品——**预支的那个号因此没有被烧掉**，修好凭证重跑即可。
- **反面边界**：Bypass 2FA 的 token 是一把长期可发布的凭证，放在本机就等于"这台机器上的文件能发版"。它换来的是每次发版少输一次口令——这笔账要发版的人自己认。

## 相关

- 发布形态与守卫：[docs/release.md](../../release.md) 的"npm 包（SDK）"
- 为什么发布在本地而不是流水线：[docs/design/galaxy/sdk.md](../../design/galaxy/sdk.md) 的"包的分发与版本"
