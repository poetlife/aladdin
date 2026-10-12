import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { afterEach, describe, expect, it, vi } from 'vitest'
import { build } from 'vite'

import { galaxyBuild } from './vite-plugin.js'
import { ASSET_LOCK_FILE } from '../lock/asset-lock.js'

const PUBLISH_ROOT = 'https://pub.example.com/g/prj_abc/'
const ASSET_ID = 'ast_test123'
const LOGO_BYTES = Buffer.from('\x89PNG\r\n\x1a\n-not-really-a-png', 'binary')

const dirs: string[] = []

afterEach(() => {
  vi.unstubAllGlobals()
  for (const dir of dirs.splice(0)) {
    rmSync(dir, { recursive: true, force: true })
  }
})

/**
 * 一个标准工程：**源里没有一处 aladdin 写法**。
 *
 * 这正是这个包要证明的那件事——`import logo from './logo.png'` 与
 * `<img src="./logo.png">` 是标准的写法，用户不必为平台改自己的源。
 */
function standardProject(): string {
  const dir = mkdtempSync(join(tmpdir(), 'aladdin-build-'))
  dirs.push(dir)
  mkdirSync(join(dir, 'src'), { recursive: true })
  writeFileSync(join(dir, 'logo.png'), LOGO_BYTES)
  writeFileSync(
    join(dir, 'index.html'),
    [
      '<!doctype html>',
      '<html><head><link rel="stylesheet" href="./src/style.css"></head>',
      '<body>',
      '<img src="./logo.png" alt="logo">',
      '<script type="module" src="./src/main.js"></script>',
      '</body></html>',
      '',
    ].join('\n'),
  )
  writeFileSync(
    join(dir, 'src/main.js'),
    "import logo from '../logo.png'\ndocument.title = logo\n",
  )
  writeFileSync(join(dir, 'src/style.css'), '.brand { background-image: url(../logo.png); }\n')
  writeFileSync(
    join(dir, ASSET_LOCK_FILE),
    `${JSON.stringify({ version: 1, assets: { [digestOf(LOGO_BYTES)]: ASSET_ID } }, null, 2)}\n`,
  )
  return dir
}

function digestOf(bytes: Buffer): string {
  return createHash('sha256').update(bytes).digest('hex')
}

async function buildProject(root: string): Promise<string> {
  const outDir = join(root, 'dist')
  await build({
    root,
    configFile: false,
    logLevel: 'silent',
    plugins: [galaxyBuild({ publishRoot: PUBLISH_ROOT })],
    build: { outDir, emptyOutDir: true },
  })
  return outDir
}

function readOut(outDir: string, name: string): string {
  return readFileSync(join(outDir, name), 'utf8')
}

/** 产物里的全部文件路径（相对 outDir）。 */
function listOut(outDir: string): string[] {
  const found: string[] = []
  const walk = (dir: string, prefix: string): void => {
    for (const entry of readdirSync(dir)) {
      const absolute = join(dir, entry)
      const relativePath = prefix === '' ? entry : `${prefix}/${entry}`
      if (statSync(absolute).isDirectory()) {
        walk(absolute, relativePath)
      } else {
        found.push(relativePath)
      }
    }
  }
  walk(outDir, '')
  return found.sort()
}

describe('构建期插件', () => {
  it('源里零方言，产物里每一处引用都落在文件组的那条条目上', async () => {
    const out = await buildProject(standardProject())
    const assetURL = `${PUBLISH_ROOT}assets/${ASSET_ID}`

    const html = readOut(out, 'index.html')
    expect(html).toContain(`src="${assetURL}"`)
    expect(html).not.toContain('./logo.png')

    const css = listOut(out).find((file) => file.endsWith('.css'))
    expect(css).toBeDefined()
    expect(readOut(out, css ?? '')).toContain(`url(${assetURL})`)

    expect(css?.length).toBeGreaterThan(0)
  })

  it('JS 里 import 进来的是那条地址，不是字节', async () => {
    const out = await buildProject(standardProject())
    const js = listOut(out).find((file) => file.endsWith('.js'))
    expect(js).toBeDefined()
    expect(readOut(out, js ?? '')).toContain(`${PUBLISH_ROOT}assets/${ASSET_ID}`)
  })

  it('素材的字节不进产物', async () => {
    const out = await buildProject(standardProject())
    // 产物里只有构建器自己产出的那些文件；一份素材都没有——它由平台上的那一份
    // 提供，而"产物里出现一份素材的副本"正是这条设计要避免的事。
    expect(listOut(out).filter((file) => file.endsWith('.png'))).toEqual([])
  })

  it('发布根交给构建器：产物里的绝对路径落在它下面', async () => {
    const out = await buildProject(standardProject())
    const html = readOut(out, 'index.html')
    // 构建器自己产出的那份 JS 也必须落在发布根下面，否则发布出去以后
    // 它的每一条绝对路径都是错的。
    expect(html).toMatch(new RegExp(`src="${PUBLISH_ROOT.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}assets/[^"]+\\.js"`))
  })

  it('构建期不联网', async () => {
    // 断网的最直接写法：让任何一次网络调用直接失败。构建照常完成，
    // 说明插件没有在构建期去取发布根、传素材、或问校验结论。
    vi.stubGlobal('fetch', () => {
      throw new Error('构建期不该发起任何请求')
    })
    const out = await buildProject(standardProject())
    expect(readOut(out, 'index.html')).toContain(`${PUBLISH_ROOT}assets/${ASSET_ID}`)
  })

  it('用户自己设了一个不同的 base 时如实报错', async () => {
    const root = standardProject()
    await expect(
      build({
        root,
        configFile: false,
        logLevel: 'silent',
        base: 'https://other.example.com/',
        plugins: [galaxyBuild({ publishRoot: PUBLISH_ROOT })],
        build: { outDir: join(root, 'dist'), emptyOutDir: true },
      }),
    ).rejects.toThrow(/发布根/)
  })

  it('记录里没有的素材照常交给构建器，插件不替它下结论', async () => {
    const root = standardProject()
    // 一份**存在但没进过资产库**的素材：插件认不出它，于是原样留给构建器——
    // 它会被照常打包进产物，而"产物里这一处引用不落在文件组上"由服务端在
    // 发布时如实拒绝（见 docs/design/galaxy/sdk.md 的第三条红线）。
    // 字节要够大，否则构建器会把它内联成 data: 地址，看不到那个文件。
    writeFileSync(join(root, 'extra.png'), Buffer.alloc(8192, 7))
    writeFileSync(
      join(root, 'src/main.js'),
      "import logo from '../logo.png'\nimport extra from '../extra.png'\ndocument.title = logo + extra\n",
    )
    const out = await buildProject(root)
    expect(listOut(out).filter((file) => file.endsWith('.png'))).not.toEqual([])
  })
})
