import { isAbsolute, resolve as resolvePath, dirname } from 'node:path'

import { digestOfFile } from '../lock/asset-lock.js'
import type { AssetLock } from '../lock/asset-lock.js'
import { siteURL } from './publish-root.js'

/**
 * **一处引用解析成什么**：源里写的那个标准写法（`./logo.png`）对应文件组里的
 * 哪一条资产条目、以及它在站点内的那条地址。
 *
 * 这是本包在构建期做的那一件事，因此它只有这一处实现：vite 插件（把它换掉）
 * 与调用面（据此写出条目）都调它，两处各写一份的表现是"页面上那条地址与文件组
 * 里那条条目对不上"——而它只在发布被拒的时候才看得出来。
 *
 * **它不判定任何事。** "这份内容能不能发布"只有服务端一处入口（见
 * docs/design/galaxy/README.md 的"引用完整性的唯一入口"）；这里回答的只是
 * "这份字节我认不认得"，答不上来就交给构建器自己处理。
 */

/**
 * 资产条目在文件组里的默认路径前缀。
 *
 * 与服务端把 `asset://<资产标识>` 登记成条目时用的前缀是同一个取值——同一个
 * 资产在两条路上因此落在同一个地址上，而不是各有一个。
 */
export const DEFAULT_ASSET_PATH_PREFIX = 'assets/'

export interface AssetReferenceOptions {
  /** 工程的根目录：源里以 `/` 开头的引用按它解析，与构建器的解析规则一致。 */
  projectRoot: string
  /** 发布根（见 publish-root.ts），用来拼出站点内的那条地址。 */
  publishRoot: string
  /** 本地资产记录（见 lock/asset-lock.ts）。 */
  lock: AssetLock
  /** 资产条目的路径前缀，默认 `assets/`。 */
  assetPathPrefix?: string
}

/** 一处解析出来的资产引用。 */
export interface ResolvedAsset {
  /** 文件组里的条目路径。 */
  path: string
  /** 它在站点内的那条地址（发布根之下的绝对地址）。 */
  url: string
  /** 资产标识。 */
  assetID: string
}

/** 资产条目的路径：由资产标识派生，只有这一处。 */
export function assetEntryPath(assetID: string, prefix: string = DEFAULT_ASSET_PATH_PREFIX): string {
  const normalized = prefix.endsWith('/') ? prefix : `${prefix}/`
  return `${normalized}${assetID}`
}

/**
 * 把源里的一处引用解析成一条资产条目。
 *
 * 认不出来的一律返回 `null`（裸包名、平台之外的地址、记录里没有的文件），
 * 由调用方决定怎么处理——**不在这里猜，也不在这里报错**：构建期要报的那个错
 * 属于构建器自己的解析（引用一份不存在的文件时它本来就会失败），
 * 而"这份文件还没送进资产库"是构建之后那一步的事。
 */
export class AssetReferenceResolver {
  readonly #projectRoot: string
  readonly #publishRoot: string
  readonly #lock: AssetLock
  readonly #prefix: string
  readonly #cache = new Map<string, ResolvedAsset | null>()

  constructor(options: AssetReferenceOptions) {
    this.#projectRoot = resolvePath(options.projectRoot)
    this.#publishRoot = options.publishRoot
    this.#lock = options.lock
    this.#prefix = options.assetPathPrefix ?? DEFAULT_ASSET_PATH_PREFIX
  }

  /** 解析源里的一处引用；`importer` 是写下这处引用的那一份文件的路径。 */
  resolve(specifier: string, importer: string): ResolvedAsset | null {
    const file = this.#specifierToFile(specifier, importer)
    if (file === null) {
      return null
    }
    return this.resolveFile(file)
  }

  /** 解析一份本地文件。同一份文件解析一次即缓存：摘要要读一遍字节。 */
  resolveFile(file: string): ResolvedAsset | null {
    const absolute = isAbsolute(file) ? file : resolvePath(this.#projectRoot, file)
    const cached = this.#cache.get(absolute)
    if (cached !== undefined) {
      return cached
    }
    const resolved = this.#resolveUncached(absolute)
    this.#cache.set(absolute, resolved)
    return resolved
  }

  #resolveUncached(file: string): ResolvedAsset | null {
    let digest: string
    try {
      digest = digestOfFile(file)
    } catch {
      // 读不到就是认不出来——**这里不是报错的地方**。引用一份不存在的文件时，
      // 构建器自己的解析会失败并指出是哪一份，那是更准确的那条信息。
      return null
    }
    const assetID = this.#lock.assets[digest]
    if (assetID === undefined) {
      return null
    }
    const path = assetEntryPath(assetID, this.#prefix)
    return { path, url: siteURL(this.#publishRoot, path), assetID }
  }

  /**
   * 一处引用指向哪个本地文件。认不出来的返回 `null`。
   *
   * 只认"相对路径"与"以 `/` 开头的站点路径"这两类——与构建器自己的解析规则
   * 一致。裸包名（`react`）与带协议的地址（`https://…`、`data:`）不是本地文件，
   * 因此不在这里处理：它们本来就不是素材引用。
   */
  #specifierToFile(specifier: string, importer: string): string | null {
    const bare = stripQueryAndHash(specifier)
    if (bare === '' || bare === '.' || bare === '..') {
      return null
    }
    if (/^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(bare)) {
      return null
    }
    if (bare.startsWith('/')) {
      return resolvePath(this.#projectRoot, bare.slice(1))
    }
    if (bare.startsWith('./') || bare.startsWith('../')) {
      return resolvePath(dirname(importer), bare)
    }
    return null
  }
}

/**
 * 去掉构建器加在引用尾巴上的查询串与 fragment（`./logo.png?url`、`#hash`）。
 *
 * 素材是不是本工程的那一份与这些后缀无关：它们是构建器对同一份文件的几种取用
 * 方式，而取到的都是同一份字节、算出的都是同一个摘要。
 */
export function stripQueryAndHash(specifier: string): string {
  const cut = Math.min(
    ...[specifier.indexOf('?'), specifier.indexOf('#')].filter((index) => index >= 0),
  )
  return Number.isFinite(cut) ? specifier.slice(0, cut) : specifier
}
