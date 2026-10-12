# npm publish 报 403 要 2FA 却不问口令：这个账号**没开** 2FA

## 症状

`make sdk-publish SDK_VERSION=v0.7.3`（内部就是 `npm publish`）把包打好了——`@poetlife/aladdin-sdk@0.7.3`、153 个文件、241.5 kB——在最后一步被拒：

```
npm error code E403
npm error 403 403 Forbidden - PUT https://registry.npmjs.org/@poetlife%2faladdin-sdk
npm error     - Two-factor authentication or granular access token with bypass 2fa enabled
npm error       is required to publish packages.
```

**关键症状是它不问一次性口令**：账号登录着（`npm whoami` 正常）、在交互式终端里跑也一样，连一次 OTP 提示都没有。

## 收敛过程（含一条走错的路）

1. 身份与归属都对：`npm whoami` 有账号，`npm org ls poetlife` 是 owner。包本身也没问题（打包检查与 `npm pack` 全过）。
2. **走错的一步**：假定"凭证类型不对"，于是重新 `npm login` 一次。实测没用——重登之后 `~/.npmrc` 的 mtime 与那次登录吻合，重跑发布**仍是同样的 E403**。顺带否掉一条想当然的判据：`npm login` 写出的令牌与原来的**同形**（`npm_` 前缀、40 位），所以**前缀不能用来判类型**。
3. 回头看 npm 的源码，判据在那里：`otplease`（`lib/utils/auth.js`）只在 **EOTP**（带 `authUrl`/`doneUrl`，走浏览器）或 **E401 且正文含 `one-time pass`** 时提示口令；**E403 直接抛出**。也就是说"它不问"是设计，不是缺了什么参数。
4. 最后去问账号自己的状态，答案一步到位：

```bash
npm profile get --json | python3 -c "import json,sys; print(json.load(sys.stdin)['tfa'])"
# false
```

## 根因

**这个账号没有开 2FA**，而 npm 现在要求发布者有 2FA（或一把开了 Bypass 2FA 的 granular token）——注册表那句 403 正是在说这件事。账号层面缺了 2FA，注册表给的是 E403，于是 npm 既**没有可提示的东西**，也不会解释"缺的是账号上的那一步"。

顺带澄清一处容易混的东西：**登录时邮件收到的那个码是登录一次性口令，不是发布要的第二因子。** 前者由 npm 在登录/新设备验证时发出，后者要是你账号上配置的 2FA 方式（认证器 / 安全密钥）。把两者当成一件事，就会得到"口令我收到了，怎么塞不进命令里"这种卡壳。

判据因此是"问不问"，而它的含义与直觉相反：

| 现象 | 含义 | 出路 |
|------|------|------|
| 问了 OTP，你没输 | 缺口令，账号上是有 2FA 的 | 输进去（或 `SDK_OTP=` 递过去） |
| **压根没问**，直接 403 | 账号上就没有可用的 2FA（或该凭证不许发布） | **先去账号里把 2FA 开起来**，或换一把 Bypass 2FA 的 granular token |

## 结论与边界

- **先定位到这一条的适用范围**：发布的主路已经改到流水线（`release.yml` 的 `publish-npm`，走 Trusted Publishing——没有长期 token，也不需要谁输口令），因此**下面这些坑只影响本地那条应急路**（`make sdk-publish`）。当时的判断是"发布留在本地"，正是那次选择把这几个交互式前置暴露了出来。
- **正确的顺序是先改账号，再上命令行**：npmjs.com → 账号 → Two-Factor Authentication → 启用（**这里**才是输码的地方）。之后发布时 npm 会开浏览器让你验证一次。
- **想彻底免码**（本地/CI 发布的官方路子）：建一把 granular access token——权限 `Read and write`、范围 `@poetlife`、勾上 **Bypass 2FA**，写进 `~/.npmrc`。官方对它的告诫照抄：只在自动化发布确实需要时开，权限取最小、有效期取最短、定期轮换。它是一把长期可发布的凭证，放在本机就等于"这台机器上的文件能发版"。
- `make sdk-publish` 保留了 `SDK_OTP=<码>` 的口子（`--otp=` 会在第一次请求就带上，不依赖 npm 的提示），但那是**备选**：账号没 2FA 时，塞码也没用。
- **仓库侧不用改**：两条守卫（版本来源、工作区干净）与打包检查都按预期工作；失败时仓库里没有残留（占位值与 lockfile 已还原），npm 上也没有半成品——**预支的那个号因此没有被烧掉**，修好账号重跑即可。

## 相关

- 发布形态与守卫：[docs/release.md](../../release.md) 的"npm 包（SDK）"
- 为什么发布在本地而不是流水线：[docs/design/galaxy/sdk.md](../../design/galaxy/sdk.md) 的"包的分发与版本"
