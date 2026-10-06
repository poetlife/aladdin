import { describe, expect, it } from 'vitest'

import { FRAME_CHANNEL, FRAME_CHANNEL_VERSION, parseFrameImagePreview } from './frame-channel'

/** 一条形状正确的消息：只改要测的那一处。 */
function message(payloadOverrides: Record<string, unknown> = {}, envelopeOverrides: Record<string, unknown> = {}) {
  return {
    channel: FRAME_CHANNEL,
    version: FRAME_CHANNEL_VERSION,
    type: 'image-preview',
    payload: {
      images: [
        { src: 'https://pub.example.com/g/prj_x/docs/a.png', alt: '甲' },
        { src: 'https://bucket.example.com/b.png', alt: '乙' },
      ],
      index: 1,
      ...payloadOverrides,
    },
    ...envelopeOverrides,
  }
}

describe('接入桥的消息', () => {
  it('认下约定的一条，取出全部图与被点那张的下标', () => {
    expect(parseFrameImagePreview(message())).toEqual({
      images: [
        { src: 'https://pub.example.com/g/prj_x/docs/a.png', alt: '甲' },
        { src: 'https://bucket.example.com/b.png', alt: '乙' },
      ],
      index: 1,
    })
  })

  // 信封之外一律忽略：通道、版本、类型三处任一不认，就当没收到。
  const envelopeCases: [string, unknown][] = [
    ['通道不对', message({}, { channel: 'other/frame' })],
    ['版本不对', message({}, { version: FRAME_CHANNEL_VERSION + 1 })],
    ['类型不认识', message({}, { type: 'something-else' })],
  ]
  it.each(envelopeCases)('%s 的载荷被丢弃', (_name, data) => {
    expect(parseFrameImagePreview(data)).toBeNull()
  })

  const payloadCases: [string, unknown][] = [
    ['不是对象', 'nonsense'],
    ['是 null', null],
    ['载荷不是对象', message({}, { payload: 'nope' })],
    ['图不是数组', { ...message(), payload: { images: 'nope', index: 0 } }],
    ['一张图都没有', message({ images: [], index: 0 })],
    [
      '图多到不合理',
      message({ images: Array.from({ length: 101 }, () => ({ src: 'https://a.example.com/x.png' })), index: 0 }),
    ],
    ['下标越界', message({ index: 2 })],
    ['下标为负', message({ index: -1 })],
    ['下标不是整数', message({ index: 0.5 })],
    ['下标不是数字', message({ index: '0' })],
    ['图上没有地址', message({ images: [{ alt: '甲' }], index: 0 })],
    ['图不是对象', message({ images: ['https://a.example.com/x.png'], index: 0 })],
  ]
  it.each(payloadCases)('%s 的载荷被丢弃', (_name, data) => {
    expect(parseFrameImagePreview(data)).toBeNull()
  })

  // 地址只进 `<img>`，因此这里挡的是"不该出现在图片地址里的写法"。
  const urlCases: [string, string][] = [
    ['相对地址', '/g/prj_x/docs/a.png'],
    ['data 地址', 'data:image/png;base64,AAAA'],
    ['javascript 地址', 'javascript:alert(1)'],
    ['没有协议的写法', 'pub.example.com/a.png'],
    ['空地址', ''],
  ]
  it.each(urlCases)('%s 被挡下', (_name, src) => {
    expect(parseFrameImagePreview(message({ images: [{ src, alt: '' }], index: 0 }))).toBeNull()
  })

  it('替代文本缺失时留空、过长时截断', () => {
    const long = 'x'.repeat(400)
    const request = parseFrameImagePreview(
      message({ images: [{ src: 'https://a.example.com/x.png' }, { src: 'https://a.example.com/y.png', alt: long }], index: 0 }),
    )
    expect(request?.images[0]?.alt).toBe('')
    expect(request?.images[1]?.alt).toHaveLength(300)
  })
})
