import { readdir, readFile } from 'node:fs/promises'
import { join, relative, resolve, sep } from 'node:path'

import type { Client } from '@connectrpc/connect'
import { create } from '@bufbuild/protobuf'

import {
  ContentSlot,
  FileEntrySchema,
  type Draft,
  type FileEntry,
  type GalaxyService,
} from '../gen/proto/aladdin/galaxy/v1/galaxy_pb.js'
import { contentDigest } from '../content-digest.js'
import { readAssetLock } from '../lock/asset-lock.js'
import { directUpload } from './direct-upload.js'

/**
 * 调用面：内容与发布那几步的薄封装。
 *
 * **它不替用户决定发的是哪一版。** 版本模型是用户可见的：一次发布发的是某一版，
 * 而不是"文件现在的样子"。收成一条"一路走到底"的调用，会让"我这次发的是哪一版"
 * 变成一个只有读这个封装的实现才能回答的问题——与命令行那条"只有原子命令"是
 * 同一条理由（见 docs/design/galaxy/cli.md）。
 *
 * 组合由**用户自己的构建脚本**决定，那是命令行那条边界明确认可的出路。
 */

/** 一次推送里的一份文件怎么进文件组。 */
export interface PushDraftOptions {
  projectId: string
  slot: ContentSlot
  /** 要送上去的那个目录（通常是构建产物目录）。 */
  directory: string
  /** 本地资产记录的路径。记录里有这份字节时，它进文件组的是**资产条目**。 */
  lockFile: string
}

/**
 * 把一个目录整组替换成草稿。
 *
 * 两件事在这里合流，而它们的判据只有一条——**这份字节在不在本地资产记录里**：
 *
 *   - 在记录里 ⇒ 它是一条**资产条目**（引用资产库里那一份，字节由平台提供）；
 *   - 不在 ⇒ 它是一条**文本条目**（字节按内容摘要写进内容对象）。
 *
 * 与构建期插件用的是同一条判据，因此"插件换掉的那条地址"与"这里写出的那条
 * 条目"必然对得上。两处各判一份的表现是页面上有一条地址、而文件组里没有对应
 * 的条目——它只在发布被拒时才看得出来。
 *
 * **整组替换，不是增量**：清单里没有的路径就是"删掉"。它表达的是期望的完整
 * 状态，否则"我到底删没删掉那一份"会变成一个需要读本实现才能回答的问题。
 */
export async function pushDraft(
  galaxy: Client<typeof GalaxyService>,
  options: PushDraftOptions,
): Promise<Draft> {
  const lock = readAssetLock(options.lockFile)
  // **记录文件本身不是内容。** 它可能就落在要送上去的那个目录里（把工程的根
  // 目录直接送上去时就会这样），而它是 SDK 的账本、不是用户写下的东西——把它
  // 发布出去既没有用处，也把"这一版引用了哪些素材"摊在了公开地址上。
  const excluded = resolve(options.lockFile)
  const files = (await listFiles(options.directory)).filter((file) => file.absolute !== excluded)
  const entries: FileEntry[] = []

  for (const file of files) {
    const bytes = await readFile(file.absolute)
    const digest = contentDigest(bytes)
    const path = file.relative

    const assetID = lock.assets[digest]
    if (assetID !== undefined) {
      entries.push(create(FileEntrySchema, { path, source: { case: 'assetId', value: assetID } }))
      continue
    }

    await uploadContentObject(galaxy, options.projectId, digest, bytes)
    entries.push(create(FileEntrySchema, { path, source: { case: 'digest', value: digest } }))
  }

  const pushed = await galaxy.pushDraft({
    projectId: options.projectId,
    slot: options.slot,
    entries,
  })
  if (pushed.draft === undefined) {
    throw new Error('服务端没有返回推送之后的草稿')
  }
  return pushed.draft
}

/**
 * 把一份文本条目的字节写进内容对象。
 *
 * **按内容摘要寻址、仅当不存在时写入**：那一份字节可能正被另一个版本用着，
 * 覆盖它就是改写别人的内容——那是"版本不可变"的反面。因此服务端先告诉我们
 * 它是不是已经有了，写的时候还要带上禁止覆盖的条件，由存储侧执行。
 */
async function uploadContentObject(
  galaxy: Client<typeof GalaxyService>,
  projectId: string,
  digest: string,
  bytes: Uint8Array,
): Promise<void> {
  const begun = await galaxy.beginContentUpload({
    projectId,
    digest,
    sizeBytes: BigInt(bytes.byteLength),
  })
  if (!begun.alreadyExists) {
    if (begun.upload === undefined) {
      throw new Error(`服务端没有为 ${digest} 下发直传凭证`)
    }
    await directUpload(begun.upload, bytes, 'application/octet-stream')
    await galaxy.commitContentUpload({ projectId, digest })
  }
}

/** 把某个槽当前的草稿存成一个不可变版本。 */
export async function saveVersion(
  galaxy: Client<typeof GalaxyService>,
  options: { projectId: string; slot: ContentSlot; description?: string },
) {
  const saved = await galaxy.saveVersion({
    projectId: options.projectId,
    slot: options.slot,
    description: options.description ?? '',
    fromSnapshotId: '',
  })
  if (saved.version === undefined) {
    throw new Error('服务端没有返回保存后的版本')
  }
  return saved.version
}

/**
 * 发布**某一个版本**。`versionId` 必填——没有"发布当前草稿"这种形状。
 *
 * 这是唯一一个让内容离开私有边界的动作，因此它单独一步，不与推送或存版本串
 * 在一起（见 docs/design/galaxy/cli.md）。
 */
export async function publish(
  galaxy: Client<typeof GalaxyService>,
  options: { projectId: string; slot: ContentSlot; versionId: string },
) {
  return galaxy.publish(options)
}

/** 校验某个槽当前的草稿能不能发布。**这是判定唯一那一处入口的客户端侧调用。** */
export async function validateDraft(
  galaxy: Client<typeof GalaxyService>,
  options: { projectId: string; slot: ContentSlot },
) {
  return galaxy.validateDraft(options)
}

/**
 * 读一个槽的**发布根**（构建时注入，形如 `https://<发布域>/g/<工程标识>/`）。
 *
 * 它是构建期插件要的那个值，而 SDK **只有这一处**能拿到它：发布根在服务端
 * 只有一处派生入口，命令行也是从那里取的。因此取它是一次**联网的、独立于构建
 * 的动作**——调用方先把它取到手，再交给构建（见 build/publish-root.ts）。
 */
export async function publishRoot(
  galaxy: Client<typeof GalaxyService>,
  options: { projectId: string; slot: ContentSlot },
): Promise<string> {
  const response = await galaxy.getProject({ projectId: options.projectId })
  const slotState = response.project?.slots.find((candidate) => candidate.slot === options.slot)
  const baseUrl = slotState?.baseUrl ?? ''
  if (baseUrl === '') {
    throw new Error(
      `工程 ${options.projectId} 的这个内容槽没有发布根：它要么没有这个槽，要么这个部署没有配置发布域`,
    )
  }
  return baseUrl
}

interface ListedFile {
  /** 相对目录的路径，用 `/` 分隔：它就是文件组里的条目路径。 */
  relative: string
  absolute: string
}

/** 列出一个目录下的全部文件。条目路径一律用 `/` 分隔，与文件组的路径形状一致。 */
async function listFiles(directory: string): Promise<ListedFile[]> {
  const found = await readdir(directory, { recursive: true, withFileTypes: true })
  const files: ListedFile[] = []
  for (const entry of found) {
    if (!entry.isFile()) {
      continue
    }
    const absolute = join(entry.parentPath, entry.name)
    files.push({ relative: relative(directory, absolute).split(sep).join('/'), absolute })
  }
  // 按路径排序，好让"哪一份先传"这件事在两次运行之间是确定的：清单本身的顺序
  // 由服务端决定，但**本地这一侧的一串网络动作**没有必要每次换一个次序。
  files.sort((left, right) => (left.relative < right.relative ? -1 : left.relative > right.relative ? 1 : 0))
  return files
}
