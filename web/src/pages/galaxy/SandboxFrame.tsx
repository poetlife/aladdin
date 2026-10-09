import { useEffect, useRef, useState } from 'react'
import { Button, Flex, Spin, Typography, theme } from 'antd'

import { LoadingHint } from '../../ui/LoadingHint'
import { parseFrameImagePreview, type FrameImagePreview } from './frame-channel'
import { FrameImageLightbox } from './FrameImageLightbox'

/**
 * 铺在内容上的遮罩撤到哪一刻。
 *
 * 撤的是**遮罩**而不是"等待"：`load` 要等文档连同它的全部子资源都取完，而正文与排版
 * 通常早就渲染出来了。在慢速网络里为一张大图继续盖着不透明的遮罩，挡住的恰恰是已经
 * 能看的内容——比空着更糟。因此到点就撤，只留一条不遮挡正文的状态条（见
 * docs/design/uiux/README.md 的"空态、加载与失败"）。
 */
const COVER_TIMEOUT_MS = 10_000

/** 状态条上那句话里的秒数由上面那个量级派生，两处不会各写各的。 */
const COVER_TIMEOUT_SECONDS = Math.round(COVER_TIMEOUT_MS / 1000)

/** 内容到哪一步了：还在遮着、到点撤了遮罩、还是已经到齐。 */
type FrameLoad = 'covering' | 'overdue' | 'loaded'

/**
 * 这一帧**这一次尝试**的结论。`covering` 是过程，不是结论，因此不在其中。
 *
 * 两个取值都要报给宿主，不能只报 `loaded`：只报到齐的话，"最慢的那一次加载"反而
 * 永远等不到回调，而它恰恰是唯一值得看的那一条（见 SandboxFrameProps.onSettled）。
 */
export type FrameSettled = Exclude<FrameLoad, 'covering'>

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
  /**
   * 这一帧这一次尝试有了结论时调用一次：`loaded` 到齐了、`overdue` 到点仍未到齐。
   * 省略时不回调。
   *
   * **宿主需要它是因为只有这里知道那一刻**：`load` 是帧自己的事件，而宿主侧
   * "什么时候把地址交出去"与它不是同一件事。遥测用它量"打开这一步用了多久"
   * （见 docs/observability.md 的「客户端事件」）——而那个耗时是这一页上唯一
   * 只有客户端答得了的量。
   *
   * 换地址或点了重试都会**重新计一次**：那是新的一次尝试，结论也该是新的。
   */
  onSettled?: (outcome: FrameSettled) => void
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
 *
 * **它同时是接入桥的宿主。** 文档页里点了图会发来一条消息，这一处收下并交给宿主侧
 * 的灯箱（见 frame-channel.ts）。**灯箱画在宿主页面上，不在 iframe 里**：只有落在
 * 宿主上才盖得住整个视口。协议与边界见 docs/design/galaxy/site-model.md 的
 * "平台接入桥"。
 *
 * **它还负责这一块的加载态**，因为只有它知道这一帧什么时候到（`load`）。规则与理由
 * 见 docs/design/galaxy/publication.md 的"主站壳"：`load` 之前铺一层遮罩，超过
 * `COVER_TIMEOUT_MS` 仍未 `load` 就撤掉遮罩、只留一条可重试的状态条。**加载态与沙箱
 * 属性一样只有这一处**——预览与壳各写一份的表现是"壳里补上了、工作台里的预览仍然
 * 白着"。
 */
export function SandboxFrame({ url, title, height = 420, onSettled }: SandboxFrameProps): React.ReactNode {
  const { token } = theme.useToken()
  const frame = useRef<HTMLIFrameElement>(null)
  const [preview, setPreview] = useState<FrameImagePreview | null>(null)
  const [load, setLoad] = useState<FrameLoad>('covering')
  // 重试要的是"这一帧再取一次"，而地址一个字都没变——只改 `src` 浏览器不会动。
  // 把重试次数交给 `key`，帧因此重挂、重新发一次请求。
  const [attempt, setAttempt] = useState(0)

  // 这一次尝试是否已经报过结论，以及报的是哪一条。
  //
  // 它挡的是"同一次尝试报两遍"：`overdue` 之后帧仍可能到齐（那时状态从 overdue
  // 走到 loaded），而宿主拿它量的是"等了多久"，报两遍会让一条动作变成两条事件。
  // **第一个结论为准**，后面的不再改写——这也是 `load` 状态本来的语义。
  const settled = useRef<FrameSettled | null>(null)
  // 回调放进 ref：它常常是宿主每次渲染新建的箭头函数，而下面那个函数不该因为
  // 宿主重渲染就拿到另一个版本。
  const settledCallback = useRef(onSettled)
  settledCallback.current = onSettled

  /**
   * 记下这一次尝试的结论并通知宿主一次。
   *
   * 它**不放在 effect 里**：`load` 是这一次尝试的状态，而地址一变它就是上一轮的
   * 值——按 `load` 变化触发的 effect 会在换地址后的第一帧就把上一轮的 `loaded`
   * 当成新一轮的结论报出去。结论由"谁让它结束的"直接给出，中间不经过状态。
   */
  const settle = (outcome: FrameSettled): void => {
    if (settled.current !== null) {
      return
    }
    settled.current = outcome
    setLoad(outcome)
    settledCallback.current?.(outcome)
  }

  // 地址换了或用户点了重试，都从头等起。计时器到点时若已经有结论，这一档就
  // 不再改写（结论是第一个说了算）。
  useEffect(() => {
    settled.current = null
    setLoad('covering')
    const timer = window.setTimeout(() => settle('overdue'), COVER_TIMEOUT_MS)
    return () => window.clearTimeout(timer)
  }, [url, attempt])

  // **灯箱的寿命不跟 `url` 走。** 想当然的写法是"地址换了就丢掉上一份内容里点出来的
  // 那张图"，但预览地址带的是**短时凭证**：重取一次就是一张新票、一个新字符串，而
  // 内容可能一个字都没变——别处 push 一次、连接重连之后的那次重拉、用户点了「刷新」，
  // 都会换一个地址。把灯箱的寿命挂在这条地址上，表现就是"点开的图闪一下就被关掉"。
  //
  // 灯箱是模态的，关它的人是用户；它也盖住了整个界面，所以"内容换了而灯箱还开着"
  // 这件事在界面上发生不了。

  useEffect(() => {
    function onMessage(event: MessageEvent): void {
      // **只认自己那一帧。** 不透明源的 `event.origin` 是字符串 `null`，而任何沙箱
      // 帧都是 `null`，那个值认不出是谁发的；能回答"是谁发的"的只有帧本身。
      if (event.source !== frame.current?.contentWindow) {
        return
      }
      const request = parseFrameImagePreview(event.data)
      if (request !== null) {
        setPreview(request)
      }
    }
    window.addEventListener('message', onMessage)
    return () => window.removeEventListener('message', onMessage)
  }, [])

  return (
    <>
      <div
        style={{
          position: 'relative',
          width: '100%',
          height,
          border: `1px solid ${token.colorBorderSecondary}`,
          borderRadius: token.borderRadius,
          // 圆角长在框上，帧自己的四个角是方的：裁掉，否则角上会露出四小块方角。
          overflow: 'hidden',
        }}
      >
        <iframe
          // 重挂是重试的实现方式，因此 `key` 只跟重试次数走；地址变了不必重挂，
          // 改 `src` 本身就是一次导航。
          key={attempt}
          ref={frame}
          title={title}
          // 逐字写死沙箱属性：不得出现 allow-same-origin，见上方注释。
          sandbox="allow-scripts"
          src={url}
          onLoad={() => settle('loaded')}
          style={{
            width: '100%',
            height: '100%',
            border: 'none',
            // 不给内容注入任何样式（见 spec）：底色交给浏览器默认，主站不参与。
            display: 'block',
          }}
        />
        {load === 'covering' && <LoadingCover />}
        {load === 'overdue' && <LoadingOverdueBar onRetry={() => setAttempt((n) => n + 1)} />}
      </div>
      <FrameImageLightbox request={preview} onClose={() => setPreview(null)} />
    </>
  )
}

/**
 * `load` 之前铺在帧上的遮罩。
 *
 * 它不透明是**有意的**：这一帧此刻是空的（浏览器还没拿到任何字节），把调用方的底色
 * 透出来只会让"还没开始加载"和"内容就是一片空白"看起来一样。铺到内容开始出现为止
 * 的分寸见上方 `COVER_TIMEOUT_MS`；长什么样由 `LoadingHint` 一处定（见
 * docs/ssot-registry.md）。
 */
function LoadingCover(): React.ReactNode {
  const { token } = theme.useToken()
  return (
    <div style={{ position: 'absolute', inset: 0, background: token.colorBgContainer }}>
      <LoadingHint />
    </div>
  )
}

/**
 * 到点仍未加载完时贴住框顶的一条状态条。
 *
 * **它不盖住正文。** 到这一刻页面多半已经渲染出来了，还在路上的是某些子资源；再盖着
 * 不透明的东西就是把能看的内容重新藏回去。因此它只占一条，正文照旧可见、可交互。
 */
function LoadingOverdueBar({ onRetry }: { onRetry: () => void }): React.ReactNode {
  const { token } = theme.useToken()
  return (
    <Flex
      align="center"
      gap={12}
      style={{
        position: 'absolute',
        top: 0,
        left: 0,
        right: 0,
        padding: '8px 12px',
        background: token.colorBgElevated,
        borderBottom: `1px solid ${token.colorBorderSecondary}`,
      }}
    >
      <Spin size="small" />
      <Typography.Text type="secondary" style={{ flex: 1 }}>
        内容还在加载，已经超过 {COVER_TIMEOUT_SECONDS} 秒。可以再等等，也可以重试一次。
      </Typography.Text>
      <Button size="small" onClick={onRetry}>
        重试
      </Button>
    </Flex>
  )
}
