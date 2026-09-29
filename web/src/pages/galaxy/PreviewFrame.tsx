import { theme } from 'antd'

interface PreviewFrameProps {
  /**
   * 要预览的地址：**草稿整站在发布域上的入口**，带短时凭证。
   *
   * 它由服务端给出（见 api/galaxy.ts 的 previewDraft 与 docs/design/galaxy/
   * site-model.md 的"预览"）：客户端不拼这条地址，也不往里塞任何字节。地址为空表示
   * "还没有可预览的东西"（草稿为空），由调用方呈现空态——不要把它交给 iframe。
   */
  url: string
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
 * **它加载的是一条真实的地址（`src`），不是塞进来的一份 HTML（`srcDoc`）。**
 * 这是页面能取到自己那些文件的前提：一份 `srcdoc` 文档没有地址，页内的相对地址
 * 与站点绝对地址都没有可解析的基准——预览于是"有样式等于碰巧、图片全裂"。地址
 * 落在**发布域**上（因此与主应用不同源，隔离不受影响）。
 *
 * 预览**不做审查、不做裁剪**：内容写什么就渲染什么，坏引用就显示坏的。"这处有
 * 问题"由校验入口单独给出——把两者混起来会让用户以为预览看起来对就等于发布能
 * 成功。
 */
export function PreviewFrame({ url, height = 420 }: PreviewFrameProps): React.ReactNode {
  const { token } = theme.useToken()

  return (
    <iframe
      title="预览"
      // 逐字写死沙箱属性：不得出现 allow-same-origin，见上方注释。
      sandbox="allow-scripts"
      src={url}
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
