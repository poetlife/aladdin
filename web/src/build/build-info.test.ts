import { describe, expect, it } from 'vitest'

import { compareCommits } from './build-info'

describe('compareCommits', () => {
  it('两侧都有且相同 → same', () => {
    expect(compareCommits('abc1234', 'abc1234')).toBe('same')
  })

  it('两侧都有但不同 → different', () => {
    expect(compareCommits('abc1234', 'def5678')).toBe('different')
  })

  it('任一侧缺值 → unknown，而不是 different', () => {
    // 这一条是本函数存在的理由：本机构建拿不到提交号，把它报成"不一致"会让每次
    // 本地开发都收到一个假告警——而平时总在报警的提示，在真不一致那天也就没人看了。
    expect(compareCommits('', 'abc1234')).toBe('unknown')
    expect(compareCommits('abc1234', '')).toBe('unknown')
    expect(compareCommits('', '')).toBe('unknown')
  })
})
