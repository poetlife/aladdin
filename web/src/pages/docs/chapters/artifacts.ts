import { CHAPTERS } from './manifest'
import type { DocsChapter } from './manifest'

/**
 * 送给 agent 的那几份产物：一章一份 Markdown，外加两份索引。
 *
 * **它们是派生物，不是第二份源。** 正文只有 `chapters/*.md` 一处，这里只做两件
 * 事：给每章拼上 `# 标题`、把清单汇成索引。因此"页面与 `.md` 会漂"这件事在结构上
 * 不可能发生——两者读的是同一个 `chapter.markdown`
 * （见 docs/design/web/agent-readable.md）。
 *
 * 链接一律写**相对路径**：绝对地址会把部署实例的域名写进仓库
 * （见 docs/deploy.md 的可验证性表）。
 */
export interface DocsArtifact {
  /** 站点根下的路径，形如 `/docs/cli.md`。 */
  readonly path: string
  readonly contentType: string
  readonly source: string
}

/** 一句话简介。它取自 README.md 的开头，改那里要一起改这里。 */
const SITE_SUMMARY =
  '阿拉丁神灯 —— 前端、服务端与命令行一体的仓库。RBAC 权限体系是底座，之上有身份登录、个人档案，以及 galaxy：用户创作工程与资产、发布成一个可公开访问的站点。'

/**
 * 接口参考不是一章：它是 proto 的派生物，由 `make api-docs` 生成、由静态目录直接
 * 服务（见 docs/design/api-docs/README.md）。索引里因此只有它一条链接。
 */
const API_DOCS_ENTRY = '- [接口参考](/api-docs/): 每个 RPC 方法的路径与鉴权要求（公开免登录）'

/** 一章的 Markdown 全文：`# 标题` 加上源正文。 */
export function chapterMarkdown(chapter: DocsChapter): string {
  return `# ${chapter.title}\n\n${chapter.markdown}\n`
}

/** `/llms.txt` 的内容：项目一句话 + 各章标题、说明与 `.md` 链接（见 llmstxt.org）。 */
export function llmsTxt(): string {
  const entries = CHAPTERS.map(
    (chapter) => `- [${chapter.title}](/docs/${chapter.slug}.md): ${chapter.summary}`,
  )
  return [
    '# aladdin',
    '',
    `> ${SITE_SUMMARY}`,
    '',
    '## 文档',
    '',
    ...entries,
    '',
    '## 其它',
    '',
    API_DOCS_ENTRY,
    '',
  ].join('\n')
}

/**
 * `/llms-full.txt` 的内容：所有章节拼接。
 *
 * 它是给"一次取完"的 agent 用的——省掉按索引逐个取的那几跳。内容与各章的 `.md`
 * 完全一致，因此不会出现"索引里一份、拼起来又是另一份"。
 */
export function llmsFullTxt(): string {
  const header = `# aladdin\n\n> ${SITE_SUMMARY}\n`
  return [header, ...CHAPTERS.map(chapterMarkdown)].join('\n')
}

/**
 * 全部产物。**唯一的清单**：构建期按它写进产物目录，开发服务器按它现场回应请求，
 * 测试按它断言。三处各写一份清单的话，某天会出现"线上有、dev 没有"这种只在一边
 * 复现的问题。
 */
export function docsArtifacts(): readonly DocsArtifact[] {
  return [
    ...CHAPTERS.map((chapter) => ({
      path: `/docs/${chapter.slug}.md`,
      contentType: 'text/markdown; charset=utf-8',
      source: chapterMarkdown(chapter),
    })),
    { path: '/llms.txt', contentType: 'text/plain; charset=utf-8', source: llmsTxt() },
    { path: '/llms-full.txt', contentType: 'text/plain; charset=utf-8', source: llmsFullTxt() },
  ]
}
