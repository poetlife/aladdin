import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Client } from '@connectrpc/connect'

import { ContentSlot, type FileEntry, type GalaxyService } from '../gen/proto/aladdin/galaxy/v1/galaxy_pb.js'
import { pushDraft } from './content.js'

const dirs: string[] = []

afterEach(() => {
  vi.unstubAllGlobals()
  for (const dir of dirs.splice(0)) {
    rmSync(dir, { recursive: true, force: true })
  }
})

beforeEach(() => {
  // 直传那一趟在测试里只需"成功"这一件事：它自己的验证在
  // cos-signature.test.ts 里（与官方 SDK 逐字对齐），这里不重复。
  vi.stubGlobal('fetch', async () => new Response('', { status: 200 }))
})

interface Recorded {
  contentUploads: string[]
  pushed: FileEntry[]
}

/** 一个只记账的假服务端：本用例只关心"哪一份被当成资产、哪一份被当成文本"。 */
function fakeGalaxy(recorded: Recorded): Client<typeof GalaxyService> {
  return {
    async beginContentUpload(request: { digest: string }) {
      recorded.contentUploads.push(request.digest)
      return {
        alreadyExists: false,
        upload: {
          bucket: 'b-1',
          region: 'ap-guangzhou',
          key: `galaxy/prj_1/text/${request.digest}`,
          secretId: 'id',
          secretKey: 'key',
          sessionToken: 'tok',
          expiresAt: '',
        },
      }
    },
    async commitContentUpload() {
      return {}
    },
    async pushDraft(request: { entries: FileEntry[] }) {
      recorded.pushed.push(...request.entries)
      return { draft: { entries: request.entries, updatedAt: '', slot: ContentSlot.SITE } }
    },
  } as unknown as Client<typeof GalaxyService>
}

function digestOf(text: string): string {
  return createHash('sha256').update(Buffer.from(text)).digest('hex')
}

describe('整组推送草稿', () => {
  it('在记录里的走资产条目，不在的走文本条目', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'aladdin-push-'))
    dirs.push(dir)
    mkdirSync(join(dir, 'assets'), { recursive: true })

    const pageBytes = '<!doctype html><img src="/assets/ast_1">'
    const logoBytes = 'PNG-BYTES'
    writeFileSync(join(dir, 'index.html'), pageBytes)
    writeFileSync(join(dir, 'assets/ast_1'), logoBytes)

    const lockFile = join(dir, 'aladdin.assets.json')
    writeFileSync(
      lockFile,
      JSON.stringify({ version: 1, assets: { [digestOf(logoBytes)]: 'ast_1' } }),
    )

    const recorded: Recorded = { contentUploads: [], pushed: [] }
    await pushDraft(fakeGalaxy(recorded), {
      projectId: 'prj_1',
      slot: ContentSlot.SITE,
      directory: dir,
      lockFile,
    })

    const byPath = new Map(recorded.pushed.map((entry) => [entry.path, entry]))
    const assetEntry = byPath.get('assets/ast_1')
    expect(assetEntry?.source.case).toBe('assetId')
    if (assetEntry?.source.case !== 'assetId') {
      throw new Error('这一条本该被当成资产条目')
    }
    expect(assetEntry.source.value).toBe('ast_1')
    expect(byPath.get('index.html')?.source.case).toBe('digest')

    // **只有文本条目那一份被传上去**：资产条目的字节由平台上的那一份提供，
    // 再传一遍就是同一个工程里存两份同样的字节。记录文件本身也不是内容——
    // 它就在这个目录里，但它不该被发布出去。
    expect(recorded.contentUploads).toEqual([digestOf(pageBytes)])
    expect(recorded.pushed.map((entry) => entry.path).sort()).toEqual(['assets/ast_1', 'index.html'])
  })

  it('条目路径用 / 分隔，与文件组的路径形状一致', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'aladdin-push-'))
    dirs.push(dir)
    mkdirSync(join(dir, 'guide/intro'), { recursive: true })
    writeFileSync(join(dir, 'guide/intro/index.html'), 'x')

    const recorded: Recorded = { contentUploads: [], pushed: [] }
    await pushDraft(fakeGalaxy(recorded), {
      projectId: 'prj_1',
      slot: ContentSlot.SITE,
      directory: dir,
      lockFile: join(dir, 'nope.json'),
    })

    expect(recorded.pushed.map((entry) => entry.path)).toEqual(['guide/intro/index.html'])
  })

  it('整组替换：空目录推上去就是一份空清单', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'aladdin-push-'))
    dirs.push(dir)
    const recorded: Recorded = { contentUploads: [], pushed: [] }
    const draft = await pushDraft(fakeGalaxy(recorded), {
      projectId: 'prj_1',
      slot: ContentSlot.SITE,
      directory: dir,
      lockFile: join(dir, 'nope.json'),
    })
    expect(draft.entries).toEqual([])
  })
})
