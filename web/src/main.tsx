import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import 'antd/dist/reset.css'

import { App } from './App'
import { tokenStorage } from './auth'
import { createTransport, setTransport } from './api/transport'
import { router } from './router'

// 传输层在应用启动时初始化一次：它是全部出站请求的唯一入口，
// 凭证的读取方式也只在这里声明。
//
// baseUrl 留空表示同源：Connect handler 挂在过程名本身
// （/aladdin.<pkg>.<Service>/<Method>），没有统一前缀。
// 开发期由 vite 的 proxy 转发到后端，见 vite.config.ts。
setTransport(
  createTransport({
    baseUrl: '',
    getToken: () => tokenStorage.read(),
  }),
)

/**
 * 撤掉预渲染那一份样式。
 *
 * 文档区那几章是**构建时预渲染**出来的，静态 HTML 里带着正文（见
 * docs/design/web/agent-readable.md），也带着一份当时抽出来的样式——而它只能是
 * **亮色**的：构建时不知道访问者选了哪一档主题。
 *
 * 这份样式必须撤掉，理由在 cssinjs 的插入位置：运行期它把自己的样式插在 `head`
 * **最前面**，于是静态那一份永远排在它后面；两者的选择器都是 `:where(...)`
 * （零特异性），排在后面的赢。不撤的话，**暗色访问者看到的是一张亮色的页面，而且
 * 怎么切都切不回来**——运行期注入的那一套变量被永久压在下面。
 *
 * 其余路径没有这一块（它们的 HTML 只有一个空壳），因此这里是空操作。
 */
function prunePrerenderedStyles(): void {
  for (const style of document.querySelectorAll('style[data-prerender]')) {
    style.remove()
  }
}

const container = document.getElementById('root')
if (container === null) {
  throw new Error('未找到挂载点 #root')
}

prunePrerenderedStyles()

// **不 hydrate。** 预渲染出来的静态 HTML 是给不跑 JS 的读者与抓取方看的；跑到这里
// 说明 JS 可用，那就整棵重新渲染。
//
// 曾经这里按 `container.hasChildNodes()` 分岔、有内容时走 `hydrateRoot`，实测不成立：
// 静态 HTML 里的内联样式是按亮色定下的字面值（`background:#ffffff`），水合时
// 客户端的取值可能不同（暗色下是 `rgb(20,20,20)`），而 React 不会回头改这些属性——
// 于是暗色访问者停在亮色上，只能靠一次交互才刷新过来。要让水合对得上，就得让客户端
// 第一帧故意渲染成亮色、挂载后再改，那等于把"第一帧说的不是实话"写进主题那一层，
// 而收益只是省下重建这十几个节点。
createRoot(container).render(
  <StrictMode>
    <App router={router} />
  </StrictMode>,
)
