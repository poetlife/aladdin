import { MediaKind } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'

/**
 * 由浏览器给出的 MIME 的**主类型前缀**猜一个媒体类别，仅用于选文件时按类别的
 * 上限早退（见 docs/design/galaxy/asset-library.md 的分档上限）。
 *
 * 它**不是校验**：真正的类别由服务端按上传方声明的类型判定、并决定此后下发给
 * 浏览器的内容类型，服务端才是权威。这里的判断只省一次往返——猜不出类别时返回
 * null，交给服务端去拒。
 */
export function mediaKindOfDeclaredType(declaredType: string): MediaKind | null {
  if (declaredType.startsWith('image/')) {
    return MediaKind.IMAGE
  }
  if (declaredType.startsWith('video/')) {
    return MediaKind.VIDEO
  }
  if (declaredType.startsWith('audio/')) {
    return MediaKind.AUDIO
  }
  if (declaredType.startsWith('font/')) {
    return MediaKind.FONT
  }
  return null
}
