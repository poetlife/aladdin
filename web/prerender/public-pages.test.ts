import { describe, expect, it } from 'vitest'

import { CHAPTERS } from '../src/pages/docs/chapters/manifest'
import { fillTemplate, htmlFileFor, pageMeta, publicPaths } from './public-pages'
import type { PageMeta } from './public-pages'

/** 一份最小模板：形状与 vite 产出的 index.html 相同。 */
const TEMPLATE = [
  '<!doctype html>',
  '<html lang="zh-CN">',
  '  <head>',
  '    <title>阿拉丁神灯</title>',
  '  </head>',
  '  <body>',
  '    <div id="root"></div>',
  '  </body>',
  '</html>',
].join('\n')

const PAGE = { html: '<p>正文</p>', css: '.a{color:red}' }

describe('公开页面的形状', () => {
  // 预渲染的路径由清单派生。手写一份的表现是"加了新章、静态页没生成"——而本地开发
  // 走 SPA 兜底，看不出这件事。
  it('公开路径就是文档索引与每一章', () => {
    expect(publicPaths()).toEqual(['/docs', ...CHAPTERS.map((chapter) => `/docs/${chapter.slug}`)])
  })

  it('每一章的 head 取的是清单里那份说明', () => {
    for (const chapter of CHAPTERS) {
      const meta = pageMeta(`/docs/${chapter.slug}`)

      expect(meta.title).toBe(`${chapter.title} · 阿拉丁神灯`)
      expect(meta.description).toBe(chapter.summary)
      expect(meta.markdown).toBe(`/docs/${chapter.slug}.md`)
    }
  })

  // 规范地址写相对路径：绝对地址会把部署实例的域名写进仓库（见 docs/deploy.md）。
  it('规范地址是相对路径，且没有 Markdown 版的页面就不声明 alternate', () => {
    expect(pageMeta('/docs')).toEqual({
      title: '文档 · 阿拉丁神灯',
      description: '阿拉丁神灯的站内文档：命令行、创作与发布、页面与外壳的通道。',
      canonical: '/docs',
    })
  })

  it('路径落成目录里的 index.html', () => {
    expect(htmlFileFor('/docs')).toBe('docs/index.html')
    expect(htmlFileFor('/docs/cli')).toBe('docs/cli/index.html')
  })
})

describe('套模板', () => {
  it('head 换成这一页的，正文进挂载点', () => {
    const html = fillTemplate(TEMPLATE, pageMeta('/docs/cli'), PAGE)

    expect(html).toContain('<title>命令行 · 阿拉丁神灯</title>')
    expect(html).toContain('<link rel="canonical" href="/docs/cli">')
    expect(html).toContain('<link rel="alternate" type="text/markdown" href="/docs/cli.md">')
    // `data-prerender` 是客户端靠它撤掉这一块的标记：它按亮色抽的，而运行期注入的
    // 样式插在 head 最前面，静态那一份因此永远排在后面、在零特异性下胜出
    // （见 src/main.tsx）。
    expect(html).toContain('<style data-prerender>.a{color:red}</style>')
    expect(html).toContain('<div id="root"><p>正文</p></div>')
    // 模板里那个默认标题不该留下第二份。
    expect(html).not.toContain('<title>阿拉丁神灯</title>')
  })

  it('索引页没有 Markdown 版，因而不声明 alternate', () => {
    const html = fillTemplate(TEMPLATE, pageMeta('/docs'), PAGE)

    expect(html).not.toContain('rel="alternate"')
  })

  // 标题与说明写进属性与文本节点，得转义——写错了页面不会报错，只会显示成
  // 一段被截断的字符串。
  it('head 里的文本按 HTML 转义', () => {
    const meta: PageMeta = {
      title: '<script>alert(1)</script>',
      description: '引号"与 & 号',
      canonical: '/docs',
    }

    const html = fillTemplate(TEMPLATE, meta, PAGE)

    expect(html).toContain('&lt;script&gt;alert(1)&lt;/script&gt;')
    expect(html).toContain('引号&quot;与 &amp; 号')
  })

  // 模板改了而这里没跟上时，必须**当场失败**：静默产出的会是一份"看起来生成了、
  // 其实没有正文"的页面，而那正是这套东西要消灭的那种页面。
  it('模板里少了挂载点或标题就抛错', () => {
    expect(() => fillTemplate('<html><head></head><body></body></html>', pageMeta('/docs'), PAGE)).toThrow(
      /<title>/,
    )
    expect(() =>
      fillTemplate('<html><head><title>x</title></head><body></body></html>', pageMeta('/docs'), PAGE),
    ).toThrow(/id="root"/)
  })
})
