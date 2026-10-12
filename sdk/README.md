# @poetlife/aladdin-sdk

aladdin 的**创作与发布 SDK**：用户在自己的构建与自己的页面里接入平台，**源保持标准**。

功能行为的唯一信源是 [docs/design/galaxy/sdk.md](../docs/design/galaxy/sdk.md)；本文件只说怎么用。

## 装上

```bash
npm install @poetlife/aladdin-sdk
```

**版本就是它所对话的服务端的版本**：包内的 proto 派生物与包来自同一个发布 tag，因此 aladdin 服务端是哪个版本，SDK 就用同一个数——不需要兼容矩阵。

**升级走 npm，本包不做自更新**（`npm update @poetlife/aladdin-sdk`，或由你的 lockfile 决定）。

## 三个面

一个包，三个入口，**按面引入**：

| 入口 | 在哪跑 | 联网 | 干什么 |
|------|-------|------|-------|
| `@poetlife/aladdin-sdk/build` | 用户的构建里 | **不** | 把源里的标准写法换成文件组条目与站点内地址；把发布根交给构建器 |
| `@poetlife/aladdin-sdk/client` | 用户的构建脚本里 | 是 | 传素材、存草稿、存版本、发布——原子操作，组合由你决定 |
| `@poetlife/aladdin-sdk/runtime` | 访客浏览器里的那一页上 | 是 | 取凭证、取内容、与宿主通信 |

不要从 `@poetlife/aladdin-sdk` 这个根入口引：它会把三个面全部拖进来，其中两个用到 node 的内置模块，在浏览器里会在打包期就报错。

## 三条红线

实现与使用都不许让这三条：

1. **构建期不联网。** 插件只读本地的资产记录；发布根由调用方先取好再传进去。构建因此不绑定目标环境。
2. **平台一个字节都不改写你的产物。** 全部改写发生在**你的构建**里，由你选的插件完成；进入平台之后平台只审与发。
3. **判定只有一处入口。** 插件认不出的引用原样留给构建器；"这份内容能不能发布"由服务端的校验入口回答，插件不替它下结论。

## 用起来

### 一、把素材送进资产库（联网，一次显式动作）

**素材入库是显式动作，不在构建里发生。** 它同时写下本地的那份资产记录。

```ts
import { createGalaxyClient, uploadAsset } from '@poetlife/aladdin-sdk/client'

const galaxy = createGalaxyClient({ address: 'https://aladdin.example.com', token })
await uploadAsset(galaxy, {
  projectId: 'prj_xxx',
  file: 'public/logo.png',
  lockFile: 'aladdin.assets.json',
  contentType: 'image/png',
})
```

### 二、构建（不联网）

发布根由调用面先取到手：

```ts
import { ContentSlot, publishRoot } from '@poetlife/aladdin-sdk/client'

const base = await publishRoot(galaxy, { projectId, slot: ContentSlot.SITE })
```

再把它交给构建：

```ts
// vite.config.ts
import { defineConfig } from 'vite'
import { galaxyBuild } from '@poetlife/aladdin-sdk/build'

export default defineConfig({
  plugins: [galaxyBuild({ publishRoot: process.env.ALADDIN_PUBLISH_ROOT! })],
})
```

源里照常写标准写法——`import logo from './logo.png'`、`<img src="./logo.png">`、CSS 里的 `url(./logo.png)`。**不必学任何 aladdin 方言。**

### 三、发布（联网，原子操作）

```ts
import { pushDraft, saveVersion, publish } from '@poetlife/aladdin-sdk/client'

await pushDraft(galaxy, { projectId, slot, directory: 'dist', lockFile: 'aladdin.assets.json' })
const version = await saveVersion(galaxy, { projectId, slot, description: '加了封面' })
await publish(galaxy, { projectId, slot, versionId: version.id }) // 发的是哪一版，由你说
```

`pushDraft` 表达的是**期望的完整状态**：清单里没有的路径就是删掉。发布**永远单独一步**——它是唯一一个让内容离开私有边界的动作，"我发出去的是哪一版"不该变成一个只有读某个封装的实现才能回答的问题。

### 四、页面里（可选）

```ts
import { contentCredential, fetchContent } from '@poetlife/aladdin-sdk/runtime'

const { credential } = await contentCredential() // 向宿主索取；本地调试时直接传一份
```

引入它时，页面里的 aladdin 代码就是**你构建产出的那一份字节**；不引入时，页面里没有一个 aladdin 字节。平台不往你的产物里注入任何东西。

## 本地资产记录

`aladdin.assets.json`：**本地的一份字节对应平台上的哪一份资产**，按内容摘要作键。

- 由调用面在素材入库时写下，由构建期插件读出——**构建期因此不必联网**；
- 它是**每个工程一份**的：资产属于唯一一个工程，换一个工程就换一份记录；
- 记录里的资产标识由**某一个部署**分配，换一个部署就是另一批。**要不要提交进版本库，取决于你的部署形态**，这个选择留给你。

摘要按字节算。移动或改名一份素材不会让记录失效。

## 开发

```bash
make sdk-install        # 装依赖
make test-sdk           # 类型检查 + 测试
make sdk-build          # 产出 dist/
make sdk-check-package  # 检查发布包里有什么（自建一遍再打包核对，含 publint）
make sdk-publish        # 发到公共 npm 源（**应急**；主路是流水线在 tag 上发）
```

`sdk/src/gen/` 是 `make gen` 从 proto 派生的，**不手工改**。

`package.json` 里的 `version` 是**占位**：真正的版本由发布那一刻从 tag 取（见 [docs/release.md](../docs/release.md) 的"npm 包（SDK）"）。改它没有意义，也不会有人读它——`make sdk-publish` 发完还会把改写过的文件还原回去。
