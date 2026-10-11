import { createHash } from 'node:crypto'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { afterEach, describe, expect, it } from 'vitest'

import {
  digestOfFile,
  emptyAssetLock,
  parseAssetLock,
  readAssetLock,
  withAsset,
  writeAssetLock,
} from './asset-lock.js'

const dirs: string[] = []

function tempDir(): string {
  const dir = mkdtempSync(join(tmpdir(), 'aladdin-lock-'))
  dirs.push(dir)
  return dir
}

afterEach(() => {
  for (const dir of dirs.splice(0)) {
    rmSync(dir, { recursive: true, force: true })
  }
})

describe('本地资产记录', () => {
  it('文件不存在就是一份空记录', () => {
    expect(readAssetLock(join(tempDir(), 'nope.json'))).toEqual(emptyAssetLock())
  })

  it('写出去再读回来逐字相同', () => {
    const file = join(tempDir(), 'aladdin.assets.json')
    const lock = withAsset(withAsset(emptyAssetLock(), 'bb', 'ast_b'), 'aa', 'ast_a')
    writeAssetLock(file, lock)
    expect(readAssetLock(file)).toEqual(lock)
  })

  it('按键排序写出，好让内容相同的记录逐字相同', () => {
    const file = join(tempDir(), 'aladdin.assets.json')
    writeAssetLock(file, { version: 1, assets: { zz: 'ast_z', aa: 'ast_a' } })
    const text = readFileSync(file, 'utf8')
    expect(Object.keys(parseAssetLock(text, file).assets)).toEqual(['aa', 'zz'])
    // 两次写出同一份内容必须逐字相同：否则每次上传都产生一次只因键序不同的
    // 改动，把版本库里真正的改动淹掉。
    writeAssetLock(file, { version: 1, assets: { aa: 'ast_a', zz: 'ast_z' } })
    expect(readFileSync(file, 'utf8')).toBe(text)
  })

  it('版本不对即拒绝，而不是猜着读', () => {
    expect(() => parseAssetLock('{"version":2,"assets":{}}', 'x.json')).toThrow(/版本/)
  })

  it('形状不对即拒绝，并指出是哪一份文件', () => {
    expect(() => parseAssetLock('不是 json', 'x.json')).toThrow(/x\.json/)
    expect(() => parseAssetLock('[]', 'x.json')).toThrow(/x\.json/)
    expect(() => parseAssetLock('{"version":1,"assets":{"a":1}}', 'x.json')).toThrow(/非空字符串/)
  })

  it('摘要按字节算，不经过 utf8 解码', () => {
    const dir = tempDir()
    const bytes = Buffer.from([0xff, 0xfe, 0x00, 0x80])
    const file = join(dir, 'raw.bin')
    writeFileSync(file, bytes)
    // 这份字节不是合法的 UTF-8：先解码成字符串再编码会把字节换掉，摘要随之变成
    // 一个谁也对不上的值。
    expect(digestOfFile(file)).toBe(createHash('sha256').update(bytes).digest('hex'))
    expect(digestOfFile(file)).not.toBe(
      createHash('sha256').update(bytes.toString('utf8'), 'utf8').digest('hex'),
    )
  })
})
