import { join } from 'node:path'

import { CHAPTERS } from '../src/pages/docs/chapters/manifest'

/**
 * 公开文档页的"形状"：哪些路径是公开的、每一页的 `<head>` 里写什么、渲染结果写到哪个
 * 文件。**这一份是那几个事实的唯一来源。**
 *
 * 它与 React 无关，也与文件系统无关——预渲染那一趟（generate.tsx）与它的测试读的都是
 * 这里。分开是为了让"哪一页叫什么名字"这件事能被单独验证，不必先跑一遍渲染。
 */

/** 站点名。页签标题里用它收尾，与 index.html 里那个默认标题同源。 */
export const SITE_NAME = '阿拉丁神灯'

/** 模板里那个空的挂载点。客户端据此判断"这一页有没有预渲染内容"（见 main.tsx）。 */
export const TEMPLATE_ROOT = '<div id="root"></div>'

/**
 * 公开路径：文档索引与每一章。
 *
 * 它由清单派生，不手写——手写一份的表现是"加了新章节、静态页却没生成"，
 * 而那件事在本地开发里看不出来（dev 走的是 SPA 兜底）。
 */
export function publicPaths(): string[] {
  return ['/docs', ...CHAPTERS.map((chapter) => `/docs/${chapter.slug}`)]
}

/** 一页的 `<head>` 里那几项。它们随路径不同，因此按路径算。 */
export interface PageMeta {
  readonly title: string
  readonly description: string
  /** 规范地址。**写成相对路径**：绝对地址会把部署实例的域名写进仓库。 */
  readonly canonical: string
  /** 这一页对应的 Markdown 版本；索引没有，因此可缺省。 */
  readonly markdown?: string
}

/** 按路径算出这一页的 head 项。 */
export function pageMeta(path: string): PageMeta {
  const chapter = CHAPTERS.find((candidate) => path === `/docs/${candidate.slug}`)
  if (chapter === undefined) {
    return {
      title: `文档 · ${SITE_NAME}`,
      description: `${SITE_NAME}的站内文档：命令行、创作与发布、页面与外壳的通道。`,
      canonical: '/docs',
    }
  }
  return {
    title: `${chapter.title} · ${SITE_NAME}`,
    description: chapter.summary,
    canonical: `/docs/${chapter.slug}`,
    markdown: `/docs/${chapter.slug}.md`,
  }
}

/** 路径 → 产物目录里的那一份文件。`/docs/cli` → `docs/cli/index.html`。 */
export function htmlFileFor(path: string): string {
  return join(...path.split('/').filter((segment) => segment !== ''), 'index.html')
}

/** 一页渲染出来的两半：正文与水合前要用的样式。 */
export interface RenderedPage {
  readonly html: string
  readonly css: string
}

/** 把一段文本放进 HTML 属性或文本节点里。文档的标题与说明都来自仓库，但仍然转义。 */
function escapeHtml(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
}

/**
 * 把渲染结果套进外壳模板。模板就是客户端构建出来的那份 `index.html`。
 *
 * 两处替换都**找不到就抛错**：模板改过（换了挂载点、去了 title）而这里没跟上时，
 * 静默产出会是一份"看起来生成了、其实没有正文"的页面——而那正是这套东西要消灭的
 * 那种页面。
 */
export function fillTemplate(template: string, meta: PageMeta, page: RenderedPage): string {
  const head = [
    `<title>${escapeHtml(meta.title)}</title>`,
    `<meta name="description" content="${escapeHtml(meta.description)}">`,
    `<link rel="canonical" href="${meta.canonical}">`,
    ...(meta.markdown === undefined
      ? []
      : [`<link rel="alternate" type="text/markdown" href="${meta.markdown}">`]),
    styleTag(page.css),
  ].join('\n    ')

  const withHead = template.replace(/[ \t]*<title>.*?<\/title>/, head)
  if (withHead === template) {
    throw new Error('模板里没有 <title>，预渲染无法注入 head')
  }
  if (!withHead.includes(TEMPLATE_ROOT)) {
    throw new Error(`模板里没有 ${TEMPLATE_ROOT}，预渲染无法注入正文`)
  }
  return withHead.replace(TEMPLATE_ROOT, `<div id="root">${page.html}</div>`)
}

/**
 * 抽出来的样式那一块。
 *
 * `data-prerender` 不只是个标记，**客户端靠它把这一块撤掉**：它是构建时按亮色抽的
 * （构建时不知道访问者选了哪一档），而 cssinjs 在运行期把自己的样式插在 `head`
 * **最前面**，于是静态那一份永远排在它后面——同样零特异性的选择器下，排在后面的赢。
 * 不撤掉的话，暗色访问者看到的是一张亮色的页面，而且**怎么都不会变**。
 * 见 [main.tsx](../../src/main.tsx)。
 */
export function styleTag(css: string): string {
  return `<style data-prerender>${css}</style>`
}
