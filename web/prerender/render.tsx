import { createCache, extractStyle, StyleProvider } from '@ant-design/cssinjs'
import { renderToString } from 'react-dom/server'
import { createMemoryRouter } from 'react-router-dom'

import { App } from '../src/App'
import { routes } from '../src/routes'
import type { RenderedPage } from './public-pages'

/**
 * 在 Node 里把一页渲染成 HTML。
 *
 * **用的是与浏览器同一张路由表**（`routes.tsx`）与同一棵组件树（`App`），因此两侧
 * 出来的是同一份东西——这里不另写一套"静态版页面"，那会是同一件事的第二份实现，
 * 两份必然漂。
 *
 * 样式走 cssinjs 的缓存抽出来、随 HTML 一起发出去：不抽的话，静态 HTML 是一份
 * **没有样式**的正文，不执行 JS 的读者看到的是浏览器默认排版，而"预渲染过"这件事
 * 就看不出好处了。抽出来的这一份只有一档主题（构建时不知道访问者选了哪一档），
 * 因此客户端挂载时**必须把它撤掉**，见 src/main.tsx。
 *
 * 抽出来的类名要与客户端那一份一致，否则等于白抽。antd 的类名里带不带 dev 标记由
 * `NODE_ENV` 决定，因此调用方必须按生产模式跑（见 package.json 的 prerender 脚本）。
 */
export function renderPage(path: string): RenderedPage {
  const cache = createCache()
  const router = createMemoryRouter(routes, { initialEntries: [path] })
  const html = renderToString(
    <StyleProvider cache={cache}>
      <App router={router} />
    </StyleProvider>,
  )
  return { html, css: extractStyle(cache, { plain: true }) }
}
