import { createBrowserRouter } from 'react-router-dom'

import { routes } from './routes'

/**
 * 浏览器路由。
 *
 * 表在 [routes.tsx](./routes.tsx)，实例在这里——分成两份是**预渲染**要求的：
 * 那张表要能在 Node 里建一棵内存路由（同一棵树才能渲染出同一份 HTML），而
 * `createBrowserRouter` 在 Node 里会直接抛错。
 */
export const router = createBrowserRouter(routes)
