import { createBrowserRouter, Navigate } from 'react-router-dom'

import { RequirePermission } from './auth'
import { AppLayout } from './layouts/AppLayout'
import { ForbiddenPage } from './pages/ForbiddenPage'
import { HomePage } from './pages/HomePage'
import { LoginPage } from './pages/LoginPage'
import { RolesPage } from './pages/RolesPage'
import { PermissionCodes } from './gen/permission-codes'

/**
 * 路由表。
 *
 * 每个受保护路由通过 `require` 显式声明其**基础权限**——
 * 这是页面准入条件的唯一声明位置，不散落在页面内部。
 */
export const router = createBrowserRouter([
  { path: '/login', element: <LoginPage /> },
  { path: '/forbidden', element: <ForbiddenPage /> },
  {
    element: <RequirePermission require={[PermissionCodes.RbacRoleRead, PermissionCodes.RbacSubjectRead]} />,
    children: [
      {
        element: <AppLayout />,
        children: [
          { path: '/', element: <HomePage /> },
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
