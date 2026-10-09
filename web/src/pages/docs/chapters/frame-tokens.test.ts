import { describe, expect, it } from 'vitest'

import { FRAME_CHANNEL, FRAME_CHANNEL_VERSION, FRAME_IMAGE_PREVIEW_TYPE } from '../../galaxy/frame-channel'
import { fillTokens } from './frame-tokens'

describe('章节源里的占位符', () => {
  // 通道名、协议版本、消息类型都是对外契约，取值只有 frame-channel.ts 一处。
  // 正文里手抄一份的表现是协议改了而文档继续说旧的——它是对外契约，说错话比看不了更糟。
  it('三个取值都取自实现里那一份', () => {
    const filled = fillTokens('{{frame.channel}} {{frame.version}} {{frame.image-preview}}')

    expect(filled).toBe(`${FRAME_CHANNEL} ${FRAME_CHANNEL_VERSION} ${FRAME_IMAGE_PREVIEW_TYPE}`)
  })

  // 留着不认识的记号而没人报错，就是"手抄一份"换了个位置发生：页面上会出现
  // 一个 {{frame.xxx}}，看起来像正文的一部分。
  it('不认识的记号直接抛错，不停在正文里', () => {
    expect(() => fillTokens('固定为 {{frame.nope}}')).toThrow(/frame\.nope/)
  })

  // 正文里出现花括号是常事（示例 JSON、命令里的 ${tag}），它们不该被当成记号。
  it('不碰不是记号的写法', () => {
    const source = '{"version": 1} 与 ${tag} 与 { 花括号 } 都不动'

    expect(fillTokens(source)).toBe(source)
  })
})
