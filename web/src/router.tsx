import { createBrowserRouter, Navigate } from 'react-router-dom'

import { RequirePermission } from './auth'
import { AppLayout } from './layouts/AppLayout'
import { AuthCallbackPage } from './pages/AuthCallbackPage'
import { DeviceApprovalPage } from './pages/DeviceApprovalPage'
import { ForbiddenPage } from './pages/ForbiddenPage'
import { HomePage } from './pages/HomePage'
import { LoginPage } from './pages/LoginPage'
import { ProfilePage } from './pages/ProfilePage'
import { RolesPage } from './pages/RolesPage'
import { PermissionCodes } from './gen/permission-codes'
import { CliPage } from './pages/docs/CliPage'
import { DocsIndexPage } from './pages/docs/DocsIndexPage'
import { GalaxyPage } from './pages/docs/GalaxyPage'
import { ProjectEditorPage } from './pages/galaxy/ProjectEditorPage'
import { ProjectListPage } from './pages/galaxy/ProjectListPage'
import { PreviewPage } from './pages/galaxy/PreviewPage'

/**
 * 路由表。
 *
 * 准入分成**两层**，不能合成一层：
 *
 *   - 外壳（含首页与个人资料）只要**已认证**。零权限的主体也必须进得来——
 *     他拿到会话是成功的，只是什么都做不了、界面是空的；而"给自己起个名字"
 *     恰恰是他最需要做、也唯一能做的事（见 docs/design/profile/README.md）。
 *   - 具体的管理页面各自声明**基础权限**，那是页面准入条件的唯一声明位置，
 *     不散落在页面内部。
 *
 * 合成一层会让"零权限"与"无权访问某个管理页"变成同一个结论，而它们该有不同
 * 的走向：前者该看到一个空界面，后者该被明确告知无权访问。
 */
export const router = createBrowserRouter([
  { path: '/login', element: <LoginPage /> },
  // 重定向型登录渠道的回调落点。它必须在 RequirePermission **之外**：
  // 浏览器刚跳回来时凭证还只在地址的 fragment 里，尚未落盘。
  //
  // 它**不在 /auth/ 下**：那一段被反向代理整段转发给服务端（服务端的
  // 回调端点在 /auth/github/），前端在这里放路由会被服务端接走。
  { path: '/login/callback', element: <AuthCallbackPage /> },
  { path: '/forbidden', element: <ForbiddenPage /> },
  {
    element: <RequirePermission />,
    children: [
      {
        element: <AppLayout />,
        children: [
          { path: '/', element: <HomePage /> },
          { path: '/profile', element: <ProfilePage /> },
          // 命令行登录的批准页。它只要**已认证**：零权限的主体也要能批准
          // 自己的登录，因此不挂在任何权限码之下。
          //
          // 地址由服务端拼进对外地址交给终端（internal/server 的
          // DeviceApprovalPath），改这里的路径要同时改那里。
          { path: '/device', element: <DeviceApprovalPage /> },
          // 文档区。它与外壳同属一回事：**文档是前端的一部分**（路由 + 组件），
          // 不是另一个站点。将来"放出去"改变的是准入，不是换宿主
          //（见 docs/design/web/docs-area.md）。
          //
          // 准入只要**已认证**、不要权限码：零权限的主体最需要它——否则
          // "先装命令行才能登录、登录了才看得到怎么装命令行"这个环闭不上。
          { path: '/docs', element: <DocsIndexPage /> },
          { path: '/docs/cli', element: <CliPage /> },
          { path: '/docs/galaxy', element: <GalaxyPage /> },
          {
            element: <RequirePermission require={[PermissionCodes.RbacRoleRead]} />,
            children: [{ path: '/roles', element: <RolesPage /> }],
          },
          // galaxy 创作面。基础权限是 `galaxy.project.read`——列表与编辑器
          // 同属一块能力；编辑器内部的写/发布/资产入口再按各自权限码裁剪。
          {
            element: <RequirePermission require={[PermissionCodes.GalaxyProjectRead]} />,
            children: [
              { path: '/galaxy', element: <ProjectListPage /> },
              { path: '/galaxy/:projectId', element: <ProjectEditorPage /> },
              // 单独打开的预览。工作台里预览与源码共用一块面积、切换着看，因此
              // "改的时候看不见渲染结果"——这一页是那件事的出口，读的仍是草稿。
              // 它只要读权限：看一眼草稿不该要求能改它。
              { path: '/galaxy/:projectId/preview', element: <PreviewPage /> },
            ],
          },
        ],
      },
    ],
  },
  { path: '*', element: <Navigate to="/" replace /> },
])
