import { describe, expect, it } from 'vitest'

import { parseChapter } from './sections'

describe('章节源的切分', () => {
  it('引言与各小节按 ## 分开', () => {
    const parsed = parseChapter(['开头一句。', '', '## 第一节', '', '正文一。', '', '## 第二节', '', '正文二。'].join('\n'))

    expect(parsed.intro).toBe('开头一句。')
    expect(parsed.sections).toEqual([
      { title: '第一节', body: '正文一。' },
      { title: '第二节', body: '正文二。' },
    ])
  })

  // 命令与示例 JSON 里出现 `## ` 是**内容**，不是分节。拿它当节标题会把一段命令
  // 从中间劈开，而且不报任何错——表现是页面上多出一张空卡片。
  it('围栏代码块里的 ## 不分节', () => {
    const source = ['## 一节', '', '```bash', '## 这行是命令注释', 'echo hi', '```', '', '结尾。'].join('\n')

    const parsed = parseChapter(source)

    expect(parsed.sections).toHaveLength(1)
    expect(parsed.sections[0]?.body).toContain('## 这行是命令注释')
  })

  it('没有小节时全是引言', () => {
    const parsed = parseChapter('只有一段引言。')

    expect(parsed.sections).toEqual([])
    expect(parsed.intro).toBe('只有一段引言。')
  })
})
