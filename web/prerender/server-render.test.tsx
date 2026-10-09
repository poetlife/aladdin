import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { createMemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'

import { App } from '../src/App'
import { CHAPTERS } from '../src/pages/docs/chapters/manifest'
import { routes } from '../src/routes'
import { publicPaths } from './public-pages'
import { renderPage } from './render'

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

afterEach(() => {
  document.body.replaceChildren()
})

/** 一份 HTML 里的可见文字。比较的是**读者读到的东西**，不是标记。 */
function textOf(html: string): string {
  const container = document.createElement('div')
  container.innerHTML = html
  return visibleText(container)
}

/** 去掉空白：两条路径的换行与缩进不必逐字节相同。 */
function visibleText(element: HTMLElement): string {
  return (element.textContent ?? '').replace(/\s+/g, '')
}

/** 在浏览器那一侧渲染同一页（挂载，不是水合——见 src/main.tsx）。 */
async function clientText(path: string): Promise<string> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(<App router={createMemoryRouter(routes, { initialEntries: [path] })} />)
  })
  const text = visibleText(container)
  await act(async () => {
    root.unmount()
  })
  return text
}

describe('预渲染出来的那一份', () => {
  // 这条是整件事的目的：不执行 JS 的抓取方拿到的必须是**正文**，不是
  // `<div id="root"></div>`（见 docs/design/web/agent-readable.md）。
  it('每一页的 HTML 里都有可见正文', () => {
    for (const path of publicPaths()) {
      const text = textOf(renderPage(path).html)

      expect(text.length, `${path} 只是空壳`).toBeGreaterThan(100)
    }
  })

  it('索引页的 HTML 里列着每一章', () => {
    const text = textOf(renderPage('/docs').html)

    for (const chapter of CHAPTERS) {
      expect(text).toContain(chapter.title)
    }
  })

  it('章节的 HTML 里有正文里的句子', () => {
    const text = textOf(renderPage('/docs/cli').html)

    expect(text).toContain('aladdin_${tag}_darwin_arm64.tar.gz')
    expect(text).toContain('装进用户可写目录')
  })

  // **这是同源那件事在呈现层的执行者。** 静态 HTML 与客户端渲染的是同一棵树
  // （同一张路由表、同一个 App），因此两条路径读到的字一模一样——正文若在某处
  // 多了一句、少了一句，这里会失败。
  it('不跑 JS 的读者与跑 JS 的读者读到同一段正文', async () => {
    for (const path of publicPaths()) {
      expect(textOf(renderPage(path).html), `${path} 两侧正文不同`).toBe(await clientText(path))
    }
  })

  // 抽出来的样式要随 HTML 一起发出去，否则不执行 JS 的读者看到的是浏览器默认排版
  // （见 render.tsx）。标记本身由 fillTemplate 加上，那条在 public-pages.test.ts。
  it('一页会抽出一份样式', () => {
    expect(renderPage('/docs/cli').css.length).toBeGreaterThan(0)
  })
})
