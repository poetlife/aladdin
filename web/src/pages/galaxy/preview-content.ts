import type { Asset } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'

/**
 * 资产占位符：`asset://<资产标识>`。
 *
 * 它是正文里引用素材的唯一记号（见 docs/design/galaxy/authoring.md）。
 * 标识由服务端分配，这里用一个保守的字符集去识别它。
 */
const ASSET_PLACEHOLDER = /asset:\/\/([A-Za-z0-9_-]+)/g

/**
 * 把正文里的资产占位符替换成编辑态的短时地址，**只用于预览**。
 *
 * 三条边界，缺一条就会走偏：
 *
 * 1. **判断正文能不能发布以服务端 ValidateContent 为唯一入口。**
 *    这里只做预览用的文本替换，不做任何引用解析——正文里哪些是合法引用、
 *    哪些该拒绝，不是这个模块回答的问题（见 docs/design/galaxy/README.md
 *    的"引用完整性的唯一入口"）。
 * 2. **取不到地址就原样留着**，而不是删掉或换成占位图：预览不裁剪正文
 *    （见 docs/design/galaxy/authoring.md），坏引用就该在预览里显式坏掉。
 * 3. **逐字替换，不解析 HTML**：占位符出现在属性、内联 CSS 的 `url()`、
 *    `srcset` 里都成立，枚举"取资源的位置"必然会漏。
 */
export function substituteAssetPlaceholders(content: string, assets: readonly Asset[]): string {
  const urlById = new Map<string, string>()
  for (const asset of assets) {
    // 地址为空表示未配置私有桶（或该资产的地址尚未签发）：那等同于取不到。
    if (asset.url !== '') {
      urlById.set(asset.id, asset.url)
    }
  }

  return content.replace(ASSET_PLACEHOLDER, (match, id: string) => urlById.get(id) ?? match)
}
