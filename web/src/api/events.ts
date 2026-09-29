import type { Event as WatchEvent } from '../gen/proto/aladdin/events/v1/events_pb'
import { eventsClient } from './transport'

/**
 * 事件通道的调用封装（见 docs/design/events/README.md）。
 *
 * 它是**连接级**的：一条流承载 `topics` 里的全部主题，事件用 `topic` 标出是关于
 * 哪一条的。集合变了（切页、开关面板）就重开一条——这是刻意的：连接内改订阅要
 * 双向流，而双向流要求端到端 HTTP/2，当前的部署形态给不了。
 *
 * 交付语义是**至多一次、不重放**，而服务端会把每个主题各一条 `RESYNC` 作为最先
 * 到达的事件。因此调用方的义务只有一条：**收到任何一条事件、以及任何一次（重）
 * 连接成功之后，都重拉一次受影响的主题**——重拉走各业务既有的 unary 入口，事件
 * 本身不含内容。
 *
 * `signal` 用来退订：页面卸载、或集合变化要换一条流时中止上一条，否则它会一直
 * 挂到服务端的上限寿命。
 *
 * 生成出来的消息类型就叫 `Event`（与 DOM 的全局 `Event` 同名），所以这里把它
 * 改名成 `WatchEvent` 再用；调用方需要这个类型时也照此处理。
 */
export function watchTopics(topics: readonly string[], signal: AbortSignal): AsyncIterable<WatchEvent> {
  return eventsClient().watch({ topics: [...topics] }, { signal })
}
