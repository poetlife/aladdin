import type { TimeWindow } from '../gen/proto/aladdin/telemetry/v1/telemetry_admin_pb'
import { telemetryAdminClient } from './transport'

/**
 * 客户端事件只读管理面的调用封装。
 *
 * 与 rbac.ts 同理：类型来自 proto 生成，这里只提供有名字的调用入口。
 * 时间窗与条数都由服务端收敛，前端不自己算时间范围——那样两端的"今天从哪一刻
 * 算起"会各有一份，而分歧只会表现为数字对不上。
 */

/** 按 client × action × result 统计时间窗内的条数。 */
export async function listEventStats(scope: string, window: TimeWindow) {
  return telemetryAdminClient().listEventStats({ scope, window })
}

/** 列出时间窗内最新的一页事件明细。limit 为 0 时由服务端取默认值。 */
export async function listRecentEvents(scope: string, window: TimeWindow, limit = 0) {
  return telemetryAdminClient().listRecentEvents({ scope, window, limit })
}
