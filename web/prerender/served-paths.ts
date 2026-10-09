/**
 * 一个请求该不该由"送给 agent 的那几份产物"回答；该的话回答哪一份。
 *
 * 这份判决在**两个运行时**里各有一份实现：开发服务器（[vite.config.ts](../vite.config.ts)
 * 的中间件）与部署（[deploy/nginx-aladdin-site.conf](../../deploy/nginx-aladdin-site.conf)
 * 的两条规则）。共享的是判据，配置各写一份——两处不一致时的表现是"线上取到的是
 * 文档、本地取到的是首页"，只在一边复现。**规则改动要同时改那两处。**
 *
 * 这里只回答"该不该、是哪一份"；找不到时给 404 还是放过去由调用方按自己的运行时决定。
 */

/**
 * Vite 自己的模块请求。
 *
 * `/src/pages/docs/chapters/cli.md?import&raw` 是它把章节源读进模块图的那一条——
 * 它**不是站点路径**。把它当成"未知的 `.md`"毙掉，开发环境下整个文档区都是 404，
 * 而构建产物里根本没有这些请求，**只在 dev 复现**。
 */
function isModuleRequest(path: string): boolean {
  return path.startsWith('/@') || path.startsWith('/src/') || path.startsWith('/node_modules/')
}

/**
 * 要哪一份产物，或者 `null` 表示"这个请求不归这里管"。
 *
 * 两条判据：
 *
 * - `.md` / `.txt` 一律由这里回答（找不到就是 404）。少了它，未知路径会被 SPA 兜底
 *   接成 200 首页——软 404，正是"文档对 agent 不可读"那件事的一种。
 * - 请求头要 `text/markdown` 时，`/docs/<章>` 直接给那一份 Markdown。**只对"章"这
 *   一级生效**：放开到任意路径会让一个带该请求头的浏览器把整个应用取成 404。
 */
export function servedArtifactPath(path: string, accept: string | undefined): string | null {
  if (isModuleRequest(path)) {
    return null
  }
  if (path.endsWith('.md') || path.endsWith('.txt')) {
    return path
  }
  if (/^\/docs\/[a-z-]+$/.test(path) && accept?.includes('text/markdown') === true) {
    return `${path}.md`
  }
  return null
}
