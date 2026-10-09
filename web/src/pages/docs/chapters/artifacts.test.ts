import { describe, expect, it } from 'vitest'

import { chapterMarkdown, docsArtifacts, llmsFullTxt, llmsTxt } from './artifacts'
import { CHAPTERS } from './manifest'
import { parseChapter } from './sections'

const artifacts = docsArtifacts()
const byPath = new Map(artifacts.map((artifact) => [artifact.path, artifact]))

describe('送给 agent 的产物', () => {
  // **这是"不为 agent 再手写第三份"这条约束的执行者。** 页面渲染的正文与
  // `/docs/<slug>.md` 里的正文是同一个字符串，因此"页面改了而 .md 没跟上"在结构上
  // 不可能发生——两侧读的都是 chapter.markdown。
  it('每一章的 .md 与页面读的是同一份正文', () => {
    for (const chapter of CHAPTERS) {
      const artifact = byPath.get(`/docs/${chapter.slug}.md`)

      expect(artifact, `${chapter.slug} 没有对应的 .md`).toBeDefined()
      expect(artifact?.source).toContain(chapter.markdown)
      expect(chapterMarkdown(chapter)).toBe(artifact?.source)
    }
  })

  // 页面按 `## ` 切卡片，`llms.txt` 按标题列条目。两者都读同一份源，因此
  // "页面上有这张卡片、索引里没有这一条"只可能来自一次切割失败。
  it('每一章都切得出小节，且小节标题就在 .md 里', () => {
    for (const chapter of CHAPTERS) {
      const parsed = parseChapter(chapter.markdown)

      expect(parsed.sections.length, `${chapter.slug} 没有小节`).toBeGreaterThan(0)
      for (const section of parsed.sections) {
        expect(chapterMarkdown(chapter)).toContain(`## ${section.title}`)
      }
    }
  })

  // llms.txt 的链接是给 agent 跟着走的。指到一个不存在的位置，表现是"索引里写着
  // 这一章、取回来是 404"——而那正是这份索引唯一要避免的事。
  it('llms.txt 里的每条链接都指向一份真实产物', () => {
    const links = [...llmsTxt().matchAll(/\]\(([^)]+)\)/g)].map((match) => match[1] ?? '')

    expect(links).toHaveLength(CHAPTERS.length + 1)
    for (const link of links) {
      // 接口参考是 proto 的派生物，由静态目录直接服务（见 docs/design/api-docs/README.md），
      // 不由这里产出。
      if (link.startsWith('/api-docs/')) continue
      expect(byPath.has(link), `llms.txt 指向了不存在的 ${link}`).toBe(true)
    }
  })

  it('每一章都在 llms.txt 里，说明取的是清单里那一句', () => {
    const index = llmsTxt()

    for (const chapter of CHAPTERS) {
      expect(index).toContain(`- [${chapter.title}](/docs/${chapter.slug}.md): ${chapter.summary}`)
    }
  })

  // 一次取完的那一份不该与逐章取回来的不一样，否则 agent 拿到的东西取决于它怎么取。
  it('llms-full.txt 就是各章拼接', () => {
    const full = llmsFullTxt()

    for (const chapter of CHAPTERS) {
      expect(full).toContain(chapterMarkdown(chapter))
    }
  })

  // 占位符没填干净，页面上就会出现 `{{frame.channel}}`，而它看起来像正文的一部分。
  it('产物里不残留任何占位符', () => {
    for (const artifact of artifacts) {
      expect(artifact.source, `${artifact.path} 里还有占位符`).not.toMatch(/\{\{[a-z0-9.-]+\}\}/)
    }
  })

  // 路径是**相对**的：绝对地址会把部署实例的域名写进仓库（见 docs/deploy.md 的
  // 可验证性表）。这条同时约束正文里的链接。
  it('产物里的地址都是相对路径', () => {
    for (const artifact of artifacts) {
      const links = [...artifact.source.matchAll(/\]\(([^)]+)\)/g)].map((match) => match[1] ?? '')

      for (const link of links) {
        expect(link, `${artifact.path} 里有绝对地址`).not.toMatch(/^https?:\/\//)
      }
    }
  })

  it('产物的路径不重复', () => {
    const paths = artifacts.map((artifact) => artifact.path)

    expect(new Set(paths).size).toBe(paths.length)
  })
})
