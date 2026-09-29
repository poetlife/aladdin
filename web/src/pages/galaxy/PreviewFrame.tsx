import { theme } from 'antd'

interface PreviewFrameProps {
  /**
   * 待预览的 HTML。**它由服务端渲染**（见 api/galaxy.ts 的 previewDraft）：
   * `docs` 形态的 markdown 渲染、`asset://` 记号到短时地址的替换都发生在那里，
   * 与发布共用同一段实现。
   */
  html: string
  /**
   * 预览区高度。数字按像素，也可给 `'100%'` 让它撑满所在面板。
   * 窄屏下高度不随宽度变，因此不按断点调整。
   */
  height?: number | string
}

/**
 * 沙箱 iframe 预览。
 *
 * **关键约束：沙箱属性是 `allow-scripts`，不含 `allow-same-origin`。**
 * 这让 iframe 落在不透明源上——内容里的脚本即使跑起来也读不到编辑器的
 * DOM、会话与本地存储，它连自己的源都没有（见 docs/design/galaxy/authoring.md）。
 * "同源 + 沙箱"不是可接受的替代：同源意味着脚本与编辑器共享 origin，隔离就
 * 只剩一层属性开关，任何一次属性调整都会变成一次真实的数据泄露。
 *
 * **代价是站内引用在预览里解析不到**（沙箱文档没有自己的源）：`docs` 形态因为
 * 整站被拼成一份、用文内锚点导航，页间跳转可用；`static` 只能逐页预览，页内的
 * 站内导航在预览里不可用。
 *
 * 预览**不做审查、不做裁剪**：内容写什么就渲染什么，坏引用就显示坏的。"这处有
 * 问题"由校验入口单独给出——把两者混起来会让用户以为预览看起来对就等于发布能
 * 成功。
 */
export function PreviewFrame({ html, height = 420 }: PreviewFrameProps): React.ReactNode {
  const { token } = theme.useToken()

  return (
    <iframe
      title="预览"
      // 逐字写死沙箱属性：不得出现 allow-same-origin，见上方注释。
      sandbox="allow-scripts"
      srcDoc={html}
      style={{
        width: '100%',
        height,
        border: `1px solid ${token.colorBorderSecondary}`,
        borderRadius: token.borderRadius,
        // 不给内容注入任何样式（见 spec）：底色交给浏览器默认，编辑器不参与。
        display: 'block',
      }}
    />
  )
}
