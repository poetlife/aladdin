import { describe, expect, it } from 'vitest'

import { describeDuration } from './duration'

describe('describeDuration', () => {
  it('不足一分钟时说秒，不说「0 分钟」', () => {
    // 重启之后要看的那一眼正是这一档：说「0 分钟」等于什么也没说。
    expect(describeDuration(0)).toBe('0 秒')
    expect(describeDuration(8)).toBe('8 秒')
    expect(describeDuration(59)).toBe('59 秒')
  })

  it('按最大的两个单位说，其余舍去', () => {
    expect(describeDuration(60)).toBe('1 分')
    expect(describeDuration(90)).toBe('1 分 30 秒')
    expect(describeDuration(3600)).toBe('1 小时')
    expect(describeDuration(3600 + 7 * 60)).toBe('1 小时 7 分')
    expect(describeDuration(2 * 86400 + 5 * 3600)).toBe('2 天 5 小时')
    expect(describeDuration(2 * 86400)).toBe('2 天')
    // 秒与分都在，但只有最大的两级出现。
    expect(describeDuration(3 * 86400 + 4 * 3600 + 5 * 60 + 6)).toBe('3 天 4 小时')
  })

  it('时钟偏差算出的负数说成「—」，不说成一个负的时长', () => {
    // 客户端与服务端差几秒时 uptime 会是负的。显示「-3 秒」只会让人以为页面坏了。
    expect(describeDuration(-3)).toBe('—')
    expect(describeDuration(Number.NaN)).toBe('—')
    expect(describeDuration(Number.POSITIVE_INFINITY)).toBe('—')
  })
})
