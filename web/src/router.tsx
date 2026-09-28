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
          {
            element: <RequirePermission require={[PermissionCodes.RbacRoleRead]} />,
            children: [{ path: '/roles', element: <RolesPage /> }],
          },
        ],
      },
    ],
  },
  { path: '*', element: <Navigate to="/" replace /> },
])
