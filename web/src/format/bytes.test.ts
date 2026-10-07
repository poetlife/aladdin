import { describe, expect, it } from 'vitest'

import { describeBytes } from './bytes'

/**
 * 这一条守的是"展示层的字节数只有一处说法"。
 *
 * 重点是**小于 1 KiB 的那一档**：换成 KB 会把它们一律舍成「0 KB」，而文件清单里
 * 最常见的就是这一档。用 KB 表它等于把一个准确的读数压成"看着像没读到"。
 */
describe('describeBytes', () => {
  it('小于 1 KiB 直接说字节数，不折成「0 KB」', () => {
    expect(describeBytes(0)).toBe('0 B')
    expect(describeBytes(100)).toBe('100 B')
    expect(describeBytes(1023)).toBe('1023 B')
  })

  it('1 KiB 到 1 MiB 之间说 KB，按最接近的整数舍入', () => {
    expect(describeBytes(1024)).toBe('1 KB')
    expect(describeBytes(1536)).toBe('2 KB')
  })

  it('整数 MiB 不带小数位', () => {
    expect(describeBytes(1024 * 1024)).toBe('1 MiB')
    expect(describeBytes(5 * 1024 * 1024)).toBe('5 MiB')
  })

  it('非整数 MiB 保留一位小数', () => {
    // 2.5 MiB
    expect(describeBytes(2 * 1024 * 1024 + 512 * 1024)).toBe('2.5 MiB')
    // 约 1.04 MiB：舍到一位小数
    expect(describeBytes(1090519)).toBe('1.0 MiB')
  })

  it('同一个取值，number 与 bigint 给出同一句话', () => {
    expect(describeBytes(100n)).toBe(describeBytes(100))
    expect(describeBytes(1536n)).toBe(describeBytes(1536))
    expect(describeBytes(2621440n)).toBe(describeBytes(2621440))
  })

  it('超过 2^53 的取值不被折叠', () => {
    // 这两个数在 double 里是同一个数（都舍成 2^60），先转 number 再算就分不开。
    // 换算全程走 bigint，因此它们仍然给出两句话——这正是入参接受 bigint 的理由。
    expect(describeBytes(2n ** 60n + 1n)).not.toBe(describeBytes(2n ** 60n))
  })
})
