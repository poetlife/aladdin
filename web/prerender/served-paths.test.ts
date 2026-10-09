import { describe, expect, it } from 'vitest'

import { servedArtifactPath } from './served-paths'

describe('哪一份产物回答这个请求', () => {
  it('三份产物各自对上', () => {
    expect(servedArtifactPath('/llms.txt', undefined)).toBe('/llms.txt')
    expect(servedArtifactPath('/llms-full.txt', undefined)).toBe('/llms-full.txt')
    expect(servedArtifactPath('/docs/cli.md', undefined)).toBe('/docs/cli.md')
  })

  // 找不到时由调用方给 404。**这条是"未知路径不再兜底成 200 首页"的前提**：
  // 判决必须落到这里，才能轮到"没这份产物"那一步，而不是让 SPA 兜底接走。
  it('未知的 .md / .txt 也归这里管（由调用方给 404）', () => {
    expect(servedArtifactPath('/not-exist.md', undefined)).toBe('/not-exist.md')
    expect(servedArtifactPath('/nope.txt', undefined)).toBe('/nope.txt')
  })

  // Vite 把章节源读进模块图的请求也走同一个中间件。当成站点路径毙掉的话，开发环境
  // 下整个文档区都是 404——而构建产物里没有这些请求，只在 dev 复现。
  it('Vite 自己的模块请求不归这里管', () => {
    expect(servedArtifactPath('/src/pages/docs/chapters/cli.md', '')).toBeNull()
    expect(servedArtifactPath('/node_modules/foo/readme.md', '')).toBeNull()
    expect(servedArtifactPath('/@fs/somewhere/x.md', '')).toBeNull()
  })

  it('要 Markdown 时章这一级直接给 .md', () => {
    expect(servedArtifactPath('/docs/cli', 'text/markdown')).toBe('/docs/cli.md')
    expect(servedArtifactPath('/docs/frame-bridge', 'text/html, text/markdown;q=0.9')).toBe(
      '/docs/frame-bridge.md',
    )
  })

  it('没有请求头时不动', () => {
    expect(servedArtifactPath('/docs/cli', undefined)).toBeNull()
    expect(servedArtifactPath('/docs/cli', 'text/html')).toBeNull()
  })

  // 放开到任意路径，会让一个带该请求头的浏览器把整个应用取成 404——而"取回一份
  // Markdown"这件事只对章有意义。
  it('只有章这一级做内容协商', () => {
    expect(servedArtifactPath('/', 'text/markdown')).toBeNull()
    expect(servedArtifactPath('/docs', 'text/markdown')).toBeNull()
    expect(servedArtifactPath('/docs/cli/extra', 'text/markdown')).toBeNull()
    expect(servedArtifactPath('/settings', 'text/markdown')).toBeNull()
  })
})
