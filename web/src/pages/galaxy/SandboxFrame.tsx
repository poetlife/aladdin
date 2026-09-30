import { theme } from 'antd'

interface SandboxFrameProps {
  /**
   * 要加载的地址：**用户内容整站的入口**，落在发布域上。
   *
   * 预览走带短时凭证的草稿通道，发布后的壳走同路径的匿名地址——两条都由服务端
   * 给出（见 api/galaxy.ts 与 docs/design/galaxy/publication.md 的"主站壳"）：
   * 客户端不拼这条地址，也不往里塞任何字节。地址为空表示"还没有可加载的东西"，
   * 由调用方呈现空态——不要把它交给 iframe。
   */
  url: string
  /**
   * 无标题 iframe 对无障碍树不可见，因此标题是必填的：读屏软件靠它区分页面上
   * 有几个 frame、各是什么。
   */
  title: string
  /**
   * 高度。数字按像素，也可给 `'100%'` 让它撑满所在面板。
   * 窄屏下高度不随宽度变，因此不按断点调整。
   */
  height?: number | string
}

/**
 * 交付用户内容的沙箱 iframe（**沙箱属性唯一的一处定义**）。
 *
 * 两个调用点（工作台的预览、主站的发布壳）共用它。两处各写一份的表现是"某一处
 * 悄悄多了 `allow-same-origin`"，而那条属性一旦出现，隔离就只剩一层开关了——
 * 因此这里逐字写死，参数里也不留可以传沙箱属性的口子。
 *
 * **关键约束：沙箱属性是 `allow-scripts`，不含 `allow-same-origin`。**
 * 这让 iframe 落在不透明源上——内容里的脚本即使跑起来也读不到主站的 DOM、会话与
 * 本地存储，它连自己的源都没有（见 docs/design/galaxy/authoring.md）。"同源 +
 * 沙箱"不是可接受的替代：同源意味着脚本与主站共享 origin，隔离就只剩一层属性
 * 开关，任何一次属性调整都会变成一次真实的数据泄露。
 *
 * **它加载的是一条真实的地址（`src`），不是塞进来的一份 HTML（`srcDoc`）。**
 * 这是页面能取到自己那些文件的前提：一份 `srcdoc` 文档没有地址，页内的相对地址
 * 与站点绝对地址都没有可解析的基准——于是"有样式等于碰巧、图片全裂"。地址落在
 * **发布域**上，因此与主应用不同源，隔离不受影响。
 */
export function SandboxFrame({ url, title, height = 420 }: SandboxFrameProps): React.ReactNode {
  const { token } = theme.useToken()

  return (
    <iframe
      title={title}
      // 逐字写死沙箱属性：不得出现 allow-same-origin，见上方注释。
      sandbox="allow-scripts"
      src={url}
      style={{
        width: '100%',
        height,
        border: `1px solid ${token.colorBorderSecondary}`,
        borderRadius: token.borderRadius,
        // 不给内容注入任何样式（见 spec）：底色交给浏览器默认，主站不参与。
        display: 'block',
      }}
    />
  )
}
