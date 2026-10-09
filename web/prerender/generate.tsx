/**
 * 预渲染：把公开文档区渲染成**带正文的静态 HTML**，并把送给 agent 的产物写进产物目录。
 *
 * 这个文件跑在 Node 里（`vite build --ssr` 打出来之后由 node 执行），不是浏览器代码。
 * "哪些路径是公开的、head 写什么、写到哪个文件"在 public-pages.ts，"一页怎么渲染"
 * 在 render.tsx，这里只负责把它们串起来写盘。
 *
 * 为什么要有它：文档区既给人看，也给 agent 看。只发一个 SPA 空壳的话，任何不执行 JS
 * 的抓取方拿到的都是 `<div id="root"></div>`——正文一个字都没有
 *（见 docs/design/web/agent-readable.md）。
 *
 * 它**只渲染公开路径**：需要登录的页面不预渲染，它们的正文依赖于"我是谁"，
 * 渲染出来要么是空的、要么是错的。
 */
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'

import { docsArtifacts } from '../src/pages/docs/chapters/artifacts'
import { fillTemplate, htmlFileFor, pageMeta, publicPaths } from './public-pages'
import { renderPage } from './render'

/** 写一个文件，目录不存在就建。 */
function writeFile(root: string, path: string, source: string): void {
  const file = join(root, path)
  mkdirSync(dirname(file), { recursive: true })
  writeFileSync(file, source)
}

/**
 * 入口。产物目录由命令行给出（`npm run build` 传 `dist`），因此这里不认识任何具体路径。
 */
function main(): void {
  const distDir = process.argv[2] ?? 'dist'
  const template = readFileSync(join(distDir, 'index.html'), 'utf8')

  for (const path of publicPaths()) {
    writeFile(distDir, htmlFileFor(path), fillTemplate(template, pageMeta(path), renderPage(path)))
    console.log(`>>> 预渲染 ${path}`)
  }

  for (const artifact of docsArtifacts()) {
    writeFile(distDir, artifact.path, artifact.source)
    console.log(`>>> 产出 ${artifact.path}`)
  }
}

main()
