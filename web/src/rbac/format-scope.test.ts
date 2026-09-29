import { describe, expect, it } from 'vitest'

import { formatScope, GLOBAL_SCOPE_LABEL, parseScope } from './format-scope'

/**
 * 这一对转换是全站显示"全局"的唯一入口：谁各自写一份，就会有人漏掉其中一侧，
 * 于是同一个范围在不同页面上写法不同。
 */
describe('管理范围的显示与解析', () => {
  it('空值显示为「全局」', () => {
    expect(formatScope('')).toBe(GLOBAL_SCOPE_LABEL)
  })

  it('具体范围原样显示', () => {
    expect(formatScope('tenant/acme')).toBe('tenant/acme')
  })

  it('输入「全局」或留空都解析成空值', () => {
    expect(parseScope(GLOBAL_SCOPE_LABEL)).toBe('')
    expect(parseScope('  ')).toBe('')
  })

  it('具体范围去掉首尾空白后原样保留', () => {
    expect(parseScope(' tenant/acme ')).toBe('tenant/acme')
  })

  it('显示与解析互为往返', () => {
    for (const scope of ['', 'tenant/acme', 'tenant/acme/project/web']) {
      expect(parseScope(formatScope(scope))).toBe(scope)
    }
  })
})
