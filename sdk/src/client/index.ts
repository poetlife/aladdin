/**
 * 调用面：**用户的构建脚本**里可以调的那些原子操作。
 *
 * 它联网，因此与构建期插件是两件事（见 docs/design/galaxy/sdk.md）。组合由
 * 用户的脚本决定——这个面**不提供"一路走到底"的调用**。
 */
export { createGalaxyClient, createGalaxyTransport } from './transport.js'
export type { GalaxyConnectionOptions } from './transport.js'
// 调用方要给出**内容槽**（`site` / `docs`）才能指到草稿、版本与发布上，因此
// 这个取值必须从包里出来：手写一个 'site' 字符串就是手写一份 proto 的枚举。
export { ContentSlot } from '../gen/proto/aladdin/galaxy/v1/galaxy_pb.js'
export type { Draft, FileEntry, Version, Publication } from '../gen/proto/aladdin/galaxy/v1/galaxy_pb.js'
export { mediaTypeOf, uploadAsset } from './assets.js'
export type { UploadAssetOptions, UploadedAsset } from './assets.js'
export { publish, publishRoot, pushDraft, saveVersion, validateDraft } from './content.js'
export type { PushDraftOptions } from './content.js'
export { directUpload } from './direct-upload.js'
export { contentDigest } from '../content-digest.js'
export {
  ASSET_LOCK_FILE,
  readAssetLock,
  withAsset,
  writeAssetLock,
} from '../lock/asset-lock.js'
export type { AssetLock } from '../lock/asset-lock.js'
