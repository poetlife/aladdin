import { createHash } from 'node:crypto'

/**
 * 一份字节的内容摘要：SHA-256 的十六进制小写。
 *
 * **这是 SDK 侧算摘要的唯一入口。** 它要与服务端认的那一份逐字相同——文件组里的
 * 文本条目、资产的公开区地址、以及本地资产记录（见 lock/asset-lock.ts）都以它
 * 为键，任何一处写出另一种写法，表现都是"东西传上去了但谁都不认"。
 *
 * 输入按字节而不是按字符串：源里的一份素材可能是任意二进制，按 utf8 解码再编码
 * 会把非 UTF-8 的字节换掉，摘要随之变成一个谁也对不上的值。
 */
export function contentDigest(bytes: Uint8Array): string {
  return createHash('sha256').update(bytes).digest('hex')
}
