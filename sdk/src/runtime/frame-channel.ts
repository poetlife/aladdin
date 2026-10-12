/**
 * 页面 ↔ 宿主之间的接入桥：**信封的形状**。
 *
 * 这条通道**已经存在**：`docs` 槽渲染出的每一页都带一段平台自己的接入脚本，
 * 信封是 `channel` / `version` / `type` / `payload` 四项。本包是那条通道在
 * **用户自己的页面**里的另一端——`site` 槽没有平台渲染的页，因此那边的接入
 * 脚本来自用户自己打进产物的这一段，而不是平台注入（见 docs/design/galaxy/sdk.md）。
 *
 * **形状只有一份，宿主侧必须复用同一个信封。** 仓库里这条协议因此有三处代码、
 * 一个形状：服务端渲染进 `docs` 槽那一页的常量脚本（Go）、主站壳侧的解析
 * （web/src/pages/galaxy/frame-channel.ts）、以及这里（用户页面侧）。
 * `frame-channel.test.ts` 把三处的取值钉在一起——**不得出现第三种形状**。
 *
 * **加的是消息类型，不是版本。** 信封本来就是对称的，v1 里宿主 → 页面那一半
 * 是空的；因此本包要用它，是增几种类型，不是开 v2。
 */

/** 通道标识。与服务端 `FrameChannelName`、主站壳的 `FRAME_CHANNEL` 是同一个值。 */
export const FRAME_CHANNEL = 'aladdin/frame'

/** 协议版本。收方不认识的版本一律忽略。 */
export const FRAME_CHANNEL_VERSION = 1

/** 页面 → 宿主：用户点了正文里的一张图。**这一条早已存在，不是本包新加的。** */
export const FRAME_IMAGE_PREVIEW_TYPE = 'image-preview'

/** 页面 → 宿主：要一份取内容用的凭证。 */
export const FRAME_CONTENT_CREDENTIAL_REQUEST_TYPE = 'content-credential-request'

/** 宿主 → 页面：那一份凭证（或它的否定结论）。 */
export const FRAME_CONTENT_CREDENTIAL_TYPE = 'content-credential'

/** 一条消息的信封。**四个字段，两个方向同一套。** */
export interface FrameEnvelope {
  channel: string
  version: number
  type: string
  payload: unknown
}

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
 * 宿主对一次凭证请求的回答。
 *
 * **没有凭证时也要回一条**，而不是不回：不回的那一侧只表现为一次超时，而超时
 * 与"网络慢"分不开；回一条明确的否定，调用方才能把"这个宿主不提供凭证"与
 * "宿主没理我"分开。
 */
export interface FrameContentCredential {
  /** 取内容用的凭证。空串表示这次拿不到。 */
  credential: string
  /** 它的失效时刻（RFC 3339）。空串表示不过期或未知。 */
  expiresAt: string
  /** 拿不到时的原因，给人看的一句话。拿到时为空。 */
  reason: string
}

/** 是不是一条认得出的信封。**认不出的一律忽略，不报错、不猜。** */
export function isFrameEnvelope(data: unknown): data is FrameEnvelope {
  if (typeof data !== 'object' || data === null) {
    return false
  }
  const envelope = data as Partial<FrameEnvelope>
  return envelope.channel === FRAME_CHANNEL && envelope.version === FRAME_CHANNEL_VERSION
}

/**
 * 把一条宿主发来的载荷解析成一份凭证答复；不是这条消息就返回 `null`。
 *
 * **它只回答"这是不是约定的那一条"，不做安全判定。** "这条消息是不是来自
 * 我嵌在里面的那个宿主"由调用方判——不透明源的 `event.origin` 是字符串
 * `null`，任何沙箱帧都是它，那个值在这条通道上认不出是谁发的。
 */
export function parseContentCredential(data: unknown): FrameContentCredential | null {
  if (!isFrameEnvelope(data) || data.type !== FRAME_CONTENT_CREDENTIAL_TYPE) {
    return null
  }
  const payload = data.payload
  if (typeof payload !== 'object' || payload === null) {
    return null
  }
  const record = payload as Partial<FrameContentCredential>
  return {
    credential: typeof record.credential === 'string' ? record.credential : '',
    expiresAt: typeof record.expiresAt === 'string' ? record.expiresAt : '',
    reason: typeof record.reason === 'string' ? record.reason : '',
  }
}

/** 组一条发往宿主的消息。 */
export function frameMessage(type: string, payload: unknown): FrameEnvelope {
  return { channel: FRAME_CHANNEL, version: FRAME_CHANNEL_VERSION, type, payload }
}
