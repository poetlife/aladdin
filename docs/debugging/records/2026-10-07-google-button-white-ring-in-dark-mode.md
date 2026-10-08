# 暗色下 Google 登录按钮四周一圈白：Chrome 给跨源 iframe 刷的可读性白底

**日期**：2026-10-07
**收敛到的端**：前端呈现层——与 aladdin 的业务代码无关，是浏览器对第三方跨源 iframe 的兜底行为

> **先看这条结论：这条路已经不存在了。** 本记录写完当天，Google 渠道从"浏览器内嵌第三方登录控件"改成了**授权码重定向型**（见 [google-login.md](../../design/identity/google-login.md)）：登录入口变成本站自己的按钮，页面里不再有 Google 的脚本、iframe 与控件。因此下面两个现象（白框、改不到的圆角）**都不会再出现**——它们是"第三方在自己页面里画东西"的后果，而那个前提已经去掉。
>
> 保留本记录的理由：同一根因（暗色页面里跨源 iframe 被刷白底）适用于**任何**第三方嵌入件；将来若真要再嵌一个，这里的判据与两条死路都不必重走。

## 症状

暗色主题下，登录页（与个人资料的绑定卡片）里的 Google 登录按钮**周围多出一圈白色边框**：

- 按钮本身是对的：暗色 `outline_dark` 的底色（`#131314`）、灰边框、白字，个性化态下还带头像与邮箱。
- 白框在**按钮之外、卡片之内**，亮色主题下看不见。
- 实测几何（截图像素 ÷ 设备像素比 2）：按钮 380×40，白框约 400×44 —— 左右各多 10px、上下各多 2px。

白框只在**浏览器已登录 Google、按钮呈个性化态（"Sign in as …"）**时出现；同一天早先提交的 #49 在非个性化态下测得两个渠道按钮一致，因此当时没暴露。

## 收敛过程

按"先看是不是自己的 CSS"的顺序排除：

1. **不是 antd 或本站样式**：容器、卡片都没有边框与白底；把白框位置与 antd 的 Card/Button 盒子对不上——它比按钮大出的量（10/2px）不是任何一处站点间距。
2. **不是 GIS 的 `theme` 失效**：按钮自己是暗色的，说明 `outline_dark` 生效了（`outline` 才会画成白底）。白框与按钮配色是两件事。
3. **看 GIS 自己画的 DOM**：反混淆 `accounts.google.com/gsi/client` 后可见，个性化态走的是
   `Is()` 的一条分支——它建一个 `div#gsi_…-wrapper`，里面放
   `iframe[title="Sign in with Google Button"]`，再加一层透明的点击热区 `role="button"`；
   iframe 的尺寸与四边 `margin` 由 iframe 侧 postMessage 的 `resize` 消息设置
   （`marginTop/Left/… = verticalMargin/horizontalMargin`），**做得比可见按钮大一圈**。
   也就是说：白框与按钮之间那 10/2px，正是这个 iframe 的热区。
4. **不是 Google 设的白底**：把跨源 iframe 的溢出裁掉是本站 DOM 做得到的事，因此先试了
   "给 wrapper 加 `overflow: hidden`"。这条路被否掉：wrapper 的宽度是 `fit-content`，
   它跟着 iframe 一起变宽，裁不到 iframe **盒子内部**的那圈白；而且白底若真是 Google 设的，
   裁掉也只会把它裁成缺角。
5. **找到浏览器侧的那条规则**：Chrome 对"暗色页面里的跨源 iframe"有一条可读性兜底——
   iframe 的文档没有声明自己支持暗色时，浏览器认为里面只会是"黑字白底"，
   于是给它的画布**刷一层不透明白底**，免得黑字落在暗色上。
   本站恰好整体声明了暗色（`document.documentElement.style.colorScheme = resolved`，
   见 `web/src/theme/theme-context.tsx`），而 GIS 的按钮 iframe 文档不声明配色，
   于是兜底触发、白底落在按钮四周的热区上。

## 根因

**Chrome 在"嵌入方文档为暗色 + 被嵌入的跨源文档只支持亮色"时强制给 iframe 画布刷不透明白底**；
Google 登录按钮正是画在这样一个比可见按钮大一圈的跨源 iframe 里，于是那层白在按钮四周露出一圈。

这不是 aladdin 的样式问题，也不是 Google 的配色参数问题，而是**根上的 `color-scheme: dark`
被继承进了第三方 iframe**。同一根因的公开结论：

- [White Background issue around "Sign in with Google" Button and "One Tap" Popup](https://stackoverflow.com/questions/75226935/)（同一控件、同一症状；结论是在容器或 iframe 上声明 `color-scheme: light`）
- [Chrome is forcing a white background on frames only on some websites](https://stackoverflow.com/questions/69591128/)（机制说明：缺 `color-scheme` 即等于"只支持亮色"）
- [giscus#675](https://github.com/giscus/giscus/issues/675)（同一症状的另一种嵌入件，最终同样落到显式声明配色）

## 顺带暴露：圆角是 GIS 默认的 4px，而且改不到

白框去掉之后，同一个按钮的第二个问题露出来了：它是 GIS `rectangular` 的 4px 圆角，
而不是站点的 8px。**不是这次改动改坏的**，是一直如此，只是先前被白框挡住、
又恰好因为它是"按钮外的一圈"而没人细看边角。

在真实浏览器里打印这棵树（Chrome 走 FedCM 时）就明白了：

| 元素 | 尺寸 | 说明 |
|------|------|------|
| `div.S9gUrf-YoZ4jf` | 370×40 | GIS 建的容器，**正好等于按钮的尺寸** |
| `div`（无 class） | 370×0 | DOM 形态按钮的容器；FedCM 下是空的 |
| `iframe[title="Sign in with Google Button"]` | 390×44 | **按钮画在它里面**，靠四边负 margin 外扩 10/2px |

- 页面上**一个 `[role="button"]` 都没有**。`google-identity.ts` 里"把站点的圆角、字号
  盖到按钮上"那段用的就是 `parent.querySelector('[role="button"]')`，FedCM 形态下查不到
  东西，整段空转（#49 的实测是在 DOM 形态下做的，所以当时看着一致）。
- **用哪一形态不由站点决定。** `gsi/client` 里那条判据
  （`kv()`：`enable_fedcm_button_global_control` / `enable_fedcm_button_global_experiment`
  优先于站点传的 `use_fedcm_for_button`，而后者缺省就是 `false`）说明：默认本该走 DOM 形态，
  是这个客户端被 Google 的服务端实验分到了 FedCM 组。也就是说同一份代码在不同用户那里
  可能是两种形态，而且会随 Google 的灰度变化。
- **试过、并且否掉了"按盒子裁"**：把 `border-radius` + `overflow: hidden` 加到"正好等于
  按钮尺寸"的那层容器上，看着能让按钮四角变圆（外溢的热区也一并裁掉），但裁剪**只会切掉
  像素**——按钮自己那 4px 描边在四角被切断，边框不再闭合，比不裁更难看。跨源 iframe 内部
  没有"重新画一个 8px 描边"的手段，凡是比 4px 更大的裁切半径都必然切到它。

## 结论与边界

- **嵌入件的容器要自己声明成亮色岛**：Google 按钮容器加 `color-scheme: light`，
  继承下来的 dark 不再传进 iframe，兜底不触发，iframe 画布恢复透明，露出的就是卡片底色
  （见 `web/src/auth/google-sign-in-button.tsx`）。
- **只承诺本站能决定的那部分观感**：按钮是本站 DOM 形态时（`[role="button"]` 在）直接盖
  圆角与字号；按钮整个在跨源 iframe 里时（Chrome 的 FedCM），**圆角与字号都是 Google 的，
  改不到**——这一档只由 `theme` / `size` / `width` 决定，因此不与站点的圆角统一。
  为它去裁盒子、遮罩、描一层假边框，都属于"为了 4px 去改第三方控件的画"，不划算。
- **不去改根上的 `color-scheme`**：它是原生滚动条与表单控件跟着换色的依据，
  为一次第三方嵌入件的表现把它撤掉是拿主要收益换次要收益。
- **不靠裁第三方 DOM 修白框**：那层白是浏览器给 iframe 画布刷的，裁够不着
  （见收敛过程第 4 条），正解是让兜底不触发。
- **按钮自己的明暗不受这两条影响**：那是 GIS `theme` 参数的事（暗色 `outline_dark`）。
- 亮色主题下 `color-scheme: light` 等于继承值，因此实现里不按主题分支。

## 复现判据（**历史**：需要那个已经删掉的 GIS 控件，现在复现不了）

当时的判据留在这里，供将来真再嵌一个第三方控件时对照——**下面这些选择器如今在页面上都不存在**。

打开登录页，在控制台里对按钮容器改一次配色声明，白框应当立即消失：

```js
document.querySelector('[data-testid="google-sign-in"]').style.colorScheme = 'light'
```

反过来，把根上的暗色声明撤掉也会消失——但那是把上一条边界一起撤掉，不作为修法：

```js
document.documentElement.style.colorScheme = 'light'
```

圆角那一条也能当场看清"为什么改不到"：

```js
// 1. 按钮是 Google 画的，页面上没有 [role="button"]，只有一个装着它的跨源 iframe
const f = document.querySelector('iframe[title="Sign in with Google Button"]')
console.log('iframe', f.getBoundingClientRect().width, f.getBoundingClientRect().height)
console.log('容器', f.parentElement.getBoundingClientRect().width, f.parentElement.getBoundingClientRect().height)

// 2. 试着按盒子裁大一号的圆角：按钮四角会变圆，但**描边在角上断开**——比不裁更难看，
//    所以这条路被否掉，代码里没有这一刀（见 google-identity.ts 的说明）
f.parentElement.style.borderRadius = '8px'
f.parentElement.style.overflow = 'hidden'
```

判据：第 1 步的 iframe 比容器大一圈（当时实测容器 370×40、iframe 390×44），
第 2 步能变圆但边框断开。

## 收场：把前提去掉，而不是继续修

两个现象都不是 aladdin 的 bug，而是"第三方控件在我们页面里画东西"的固有代价：白底是浏览器给的，圆角是 Google 画的，中间那圈热区是 Google 定的——**我们能改的只有"要不要嵌它"**。

Google 渠道因此改成了**授权码重定向型**，与 GitHub 同构：入口是本站在主题下画的普通按钮，浏览器只被导航到服务端的一个端点，凭证由渠道直接交给服务端。改完之后：

- 页面里没有 Google 的脚本与 iframe，白框与圆角这两个现象一起消失；
- 服务端拿到了客户端密钥，与 GitHub 一样（见 [credentials.md](../../design/config/credentials.md)）；
- "客户端把渠道凭证交进来"那个 RPC 入口整体删除，渠道只剩一条路。

