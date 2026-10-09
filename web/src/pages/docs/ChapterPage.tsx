import { Card, Space } from 'antd'
import { useMemo } from 'react'

import { Markdown } from '../../ui/Markdown'
import type { DocsChapter } from './chapters/manifest'
import { parseChapter } from './chapters/sections'

/**
 * 一章的样子：引言一张卡片，之后一小节一张卡片。
 *
 * **它不认识任何具体章节。** 正文在 `chapters/<slug>.md` 里，页面这一侧只决定
 * "一段 Markdown 长在什么上"——因此加一章不用碰这个文件，改配色与间距也只在这里
 * 一处生效。
 *
 * 章节标题取 `chapter.title`，不在正文里再写一遍 `# 标题`：标题已经在清单里，
 * 写两处就会漂，而漂的表现是"页面上叫这个名字、`llms.txt` 里叫另一个"。
 */
export function ChapterPage({ chapter }: { chapter: DocsChapter }): React.ReactNode {
  const { intro, sections } = useMemo(() => parseChapter(chapter.markdown), [chapter])

  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      {intro !== '' && (
        <Card
          title={
            <Space size={8}>
              {chapter.icon}
              {chapter.title}
            </Space>
          }
        >
          <Markdown>{intro}</Markdown>
        </Card>
      )}

      {sections.map((section) => (
        <Card key={section.title} title={section.title}>
          <Markdown>{section.body}</Markdown>
        </Card>
      ))}
    </Space>
  )
}
