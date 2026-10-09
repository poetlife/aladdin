import {
  FRAME_CHANNEL,
  FRAME_CHANNEL_VERSION,
  FRAME_IMAGE_PREVIEW_TYPE,
} from '../../galaxy/frame-channel'

/**
 * 章节源里的占位符，以及它们各自取自实现里的哪一个常量。
 *
 * **取值只有一处定义**：通道名、协议版本、消息类型都是对外契约，写在
 * `frame-channel.ts` 里（见 docs/design/web/docs-area.md）。正文里手抄一份的表现
 * 是协议改了而文档不声不响地继续说旧的——它是对外契约，说错话比看不了更糟。
 *
 * 因此章节源里写的是 `{{frame.channel}}` 这样的记号，构建与渲染时按这张表填。
 */
const TOKENS: ReadonlyMap<string, string> = new Map([
  ['frame.channel', FRAME_CHANNEL],
  ['frame.version', String(FRAME_CHANNEL_VERSION)],
  ['frame.image-preview', FRAME_IMAGE_PREVIEW_TYPE],
])

/** 记号的形状。名字里只允许小写字母、数字、点与短横，免得把正文里的其它花括号卷进来。 */
const TOKEN_PATTERN = /\{\{([a-z0-9.-]+)\}\}/g

/**
 * 把章节源里的占位符填成实现里的取值。
 *
 * 遇到不认识的记号**直接抛错**，不留它在正文里：留着的话，页面上会出现一个
 * `{{frame.xxx}}` 而没有任何人报错——那正是"手抄一份"最坏的表现形式，
 * 只是换了个位置发生。
 */
export function fillTokens(markdown: string): string {
  return markdown.replace(TOKEN_PATTERN, (_match, name: string) => {
    const value = TOKENS.get(name)
    if (value === undefined) {
      throw new Error(`章节源里有未登记的占位符：{{${name}}}`)
    }
    return value
  })
}
