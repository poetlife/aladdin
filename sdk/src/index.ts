/**
 * aladdin 创作与发布 SDK：**用户在自己的构建与自己的页面里**接入平台。
 *
 * 这个包有三个面，各自住在一个子路径下——**按面引入，不要从这个入口引**：
 *
 *   - `@aladdin/sdk/build` —— 构建期插件。跑在用户的构建里，**不联网**。
 *   - `@aladdin/sdk/client` —— 调用面。跑在用户的构建脚本里，联网。
 *   - `@aladdin/sdk/runtime` —— 运行期包。跑在访客浏览器里的那一页上。
 *
 * 从这里引会把三个面全部拖进来，其中两个用到 node 的内置模块（`node:crypto`、
 * `node:fs`），在浏览器里会**在打包期**就报错——报得响，但没必要先踩一次。
 *
 * 不做什么，与 docs/design/galaxy/sdk.md 里那三条红线一一对应：不改用户的源、
 * 不接管他的构建、不往产物里注入任何字节、不做"能不能发布"的判定。
 */
export * as build from './build/index.js'
export * as client from './client/index.js'
export * as runtime from './runtime/index.js'
