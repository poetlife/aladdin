import { theme } from 'antd'

import type { Asset } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { substituteAssetPlaceholders } from './preview-content'

interface PreviewFrameProps {
  /** 待预览的正文（草稿当前内容）。 */
  content: string
  /** 本工程的资产，用于把占位符换成短时地址。 */
  assets: readonly Asset[]
  /** 预览区高度。窄屏下高度不随宽度变，因此不按断点调整。 */
  height?: number
}

/**
 * 沙箱 iframe 预览。
 *
 * **关键约束：沙箱属性是 `allow-scripts`，不含 `allow-same-origin`。**
 * 这让 iframe 落在不透明源上——正文里的脚本即使跑起来也读不到编辑器的
 * DOM、会话与本地存储，它连自己的源都没有（见 docs/design/galaxy/authoring.md）。
 * "同源 + 沙箱"不是可接受的替代：同源意味着脚本与编辑器共享 origin，隔离就
 * 只剩一层属性开关，任何一次属性调整都会变成一次真实的数据泄露。
 *
 * 预览**不做审查、不做裁剪**：正文写什么就渲染什么，坏引用就显示坏的。
 * 占位符到地址的替换只在预览这一侧发生，且取不到地址时原样保留。
 */
export function PreviewFrame({ content, assets, height = 420 }: PreviewFrameProps): React.ReactNode {
  const { token } = theme.useToken()

  return (
    <iframe
      title="预览"
      // 逐字写死沙箱属性：不得出现 allow-same-origin，见上方注释。
      sandbox="allow-scripts"
      srcDoc={substituteAssetPlaceholders(content, assets)}
      style={{
        width: '100%',
        height,
        border: `1px solid ${token.colorBorderSecondary}`,
        borderRadius: token.borderRadius,
        // 不给正文注入任何样式（见 spec）：底色交给浏览器默认，编辑器不参与。
        display: 'block',
      }}
    />
  )
}
