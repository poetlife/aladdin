import { ContentSlot } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'

/**
 * 内容槽在界面上的展示文案（唯一入口）。
 *
 * **一个工程可以两个槽都有**，因此界面上从来不问"这个工程是什么形态"，只逐槽
 * 说话：站点、文档各是一块内容，各有一套草稿、版本与发布地址（见
 * docs/design/galaxy/site-model.md）。
 */
export function slotLabel(slot: ContentSlot): string {
  switch (slot) {
    case ContentSlot.SITE:
      return '站点'
    case ContentSlot.DOCS:
      return '文档'
    default:
      return '未知'
  }
}

/** 内容槽的一句话说明，用在选择与提示里。 */
export function slotDescription(slot: ContentSlot): string {
  switch (slot) {
    case ContentSlot.SITE:
      return '整站文件原样服务（手写页面、构建产物），入口 index.html'
    case ContentSlot.DOCS:
      return 'markdown 渲染成多页文档，入口 index.md'
    default:
      return ''
  }
}

/** 全部内容槽，按固定顺序（与服务端的展示顺序一致：站点在前）。 */
export function allSlots(): ContentSlot[] {
  return [ContentSlot.SITE, ContentSlot.DOCS]
}

/**
 * 把一个地址里的取值（`site` / `docs`）解析成内容槽，认不出来时返回 undefined。
 *
 * 它服务的是"单独打开预览"那条带查询串的地址：那里只有一个字符串，而槽的取值
 * 字面与服务端、命令行一致（见 docs/design/galaxy/cli.md）。
 */
export function slotFromName(name: string | null): ContentSlot | undefined {
  return allSlots().find((slot) => slotName(slot) === name)
}

/** 内容槽的取值字面（`site` / `docs`）。 */
export function slotName(slot: ContentSlot): string {
  switch (slot) {
    case ContentSlot.SITE:
      return 'site'
    case ContentSlot.DOCS:
      return 'docs'
    default:
      return ''
  }
}

/** 「加一个内容槽」入口的文案：工程还没有的那个槽是哪几个。 */
export function addSlotLabel(enabled: readonly ContentSlot[]): string {
  const missing = allSlots().filter((slot) => !enabled.includes(slot))
  if (missing.length === 0) {
    return ''
  }
  return `加${missing.map(slotLabel).join('或')}`
}
