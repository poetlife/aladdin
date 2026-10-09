import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { SessionProvider } from './auth'
import { ThemeProvider } from './theme'
import { CHAPTERS } from './pages/docs/chapters/manifest'
import { routes } from './routes'

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

/**
 * 按**真实路由表**渲染一条路径，且**不带任何凭证**。
 *
 * 这就是"未认证"那一侧：`localStorage` 里没有令牌，`SessionProvider` 因此直接落到
 * 匿名态，一次接口都不会调。文档区若还被挂在 `RequirePermission` 之下，这里就会
 * 跳到登录页——那正是这一组测试要挡住的事。
 */
async function renderAnonymous(path: string): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  const router = createMemoryRouter(routes, { initialEntries: [path] })
  await act(async () => {
    root?.render(
      <ThemeProvider>
        <SessionProvider>
          <RouterProvider router={router} />
        </SessionProvider>
      </ThemeProvider>,
    )
  })
  return container
}

beforeEach(() => {
  globalThis.localStorage?.clear()
})

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
})

describe('文档区的准入', () => {
  // 它讲的是"怎么把命令行装上并登录"。留在登录墙后面，"先装命令行才能登录、
  // 登录了才看得到怎么装命令行"这个环就闭不上（见 docs/design/web/docs-area.md）。
  it('未登录也能读一章', async () => {
    const container = await renderAnonymous('/docs/cli')

    expect(container.textContent).toContain('aladdin_${tag}_darwin_arm64.tar.gz')
  })

  it('未登录也能读索引，索引列出每一章', async () => {
    const container = await renderAnonymous('/docs')

    for (const chapter of CHAPTERS) {
      expect(container.textContent).toContain(chapter.title)
    }
  })

  // 放出去给它的是**自己的一层外壳**，不是把应用外壳放宽：账号区与管理范围回答的是
  // "我是谁、我在哪个范围下工作"，而访客没有这两样东西（见 PublicDocsLayout.tsx）。
  it('公开外壳里没有应用侧边栏，也没有管理范围', async () => {
    const container = await renderAnonymous('/docs/cli')

    expect(container.querySelector('.ant-layout-sider')).toBeNull()
    expect(container.querySelector('.ant-drawer')).toBeNull()
    expect(container.textContent).not.toContain('管理范围')
  })

  // 登录用户从侧边栏点「文档」也会落到这层外壳上，因此"怎么回去"必须写在这里，
  // 否则他只能按浏览器后退。
  it('公开外壳里有一条回应用的入口', async () => {
    const container = await renderAnonymous('/docs/cli')

    const back = [...container.querySelectorAll('a')].find((a) => a.textContent?.includes('进入应用'))
    expect(back, '公开外壳里没有回应用的入口').not.toBeUndefined()
    expect(back?.getAttribute('href')).toBe('/')
  })

  it('公开外壳里的导航能到每一章', async () => {
    const container = await renderAnonymous('/docs')

    const hrefs = [...container.querySelectorAll('a')].map((a) => a.getAttribute('href'))
    for (const chapter of CHAPTERS) {
      expect(hrefs).toContain(`/docs/${chapter.slug}`)
    }
  })

  // 「放出去」改的是准入，不是宿主：正文仍然长在同一个外壳里（不是另一张静态页），
  // 因此它照常跟主题走、深链照常直达。
  it('深链直达一章时正文就在这一层外壳里', async () => {
    const container = await renderAnonymous('/docs/galaxy')

    expect(container.textContent).toContain('asset://')
    // 外壳在，正文在它之内。
    expect(container.querySelector('.ant-layout-content')?.textContent).toContain('asset://')
  })

  // 未知的章节回到索引。少了那条兜底，公开外壳里会空出一块——看起来像这一页加载
  // 坏了，而不是"没有这一章"。
  it('未知的章节回到索引，而不是空一块', async () => {
    const container = await renderAnonymous('/docs/does-not-exist')

    for (const chapter of CHAPTERS) {
      expect(container.textContent).toContain(chapter.title)
    }
  })
})
