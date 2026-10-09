import { Sparkles, Terminal, Waypoints } from 'lucide-react'

import cliSource from './cli.md?raw'
import frameBridgeSource from './frame-bridge.md?raw'
import galaxySource from './galaxy.md?raw'
import { fillTokens } from './frame-tokens'

/**
 * 站内文档区的章节清单——**这一份是章节这件事的唯一来源**。
 *
 * 索引页、路由表、送给 agent 的 `.md` 与 `llms.txt` 都从它派生，因此"加一章"
 * 只需要在这里加一条、再放一个同名 `.md`。在这之前章节清单有两处（索引页的
 * 清单与路由表的子路由），两处定义比一处手写更容易漂。
 *
 * 正文是 `.md` 文件，不在组件里：文档区的三层形态（人看的页面、agent 读的
 * Markdown、agent 索引）全部由同一份源生成，不为 agent 再手写一份
 * （见 docs/design/web/agent-readable.md）。
 */
export interface DocsChapter {
  /** 地址里那一段，形如 `cli` → `/docs/cli`。它同时是 `/docs/cli.md` 的文件名。 */
  readonly slug: string
  /** 章节标题。索引页、页面标题与 Markdown 的 `#` 标题都用它。 */
  readonly title: string
  /** 一句话：索引页里那行说明，也是 `llms.txt` 里这一条的说明。 */
  readonly summary: string
  /** 索引页那一项前面的图标。 */
  readonly icon: React.ReactNode
  /**
   * 章节正文，Markdown 源，**占位符已按实现里的取值填好**。
   *
   * 不含标题——`# 标题` 由消费方按 `title` 拼，免得标题在源里再写一遍。
   */
  readonly markdown: string
}

/** 章节清单，同时也是索引页与路由表里的顺序。 */
export const CHAPTERS: readonly DocsChapter[] = [
  {
    slug: 'cli',
    title: '命令行',
    summary: '怎么装、怎么登录、怎么升级',
    icon: <Terminal size={18} />,
    markdown: fillTokens(cliSource),
  },
  {
    slug: 'galaxy',
    title: '创作与发布',
    summary: '写一份 HTML，用记号引用素材，发布成别人能打开的页面',
    icon: <Sparkles size={18} />,
    // 它写的是"页面长在什么上"，不是"你要做什么"——文档页里的图片能点开，
    // 作者一行都不用写。见 chapters/frame-bridge.md。
    markdown: fillTokens(galaxySource),
  },
  {
    slug: 'frame-bridge',
    title: '页面与外壳的通道',
    summary: '文档页里的图片点开就是它：一条有版本的消息通道，以及两侧各判什么',
    icon: <Waypoints size={18} />,
    markdown: fillTokens(frameBridgeSource),
  },
]

/** 按 slug 取一章。找不到即抛错：调用方给的都是清单里的值，取不到就是写错了。 */
export function chapterBySlug(slug: string): DocsChapter {
  const chapter = CHAPTERS.find((candidate) => candidate.slug === slug)
  if (chapter === undefined) {
    throw new Error(`没有这一章：${slug}`)
  }
  return chapter
}
