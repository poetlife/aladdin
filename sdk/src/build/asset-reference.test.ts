import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

import { DEFAULT_ASSET_PATH_PREFIX, assetEntryPath } from './asset-reference.js'

/**
 * 默认条目路径的漂移闸门。
 *
 * 同一个资产在两条路上会各生成一条条目路径：服务端把 `asset://<资产标识>`
 * 登记成条目时用一个前缀，SDK 把标准写法解析成条目时用另一个常量。**两处必须
 * 同值**，否则同一个资产在两个槽里有两个地址（见 docs/ssot-registry.md）。
 *
 * 因此这里直接读服务端那一份的源码文本，把取值钉在一起。文件不在（这个包被
 * 单独安装到别处时）就跳过：这是仓库内的闸门，不是运行时约束。
 */
describe('资产条目的默认路径前缀与服务端同源', () => {
  it('与服务端把记号登记成条目时用的前缀相同', () => {
    const path = fileURLToPath(new URL('../../../internal/galaxy/placeholder.go', import.meta.url))
    if (!existsSync(path)) {
      return
    }
    const text = readFileSync(path, 'utf8')
    const declared = /assetMarkerPathPrefix\s*=\s*"([^"]+)"/.exec(text)?.[1]
    expect(declared).toBe(DEFAULT_ASSET_PATH_PREFIX)
  })

  it('前缀结尾一定带一个斜杠，少一个就是一条拼错的地址', () => {
    expect(assetEntryPath('ast_1')).toBe(`${DEFAULT_ASSET_PATH_PREFIX}ast_1`)
    expect(assetEntryPath('ast_1', 'media')).toBe('media/ast_1')
    expect(assetEntryPath('ast_1', 'media/')).toBe('media/ast_1')
  })
})
