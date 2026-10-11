import { readFile } from 'node:fs/promises'
import { basename, extname } from 'node:path'

import type { Client } from '@connectrpc/connect'

import type { GalaxyService } from '../gen/proto/aladdin/galaxy/v1/galaxy_pb.js'
import { contentDigest } from '../content-digest.js'
import { withAsset, readAssetLock, writeAssetLock } from '../lock/asset-lock.js'
import { directUpload } from './direct-upload.js'

/**
 * 调用面：把一份素材送进资产库。
 *
 * **它是用户的显式动作，不在构建里发生。** 构建期不联网是 SDK 的第一条红线：
 * 一次构建一旦要连服务端，构建环境里就得有凭证，"这份产物是给哪个部署的"
 * 变成一个由构建时的网络决定的隐式事实（见 docs/design/galaxy/sdk.md）。
 *
 * 它同时写下**本地资产记录**里那一条——构建期插件靠它把源里的标准写法认到
 * 资产上（见 lock/asset-lock.ts）。记录是**每个工程一份**的：资产属于唯一一个
 * 工程，因此一份记录只对它所属的那个工程有意义。
 */

export interface UploadAssetOptions {
  projectId: string
  /** 本地文件的路径。 */
  file: string
  /**
   * **声明**的内容类型。留空时按扩展名判（见 mediaTypeOf）。
   *
   * 声明不等于验证：服务端只校验它在白名单内，并把它作为此后下发时回给浏览器的
   * 内容类型（见 docs/design/galaxy/asset-library.md）。
   */
  contentType?: string
  /** 展示标题。空表示没有标题，界面回退到文件名。 */
  title?: string
  /** 标签。归一化由服务端做，这里给原样。 */
  tags?: string[]
  /** 备注。纯文本。 */
  notes?: string
  /** 本地资产记录的路径。 */
  lockFile: string
  /** 记录里已经有这份字节时，仍然重新传一份（记录过期时用）。 */
  force?: boolean
}

/** 一次上传的结果。 */
export interface UploadedAsset {
  /** 资产标识。 */
  assetID: string
  /** 这份字节的内容摘要。 */
  digest: string
  /** 这一趟有没有真的传字节。记录里已经有它时为 false。 */
  uploaded: boolean
}

export async function uploadAsset(
  galaxy: Client<typeof GalaxyService>,
  options: UploadAssetOptions,
): Promise<UploadedAsset> {
  const bytes = await readFile(options.file)
  const digest = contentDigest(bytes)

  const lock = readAssetLock(options.lockFile)
  const known = lock.assets[digest]
  if (known !== undefined && options.force !== true) {
    // 同一份字节在平台上就是同一个资产，因此重复调用不产生第二份。这不只是
    // 省一次上传：它让"这份素材我早就送过了"变成一个**本地就能回答**的问题，
    // 而构建期正是靠它做到不联网的。
    return { assetID: known, digest, uploaded: false }
  }

  const contentType = options.contentType ?? mediaTypeOf(options.file)
  const filename = basename(options.file)

  const begun = await galaxy.beginAssetUpload({
    projectId: options.projectId,
    contentType,
    sizeBytes: BigInt(bytes.byteLength),
  })
  const assetID = begun.assetId
  if (assetID === '' || begun.upload === undefined) {
    throw new Error('服务端没有分配资产标识或没有下发直传凭证')
  }

  await directUpload(begun.upload, bytes, contentType)

  await galaxy.commitAssetUpload({
    projectId: options.projectId,
    assetId: assetID,
    contentType,
    digest,
    filename,
    title: options.title ?? '',
    tags: options.tags ?? [],
    notes: options.notes ?? '',
  })

  writeAssetLock(options.lockFile, withAsset(lock, digest, assetID))
  return { assetID, digest, uploaded: true }
}

/**
 * 按扩展名判一个标准媒体类型。
 *
 * **这不是 aladdin 的素材白名单**——那份白名单在服务端，且它是权威
 * （`internal/galaxy/asset.go`）。这里给的只是"这个扩展名对应哪个通用媒体类型"
 * 这一件与平台无关的事，免得调用方为一张 `.png` 手写一遍 `image/png`。
 * 判不出来时回落到中性的 `application/octet-stream`，由服务端如实拒绝。
 */
export function mediaTypeOf(file: string): string {
  const ext = extname(file).toLowerCase()
  return MEDIA_TYPES[ext] ?? 'application/octet-stream'
}

const MEDIA_TYPES: Record<string, string> = {
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.gif': 'image/gif',
  '.webp': 'image/webp',
  '.avif': 'image/avif',
  '.svg': 'image/svg+xml',
  '.mp4': 'video/mp4',
  '.webm': 'video/webm',
  '.mov': 'video/quicktime',
  '.mp3': 'audio/mpeg',
  '.wav': 'audio/wav',
  '.ogg': 'audio/ogg',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2',
  '.ttf': 'font/ttf',
  '.otf': 'font/otf',
}
