/**
 * 文档页 ↔ 宿主之间的接入桥：**宿主侧消息形状与校验的唯一入口**。
 *
 * 对端是服务端渲染进 `docs` 槽每一页的那一段常量脚本（见
 * internal/galaxy/doc_frame_bridge.go 与 docs/design/galaxy/site-model.md 的
 * "平台接入桥"）。两边没有共享的生成物——一边是 Go 常量、一边是 TypeScript——因此
 * 这份形状是**跨语言的第二份**：改一处必须同时改另一处，两侧的测试各钉一次。
 *
 * 这是平台在自己渲染的文档页上提供的一个标准接口，不是往用户内容里注入的东西：
 * `site` 槽逐字交付用户的整站，那边没有这条通道。
 */

/** 通道标识。与服务端 `FrameChannelName` 是同一个值。 */
export const FRAME_CHANNEL = 'aladdin/frame'

/** 协议版本。与服务端 `FrameChannelVersion` 是同一个值；收方不认识的版本一律忽略。 */
export const FRAME_CHANNEL_VERSION = 1

/** 正文里的一张图：地址与替代文本。 */
export interface FrameImage {
  src: string
  alt: string
}

/** 一次图片预览请求：这一页的全部正文图，以及被点的那一张的下标。 */
export interface FrameImagePreview {
  images: FrameImage[]
  index: number
}

/**
 * 一次预览请求最多接受这么多张图。正常文档到不了；到不了的数字只可能是伪造或异常，
 * 因此不让它决定宿主页面上要挂多少张图。
 */
const MAX_IMAGES = 100

/** 单张地址的长度上限。 */
const MAX_SRC_LENGTH = 2048

/** 替代文本的长度上限：它只喂给 `alt`，再长也没有意义。 */
const MAX_ALT_LENGTH = 300

/**
 * 把一条来自沙箱 iframe 的 `message` 载荷解析成一次图片预览请求；不是这条消息就
 * 返回 `null`。
 *
 * **它只回答"这是不是约定的那一条"，不做安全判定。** "这条消息是不是来自我们自己
 * 那一帧"由调用方按 `event.source` 判（见 [SandboxFrame](./SandboxFrame.tsx)）——
 * 不透明源的 `event.origin` 是字符串 `null`，而任何沙箱帧都是 `null`，那个值在这条
 * 通道上认不出是谁发的，不能拿来当判据。
 *
 * 地址只被拿去渲染 `<img>`，因此这里不关心它指向什么内容，只挡住"不该出现在图片
 * 地址里的写法"（非 http / https 的绝对地址）与不合理的体量。载荷有多大不该由发
 * 消息的那一侧单方面决定。
 */
export function parseFrameImagePreview(data: unknown): FrameImagePreview | null {
  if (!isRecord(data)) {
    return null
  }
  if (data.channel !== FRAME_CHANNEL || data.version !== FRAME_CHANNEL_VERSION) {
    return null
  }
  if (data.type !== 'image-preview') {
    return null
  }

  const payload = data.payload
  if (!isRecord(payload)) {
    return null
  }
  const rawImages = payload.images
  if (!Array.isArray(rawImages) || rawImages.length === 0 || rawImages.length > MAX_IMAGES) {
    return null
  }

  const images: FrameImage[] = []
  for (const raw of rawImages) {
    if (!isRecord(raw)) {
      return null
    }
    const src = raw.src
    if (typeof src !== 'string' || !isDisplayableUrl(src)) {
      return null
    }
    images.push({ src, alt: typeof raw.alt === 'string' ? raw.alt.slice(0, MAX_ALT_LENGTH) : '' })
  }

  const index = payload.index
  if (typeof index !== 'number' || !Number.isInteger(index) || index < 0 || index >= images.length) {
    return null
  }

  return { images, index }
}

/** 一个地址是不是能放进 `<img>` 的写法：只收 http / https 的绝对地址。 */
function isDisplayableUrl(value: string): boolean {
  if (value.length === 0 || value.length > MAX_SRC_LENGTH) {
    return false
  }
  try {
    const { protocol } = new URL(value)
    return protocol === 'https:' || protocol === 'http:'
  } catch {
    return false
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}
