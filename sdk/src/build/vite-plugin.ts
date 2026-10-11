import { extname, resolve as resolvePath } from 'node:path'

import type { Plugin } from 'vite'

import { AssetReferenceResolver, stripQueryAndHash } from './asset-reference.js'
import { normalizePublishRoot, siteURL } from './publish-root.js'
import { ASSET_LOCK_FILE, readAssetLock } from '../lock/asset-lock.js'

/**
 * 构建期插件：把源里的**标准写法**解析成文件组条目与站点内地址，并把发布根
 * 交给构建器。
 *
 * 三条它不做的事，与 docs/design/galaxy/sdk.md 的三条红线一一对应：
 *
 *   - **不联网。** 它只读本地的那份资产记录（lock），不取发布根、不上传素材、
 *     不问校验结论。发布根由调用方给出。
 *   - **不往产物里注入任何平台的字节。** 它只替换地址文本，不改内容的形状、
 *     不加脚本、不加样式。
 *   - **不判定。** "这份内容能不能发布"只有服务端一处入口。认不出来的引用一律
 *     原样留给构建器自己处理——引用一份不存在的文件时，构建器自己的解析会失败
 *     并指出是哪一份，那是比这里更准确的信息。
 *
 * 产物由**用户的构建**产出：插件是挂在他构建上的一个环节，不是替他构建。
 */

/** 插件名。构建输出里用它标识本插件。 */
export const PLUGIN_NAME = 'aladdin:galaxy-build'

export interface GalaxyBuildOptions {
  /**
   * 发布根，形如 `https://<发布域>/g/<工程标识>/`。
   *
   * **由调用方给出**（调用面从服务端取，见 build/publish-root.ts），插件不自己拼、
   * 也不去取。它同时是构建器的 `base`：产物里的绝对路径要落在它下面。
   */
  publishRoot: string
  /** 工程根目录。默认取构建器解析出来的 root。 */
  projectRoot?: string
  /** 本地资产记录的路径。默认是工程根下的 `aladdin.assets.json`。 */
  lockFile?: string
  /** 资产条目的路径前缀，默认 `assets/`（见 asset-reference.ts）。 */
  assetPathPrefix?: string
}

/** 我们接管的模块 id 前缀。带 `\0` 的 id 是构建器约定："这是插件造出来的，不是文件"。 */
const VIRTUAL_PREFIX = '\0aladdin:asset:'

export function galaxyBuild(options: GalaxyBuildOptions): Plugin {
  const publishRoot = normalizePublishRoot(options.publishRoot)

  let root = options.projectRoot === undefined ? process.cwd() : resolvePath(options.projectRoot)
  /** 构建开始时装配一次：记录只在那一刻读，构建中途不再回读。 */
  let resolver: AssetReferenceResolver | null = null

  const ensureResolver = (): AssetReferenceResolver => {
    if (resolver !== null) {
      return resolver
    }
    const lockFile = options.lockFile ?? resolvePath(root, ASSET_LOCK_FILE)
    resolver = new AssetReferenceResolver({
      projectRoot: root,
      publishRoot,
      lock: readAssetLock(lockFile),
      ...(options.assetPathPrefix === undefined ? {} : { assetPathPrefix: options.assetPathPrefix }),
    })
    return resolver
  }

  return {
    name: PLUGIN_NAME,
    // **必须在构建器自己的素材处理之前**：晚一步，素材已经被它复制进产物了，
    // 而那份字节不该出现在产物里——它应该由平台上的那一份来提供。
    enforce: 'pre',

    config(userConfig) {
      // 发布根与 base 是同一件事：产物里的绝对路径要落在发布根下面。用户自己
      // 设了一个不同的 base 时**如实报错**而不是悄悄覆盖——那意味着他以为产物
      // 会落在别处，而发布出去以后每一条绝对路径都是错的。
      if (isRealBase(userConfig.base)) {
        const given = normalizePublishRoot(userConfig.base)
        if (given !== publishRoot) {
          throw new Error(
            `${PLUGIN_NAME}: 构建器已设了 base=${userConfig.base}，与发布根 ${publishRoot} 不一致。` +
              `产物里的绝对路径只能落在发布根下面，请去掉 base，或用同一个值。`,
          )
        }
      }
      return { base: publishRoot }
    },

    configResolved(config) {
      // root 的权威在这里，不在 userConfig 里：构建器可能从配置文件或命令行
      // 给一个别处的 root，而它决定资产记录在哪一份目录下。用户显式给了
      // projectRoot 时以他给的为准——那是"构建目录与工程目录不是同一个"的情形。
      if (options.projectRoot === undefined) {
        root = config.root
      }
      // 装配放在这里而不是构造函数里：这一步之后 root 才是最终的。
      resolver = null
    },

    // 一份被引用的文件是本工程的一份资产时，这个模块的"导出"就是它在站点内的
    // 那条地址——而不是它的字节。字节由平台上的那一份提供，产物里不带素材。
    resolveId(source, importer) {
      if (importer === undefined || importer.startsWith('\0')) {
        return null
      }
      const found = ensureResolver().resolve(source, importer)
      return found === null ? null : `${VIRTUAL_PREFIX}${found.path}`
    },

    load(id) {
      if (!id.startsWith(VIRTUAL_PREFIX)) {
        return null
      }
      const url = siteURL(publishRoot, id.slice(VIRTUAL_PREFIX.length))
      return `export default ${JSON.stringify(url)}`
    },

    /**
     * HTML 与 CSS 里的引用。
     *
     * **按"这个值是不是本工程的一份资产"替换，不按属性名枚举。** 枚举属性名
     * 必然会漏（`srcset`、内联 CSS、`poster`、以后新增的属性），而漏掉的那一处
     * 不会被任何人发现——它表现为一张裂图。判据因此落在值上：一段看起来像本地
     * 路径的文本，解析出来确实是本工程记录里的一份资产，就换掉。
     *
     * 换掉的是一条**绝对地址**，构建器因此不会再拿它去取本地文件。
     */
    transform(code, id) {
      const bare = stripQueryAndHash(id)
      const ext = extname(bare)
      if (ext !== '.html' && ext !== '.css') {
        return null
      }
      const rewritten = rewriteAssetReferences(code, (specifier) =>
        ensureResolver().resolve(specifier, bare),
      )
      return rewritten === code ? null : { code: rewritten, map: null }
    },
  }
}

/** 用户设的 base 是不是一个"真的地址"，而不是构建器的缺省值。 */
function isRealBase(base: unknown): base is string {
  return typeof base === 'string' && base !== '' && base !== './' && base !== '/'
}

/**
 * 把一段 HTML 或 CSS 里所有"看起来像本地路径、且确实是本工程的一份资产"的
 * 文本换成它在站点内的地址。
 *
 * 只认相对路径（`./`、`../`）与以 `/` 开头的站点路径这两类：别的写法不是指向
 * 本工程的文件（裸包名、带协议的地址、`data:`），本来就不该在这里被处理。
 * 认不出来的一律**原样留下**——那些位置由构建器自己处理。
 */
export function rewriteAssetReferences(
  code: string,
  resolveReference: (specifier: string) => { url: string } | null,
): string {
  const pattern = /(?:\.{1,2}\/|\/)[^\s"'`()<>\\]*/g
  const cache = new Map<string, string | null>()
  return code.replace(pattern, (match) => {
    const cached = cache.get(match)
    if (cached !== undefined) {
      return cached ?? match
    }
    const found = resolveReference(match)
    cache.set(match, found === null ? null : found.url)
    return found === null ? match : found.url
  })
}
