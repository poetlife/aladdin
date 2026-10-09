/**
 * 把一段时长说成人话（"3 天 5 小时"），用于展示。
 *
 * 它是**唯一实现**：部署信息页的 uptime 是眼下唯一的调用方，但它不该因此长在
 * 那个页面里——"多久"与"多大"（见 ./bytes）是同一类展示问题，各写一份的表现是
 * 同一段时间在两个界面上给出两种读法。
 *
 * 取**最大的两个单位**，其余舍去：`2 天 5 小时 37 分 12 秒` 里的后两项对"这个
 * 进程起来多久了"不提供任何判断价值，却要占掉一行的宽度。秒级只在不足一分钟时
 * 出现，那时它是唯一有意义的读数。
 */

/** 一分钟、一小时、一天的秒数。 */
const MINUTE = 60
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

/**
 * 把秒数说成"多久"。
 *
 * 负数（客户端与服务端时钟有偏差时可能算出负数）说成「—」而不是负数时长：
 * 一个 "-3 秒" 的 uptime 只会让人以为页面坏了，而它其实只是两边时钟差了几秒。
 */
export function describeDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) {
    return '—'
  }

  const days = Math.floor(seconds / DAY)
  const hours = Math.floor((seconds % DAY) / HOUR)
  const minutes = Math.floor((seconds % HOUR) / MINUTE)

  // 依次落到第一个非零单位，再带上它的下一级。**不足一分钟先说秒**：
  // 那时候说「0 分钟」等于什么也没说，而"刚起来 8 秒"正是重启后要看的那一眼。
  if (days > 0) {
    return hours > 0 ? `${days} 天 ${hours} 小时` : `${days} 天`
  }
  if (hours > 0) {
    return minutes > 0 ? `${hours} 小时 ${minutes} 分` : `${hours} 小时`
  }
  if (minutes > 0) {
    const secs = Math.floor(seconds % MINUTE)
    return secs > 0 ? `${minutes} 分 ${secs} 秒` : `${minutes} 分`
  }
  return `${Math.floor(seconds)} 秒`
}
