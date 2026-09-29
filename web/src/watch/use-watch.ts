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
 * 订阅一组主题；某条主题变了（以及每次（重）连接之后服务端发来的 RESYNC）就调一次
 * `onChange`，参数是**变了的那条主题**。
 *
 * **它只负责"什么时候该重看一眼"**，不负责看一眼之后做什么（重拉走各业务既有的
 * unary 入口，事件不含内容）。三条策略都在这里：
 *
 * 1. **集合变化就重开一条**。连接内改订阅要双向流，而双向流要求端到端 HTTP/2，
 *    当前部署形态给不了（见 docs/design/events/README.md）。重开复用同一条退避
 *    链路，代价只是重连之后每个主题各重拉一次——而 RESYNC 正是为此存在的。
 * 2. **流结束时重连**：正常结束（服务端的上限寿命到点）与出错走同一条路。重连
 *    之后服务端会给每个主题各一条 RESYNC，于是重拉——这正是交付语义是"至多一次、
 *    不重放"仍然安全的原因。
 * 3. **卸载即中止**：不中止的话那条流会一直挂在服务端（它的寿命上限是半小时），
 *    而这一页早就不在了。
 *
 * 心跳不回调（它不是变更）；**重试无意义的失败不重连**（见 isPermanentFailure）：
 * 没权限、主题类型没注册过、主题不存在——退避再多次也是同一个结论，而页面自己
 * 的那次读取会显示原因。
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

    const controller = new AbortController()
    let attempt = 0
    let timer: ReturnType<typeof setTimeout> | undefined
    let stopped = false

    const connect = async (): Promise<void> => {
      try {
        for await (const event of watchTopics(subscribed, controller.signal)) {
          if (event.control === Control.HEARTBEAT) {
            continue
          }
          // 有主题的事件都要重拉一次：普通变更与 RESYNC 在调用方看来是同一件事。
          if (event.topic === '') {
            continue
          }
          // 收到过东西说明这条流是通的，退避从头算。
          attempt = 0
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
        // 其余（网络中断、服务端重启）走退避重连。
      }
      if (stopped) {
        return
      }
      const base = Math.min(RECONNECT_MIN_MS * 2 ** attempt, RECONNECT_MAX_MS)
      attempt += 1
      timer = setTimeout(() => {
        void connect()
      }, base * (0.5 + Math.random() * 0.5))
    }

    void connect()

    return () => {
      stopped = true
      if (timer !== undefined) {
        clearTimeout(timer)
      }
      controller.abort()
    }
  }, [key])
}
