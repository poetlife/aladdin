import { useEffect, useRef } from 'react'

import { isPermanentFailure, isUnauthenticated } from '../api/errors'
import { watchTopics } from '../api/events'
import { notifyUnauthenticated } from '../api/transport'
import { Control } from '../gen/proto/aladdin/events/v1/events_pb'

/**
 * 重连退避的起点、上限与抖动。
 *
 * 退避是因为断流常常是"服务端在重启"或"网络刚断"，立刻重试只会一串失败。
 * 抖动是因为多个标签页多半在同一时刻一起断——不抖的话它们会一起回来。
 */
const RECONNECT_MIN_MS = 1_000
const RECONNECT_MAX_MS = 30_000

/**
 * 多久没从这条流上收到任何东西（心跳也算）就判它已经死了。
 *
 * **为什么必须有它**：半开的连接——笔记本睡眠、NAT 表项过期、中途换网——**两边都
 * 不会报错**：服务端的心跳写不出去（它只是重传），客户端也永远读不到任何东西。
 * 不报错就不会重连，不重连就没有 `RESYNC`，于是页面可以无限期停在旧状态上。
 * 浏览器冻结后台标签页只是触发这件事的一种方式，不是它的全部——所以判据是**这条
 * 连接还有没有动静**，而不是"用户有没有离开过"（见 docs/design/events/README.md
 * 的"页面隐藏时"）。
 *
 * 取值远大于心跳周期（服务端 25 秒一条，见 internal/server/events_service.go 的
 * `watchHeartbeat`），是为了容忍一次网络抖动与代理层的一跳缓冲。**这个量级与心跳
 * 周期是一对**：心跳调大，它也应当跟着调大。前端测试直接用它来推进假定时器，
 * 因此导出。
 */
export const STALL_TIMEOUT_MS = 90_000

/**
 * 订阅一组主题；某条主题变了（以及每次（重）连接之后服务端发来的 RESYNC）就调一次
 * `onChange`，参数是**变了的那条主题**。
 *
 * **它只负责"什么时候该重看一眼"**，不负责看一眼之后做什么（重拉走各业务既有的
 * unary 入口，事件不含内容）。四条策略都在这里：
 *
 * 1. **集合变化就重开一条**。连接内改订阅要双向流，而双向流要求端到端 HTTP/2，
 *    当前部署形态给不了（见 docs/design/events/README.md）。重开复用同一条退避
 *    链路，代价只是重连之后每个主题各重拉一次——而 RESYNC 正是为此存在的。
 * 2. **流结束时重连**：正常结束（服务端的上限寿命到点）与出错走同一条路。重连
 *    之后服务端会给每个主题各一条 RESYNC，于是重拉——这正是交付语义是"至多一次、
 *    不重放"仍然安全的原因。
 * 3. **静默到判死就断开重连**（见 `STALL_TIMEOUT_MS`）。这一条补的是上一条够不到
 *    的场合：连接死了而**没有任何一方报错**，于是"流结束"这件事永远不会发生。
 * 4. **卸载即中止**：不中止的话那条流会一直挂在服务端（它的寿命上限是半小时），
 *    而这一页早就不在了。
 *
 * 心跳不回调（它不是变更），但**它是这条连接还活着的证据**——判活用的就是它；
 * **重试无意义的失败不重连**（见 isPermanentFailure）：没权限、主题类型没注册过、
 * 主题不存在——退避再多次也是同一个结论，而页面自己的那次读取会显示原因。
 */
export function useWatch(topics: readonly string[], onChange: (topic: string) => void): void {
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange
  const topicsRef = useRef(topics)
  topicsRef.current = topics

  // 依赖是主题集合的**内容**而不是数组身份：调用方常常就地写一个数组字面量，
  // 按身份做依赖会让每次渲染都重开一条流。
  const key = [...topics].sort().join('\n')

  useEffect(() => {
    const subscribed = topicsRef.current
    if (subscribed.length === 0) {
      return
    }

    let attempt = 0
    let reconnectTimer: ReturnType<typeof setTimeout> | undefined
    let stallTimer: ReturnType<typeof setTimeout> | undefined
    // 当前这一条连接的句柄。它有两种断法：这里主动断（判死），或者帧卸载时断。
    let connection: AbortController | undefined
    let stopped = false

    const connect = async (): Promise<void> => {
      const current = new AbortController()
      connection = current
      // 计时器在收到任何东西（含心跳）时往后推：它量的不是"多久没等到事件"，
      // 而是"这条连接多久没动静了"。
      const armStall = (): void => {
        clearTimeout(stallTimer)
        stallTimer = setTimeout(() => {
          current.abort()
        }, STALL_TIMEOUT_MS)
      }
      armStall()
      try {
        for await (const event of watchTopics(subscribed, current.signal)) {
          armStall()
          // 收到过任何东西（**心跳也算**）都说明这条连接是通的，退避从头算：
          // 一条已经活了很久的连接再断，下一次重连不该背着更长的等待。
          attempt = 0
          if (event.control === Control.HEARTBEAT) {
            continue
          }
          // 有主题的事件都要重拉一次：普通变更与 RESYNC 在调用方看来是同一件事。
          if (event.topic === '') {
            continue
          }
          onChangeRef.current(event.topic)
        }
      } catch (error) {
        if (isUnauthenticated(error)) {
          // 会话失效：交给会话层引导重新登录。重连也还是未认证，因此不再重连。
          notifyUnauthenticated()
          return
        }
        if (isPermanentFailure(error)) {
          // 请求本身不成立：退避再多次也是同一个结论。页面自己那次读取会显示原因。
          return
        }
        // 其余（网络中断、服务端重启、以及上面那次主动断开的判死）走退避重连。
      }
      // 这一条连接到此为止：它的计时器不该再去动下一条。
      clearTimeout(stallTimer)
      if (stopped) {
        return
      }
      const base = Math.min(RECONNECT_MIN_MS * 2 ** attempt, RECONNECT_MAX_MS)
      attempt += 1
      reconnectTimer = setTimeout(() => {
        void connect()
      }, base * (0.5 + Math.random() * 0.5))
    }

    void connect()

    return () => {
      stopped = true
      if (reconnectTimer !== undefined) {
        clearTimeout(reconnectTimer)
      }
      clearTimeout(stallTimer)
      connection?.abort()
    }
  }, [key])
}
