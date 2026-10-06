import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'

import { DocsIndexPage } from './DocsIndexPage'

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

/** 索引页挂在一张与生产同形的路由表上：兜底那条用来暴露"被 SPA 接走"的跳转。 */
async function renderPage(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <MemoryRouter initialEntries={['/docs']}>
        <Routes>
          <Route path="/docs" element={<DocsIndexPage />} />
          <Route path="*" element={<p>被 SPA 的兜底接走了</p>} />
        </Routes>
      </MemoryRouter>,
    )
  })
  return container
}

async function clickHref(container: HTMLElement, href: string): Promise<void> {
  const anchor = container.querySelector(`a[href="${href}"]`)
  expect(anchor, `没有指向 ${href} 的入口`).not.toBeNull()
  await act(async () => {
    anchor?.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
  })
}

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
})

describe('文档索引页', () => {
  // 章节清单与路由是两处，加一章要同时改。至少让"索引指向的那条路径"
  // 被钉住：改回 /cli 之类的旧路径会在这里失败。
  it('列出命令行一章，并指向它的真实路径', async () => {
    const container = await renderPage()

    expect(container.textContent).toContain('命令行')

    const hrefs = Array.from(container.querySelectorAll('a')).map((a) => a.getAttribute('href'))
    expect(hrefs).toContain('/docs/cli')
  })

  // 创作与发布是同一条规则下的第二处：索引有它、路由表里也得有它，
  // 只有一处改到位就会表现为"点了这一项回到首页"。
  it('列出创作与发布一章，并指向它的真实路径', async () => {
    const container = await renderPage()

    expect(container.textContent).toContain('创作与发布')

    const hrefs = Array.from(container.querySelectorAll('a')).map((a) => a.getAttribute('href'))
    expect(hrefs).toContain('/docs/galaxy')
  })

  // 通道那一章是同一规则下的第三处：索引有它、路由表里也得有它，
  // 只有一处改到位同样会表现为"点了这一项回到首页"。
  it('列出页面与外壳的通道一章，并指向它的真实路径', async () => {
    const container = await renderPage()

    expect(container.textContent).toContain('页面与外壳的通道')

    const hrefs = Array.from(container.querySelectorAll('a')).map((a) => a.getAttribute('href'))
    expect(hrefs).toContain('/docs/frame-bridge')
  })

  // 接口参考是**外链**：它不在路由表里，走 React Router 的 <Link> 会被 `*`
  // 那条兜底接住，表现为"点了接口文档却回到首页"，且没有任何报错。
  // 只断言 href 区分不出这两者（两者渲染出来的都是 <a href>），所以这里
  // 点一下：真跳转不会走到路由的兜底，被 SPA 接住才会。
  it('接口参考是一条跳出去的外链，不被 SPA 接走', async () => {
    const container = await renderPage()

    const hrefs = Array.from(container.querySelectorAll('a')).map((a) => a.getAttribute('href'))
    expect(hrefs).toContain('/api-docs/')

    await clickHref(container, '/api-docs/')

    expect(container.textContent).not.toContain('被 SPA 的兜底接走了')
  })
})
