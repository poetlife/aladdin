import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

import {
  FRAME_CHANNEL,
  FRAME_CHANNEL_VERSION,
  FRAME_CONTENT_CREDENTIAL_REQUEST_TYPE,
  FRAME_CONTENT_CREDENTIAL_TYPE,
  FRAME_IMAGE_PREVIEW_TYPE,
  frameMessage,
  isFrameEnvelope,
  parseContentCredential,
} from './frame-channel.js'

/**
 * 协议形状的漂移闸门。
 *
 * 这条通道在仓库里有**三处代码、一个形状**：服务端渲染进 `docs` 槽那一页的
 * 常量脚本（Go）、主站壳侧的解析（web）、以及用户页面侧的这一份。三处各写一份
 * 是跨语言逼出来的，但**取值只能有一个**——取值漂了的表现是"宿主认不出页面发的
 * 消息"，而三处代码各自看都自洽。
 *
 * 因此这里直接读那两份文件的文本，把取值钉在一起。文件不在（这个包被单独安装到
 * 别处时）就跳过：这一条是仓库内的闸门，不是运行时约束。
 */
describe('接入桥的信封与仓库里另外两处同源', () => {
  const goSource = repoFile('internal/galaxy/doc_frame_bridge.go')
  const webSource = repoFile('web/src/pages/galaxy/frame-channel.ts')

  it('通道标识与协议版本与服务端渲染的那一段脚本相同', () => {
    if (goSource === null) {
      return
    }
    const text = readFileSync(goSource, 'utf8')
    expect(/FrameChannelName\s*=\s*"([^"]+)"/.exec(text)?.[1]).toBe(FRAME_CHANNEL)
    expect(/FrameChannelVersion\s*=\s*(\d+)/.exec(text)?.[1]).toBe(String(FRAME_CHANNEL_VERSION))
  })

  it('通道标识与协议版本与主站壳侧的解析相同', () => {
    if (webSource === null) {
      return
    }
    const text = readFileSync(webSource, 'utf8')
    expect(/FRAME_CHANNEL\s*=\s*'([^']+)'/.exec(text)?.[1]).toBe(FRAME_CHANNEL)
    expect(/FRAME_CHANNEL_VERSION\s*=\s*(\d+)/.exec(text)?.[1]).toBe(String(FRAME_CHANNEL_VERSION))
  })

  it('宿主侧认得本包用到的每一种消息类型', () => {
    if (webSource === null) {
      return
    }
    const text = readFileSync(webSource, 'utf8')
    // 宿主认不出的类型一律忽略，因此"本包发出去、宿主不认"的表现是**静默无反应**：
    // 页面上什么错都没有，只是什么也没发生。取值因此要在两边各钉一次。
    expect(text).toContain(`'${FRAME_CONTENT_CREDENTIAL_REQUEST_TYPE}'`)
    expect(text).toContain(`'${FRAME_CONTENT_CREDENTIAL_TYPE}'`)
    expect(text).toContain(`'${FRAME_IMAGE_PREVIEW_TYPE}'`)
  })

  it('加的是消息类型，不是版本', () => {
    // 信封本来就是对称的，v1 里宿主 → 页面那一半是空的。本包要用它，因此
    // 只增类型；版本一旦动，已经发出去的那些页面会整体认不出宿主的话。
    expect(FRAME_CHANNEL_VERSION).toBe(1)
    expect(FRAME_CONTENT_CREDENTIAL_TYPE).not.toBe('')
  })
})

describe('信封的校验', () => {
  it('认不出的版本一律忽略', () => {
    expect(isFrameEnvelope({ channel: FRAME_CHANNEL, version: 2, type: 'x', payload: {} })).toBe(false)
    expect(isFrameEnvelope({ channel: '别人的通道', version: 1, type: 'x', payload: {} })).toBe(false)
    expect(isFrameEnvelope('不是对象')).toBe(false)
    expect(isFrameEnvelope(null)).toBe(false)
  })

  it('组出来的消息能过自己的校验', () => {
    expect(isFrameEnvelope(frameMessage('x', {}))).toBe(true)
  })
})

describe('凭证答复的解析', () => {
  it('认得出这一条', () => {
    const message = frameMessage(FRAME_CONTENT_CREDENTIAL_TYPE, {
      credential: 'tok',
      expiresAt: '2030-01-01T00:00:00Z',
      reason: '',
    })
    expect(parseContentCredential(message)).toEqual({
      credential: 'tok',
      expiresAt: '2030-01-01T00:00:00Z',
      reason: '',
    })
  })

  it('别的类型、别的形状一律返回 null，不猜', () => {
    expect(parseContentCredential(frameMessage('image-preview', {}))).toBeNull()
    expect(parseContentCredential(frameMessage(FRAME_CONTENT_CREDENTIAL_TYPE, 'x'))).toBeNull()
    expect(parseContentCredential({ channel: FRAME_CHANNEL, version: 9, type: FRAME_CONTENT_CREDENTIAL_TYPE })).toBeNull()
  })

  it('拿不到时也要有一条明确的否定，缺字段按空值读', () => {
    expect(parseContentCredential(frameMessage(FRAME_CONTENT_CREDENTIAL_TYPE, { reason: '没有' }))).toEqual({
      credential: '',
      expiresAt: '',
      reason: '没有',
    })
  })
})

function repoFile(relative: string): string | null {
  const path = fileURLToPath(new URL(`../../../${relative}`, import.meta.url))
  return existsSync(path) ? path : null
}
