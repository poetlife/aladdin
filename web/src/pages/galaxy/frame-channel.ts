/**
 * 文档页 ↔ 宿主之间的接入桥：**宿主侧消息形状与校验的唯一入口**。
 *
 * 对端有两个载体，**形状是同一个**：
 *
 *   - `docs` 槽渲染出的每一页里那一段常量脚本（见
 *     internal/galaxy/doc_frame_bridge.go 与 docs/design/galaxy/site-model.md 的
 *     "平台接入桥"）；
 *   - `site` 槽里用户自己打进产物的**运行期包**（见
 *     sdk/src/runtime/frame-channel.ts 与 docs/design/galaxy/sdk.md）。
 *
 * 两边没有共享的生成物——一边是 Go 常量、一边是 TypeScript——因此这份形状是
 * **跨语言的第二份**：改一处必须同时改另一处，两侧的测试各钉一次。运行期包那
 * 一份由它自己的测试读本文件与服务端源码来钉（`sdk/src/runtime/frame-channel.test.ts`）。
 *
 * 这是平台在自己渲染的页面与用户自己的页面之间提供的一个标准接口，不是往用户
 * 内容里注入的东西：用户不引入运行期包时，`site` 槽里没有一个 aladdin 字节。
 */

/** 通道标识。与服务端 `FrameChannelName` 是同一个值。 */
export const FRAME_CHANNEL = 'aladdin/frame'

/** 协议版本。与服务端 `FrameChannelVersion` 是同一个值；收方不认识的版本一律忽略。 */
export const FRAME_CHANNEL_VERSION = 1

/**
 * 页面 → 外壳：用户点了正文里的一张图。
 *
 * 这一条也是**站内文档区那一章要写的取值来源**——那一页讲的就是这个协议，
 * 因此它从这里取，不另抄一份（见 docs/design/web/docs-area.md）。
 */
export const FRAME_IMAGE_PREVIEW_TYPE = 'image-preview'

/** 页面 → 外壳：要一份取内容用的凭证（见下"取内容用的凭证"）。 */
export const FRAME_CONTENT_CREDENTIAL_REQUEST_TYPE = 'content-credential-request'

/** 外壳 → 页面：那一份凭证，或一次明确的"拿不到"。 */
export const FRAME_CONTENT_CREDENTIAL_TYPE = 'content-credential'

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
 * 外壳对一次凭证请求的回答。
 *
 * **没有凭证时也要回一条**，而不是不回：不回的那一侧只表现为一次超时，而超时
 * 与"网络慢"分不开。回一条明确的否定，页面才能把"这个外壳不提供凭证"与"外壳
 * 没理我"分开。
 */
export interface FrameContentCredential {
  /** 取内容用的凭证。空串表示这次拿不到。 */
  credential: string
  /** 它的失效时刻（RFC 3339）。空串表示不过期或未知。 */
  expiresAt: string
  /** 拿不到时的原因，给人看的一句话。拿到时为空。 */
  reason: string
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
  if (data.type !== FRAME_IMAGE_PREVIEW_TYPE) {
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

/**
 * 一条来自沙箱 iframe 的 `message` 载荷是不是"要一份取内容用的凭证"。
 *
 * 它不带任何载荷，因此这里只认类型；**"这条消息是不是来自我们自己那一帧"由
 * 调用方按 `event.source` 判**（同 parseFrameImagePreview）。
 */
export function isFrameContentCredentialRequest(data: unknown): boolean {
  return (
    isRecord(data) &&
    data.channel === FRAME_CHANNEL &&
    data.version === FRAME_CHANNEL_VERSION &&
    data.type === FRAME_CONTENT_CREDENTIAL_REQUEST_TYPE
  )
}

/**
 * 组一条发回页面的凭证答复。
 *
 * **取内容用的凭证不是主站的会话凭证。** 页面跑在不透明源的沙箱里、里面是用户
 * 自己的脚本；把会话交进去，等于让用户的内容可以替这位访客调主站的接口——那正是
 * 沙箱属性要挡住的事。因此这一条通道只下发一份**只够取这一份内容**的窄凭证，
 * 而它的签发属于内容保密那一条（#87）的服务端一半。
 *
 * 在那一半落地之前，这里如实回一条"拿不到"——**回一条明确的否定，而不是不回**：
 * 不回的那一侧只表现为一次超时，而与"网络慢"分不开。
 */
export function frameContentCredentialAnswer(): { channel: string; version: number; type: string; payload: FrameContentCredential } {
  return {
    channel: FRAME_CHANNEL,
    version: FRAME_CHANNEL_VERSION,
    type: FRAME_CONTENT_CREDENTIAL_TYPE,
    payload: { credential: '', expiresAt: '', reason: '这个部署还不能签发取内容用的凭证' },
  }
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
