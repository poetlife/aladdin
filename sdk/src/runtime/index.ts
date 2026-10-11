/**
 * 运行期包：跑在**访客浏览器里的那一页**上。
 *
 * **它不往页面里注入任何东西——它本身就是用户产出的字节之一。** 用户不引入它
 * 时，页面里没有一个 aladdin 字节；引入时，页面里的 aladdin 代码就是用户构建
 * 产出的那一份（见 docs/design/galaxy/sdk.md）。
 */
export {
  FRAME_CHANNEL,
  FRAME_CHANNEL_VERSION,
  FRAME_CONTENT_CREDENTIAL_REQUEST_TYPE,
  FRAME_CONTENT_CREDENTIAL_TYPE,
  FRAME_IMAGE_PREVIEW_TYPE,
  frameMessage,
  isFrameEnvelope,
  parseContentCredential,
} from './frame-channel.js'
export type {
  FrameContentCredential,
  FrameEnvelope,
  FrameImage,
  FrameImagePreview,
} from './frame-channel.js'
export { contentCredential } from './credentials.js'
export type { ContentCredential, ContentCredentialOptions } from './credentials.js'
export { fetchContent, isSameOriginAsSelf } from './content.js'
export type { FetchContentOptions } from './content.js'
