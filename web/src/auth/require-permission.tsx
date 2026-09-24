import { Navigate, Outlet, useLocation } from 'react-router-dom'
import { Spin } from 'antd'

import type { PermissionCode } from '../gen/permission-codes'
import { useAnyPermission } from './use-permission'
import { useSession } from './session'

interface RequirePermissionProps {
  /** 进入该路由所需的基础权限，任意一个满足即可。 */
  require: readonly PermissionCode[]
}

/**
 * 路由级权限裁剪。
 *
 * 依赖的是"该页面所需的基础权限"这一显式清单，而不是页面里所有控件权限的并集——
 * 后者会让"加一个新按钮"意外地把整个页面的准入条件抬高。
 */
export function RequirePermission({ require }: RequirePermissionProps): React.ReactNode {
  const { status } = useSession()
  const allowed = useAnyPermission(require)
  const location = useLocation()

  if (status === 'loading') {
    return <Spin style={{ display: 'block', marginTop: 120 }} />
  }
  if (status === 'anonymous') {
    // 记住来路，登录后回到原页面。
    return <Navigate to="/login" replace state={{ from: location.pathname }} />
  }
  if (!allowed) {
    // 明确跳转到无权限页，而不是静默回首页——后者会让用户以为点击失灵。
    return <Navigate to="/forbidden" replace />
  }
  return <Outlet />
}
