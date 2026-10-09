/** 一章里的一节：页面上是一张卡片，Markdown 里是一个 `##` 小节。 */
export interface DocsSection {
  /** 小节标题。它同时是卡片标题与 Markdown 里那个 `##`。 */
  readonly title: string
  /** 小节正文，Markdown 源（占位符已填）。 */
  readonly body: string
}

/** 拆开之后的一章：开头的引言与各小节。 */
export interface ParsedChapter {
  readonly intro: string
  readonly sections: readonly DocsSection[]
}

/** 小节标题那一行。`## ` 之后到行尾都是标题。 */
const SECTION_HEADING = /^##[ \t]+(.+?)[ \t]*$/

/** 围栏代码块的开合。开栏可以带语言标记，闭栏只有反引号。 */
const FENCE = /^[ \t]*(?:```|~~~)/

/**
 * 把一章的 Markdown 拆成引言与各小节。
 *
 * **章节源与送给 agent 的 `.md` 是同一个文件**，所以页面这一侧必须能只取"一节"来
 * 配一张卡片。为此定一条形状约定：`## ` 开一行即起一节，它之前的属于引言。
 *
 * 扫描时**跳过围栏代码块**：命令与示例 JSON 里出现 `## ` 是内容，不是分节——
 * 拿它当节标题会把一段命令从中间劈开，而且不报任何错。
 */
export function parseChapter(markdown: string): ParsedChapter {
  const intro: string[] = []
  const sections: { title: string; lines: string[] }[] = []
  let inFence = false

  for (const line of markdown.split('\n')) {
    if (FENCE.test(line)) {
      inFence = !inFence
    }
    const heading = inFence ? null : SECTION_HEADING.exec(line)
    if (heading?.[1] !== undefined) {
      sections.push({ title: heading[1], lines: [] })
      continue
    }
    if (sections.length === 0) {
      intro.push(line)
    } else {
      sections[sections.length - 1]?.lines.push(line)
    }
  }

  return {
    intro: intro.join('\n').trim(),
    sections: sections.map((section) => ({
      title: section.title,
      body: section.lines.join('\n').trim(),
    })),
  }
}
