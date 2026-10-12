/**
 * 构建期插件这个面。
 *
 * 它跑在**用户的构建**里，**不联网**（见 docs/design/galaxy/sdk.md）。
 */
export { galaxyBuild, PLUGIN_NAME, rewriteAssetReferences } from './vite-plugin.js'
export type { GalaxyBuildOptions } from './vite-plugin.js'
export { AssetReferenceResolver, assetEntryPath, DEFAULT_ASSET_PATH_PREFIX } from './asset-reference.js'
export type { AssetReferenceOptions, ResolvedAsset } from './asset-reference.js'
export { normalizePublishRoot, siteURL } from './publish-root.js'
export {
  ASSET_LOCK_FILE,
  digestOfFile,
  emptyAssetLock,
  parseAssetLock,
  readAssetLock,
  withAsset,
  writeAssetLock,
} from '../lock/asset-lock.js'
export type { AssetLock } from '../lock/asset-lock.js'
