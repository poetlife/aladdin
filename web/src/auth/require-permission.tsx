import { Navigate, Outlet, useLocation } from 'react-router-dom'
import { Spin } from 'antd'

import type { PermissionCode } from '../gen/permission-codes'
import { useAnyPermission } from './use-permission'
import { useSession } from './session'

interface RequirePermissionProps {
  /**
   * 进入该路由所需的基础权限，任意一个满足即可。
   *
   * **不声明表示"仅需已认证"**：自服务页面（个人资料）的准入条件不是某个
   * 权限码——它只作用于调用者自己，因此零权限的主体也必须进得来。这正是
   * identity 那条"首次登录是成功的，界面是空的"成功标准的落点。
   */
  require?: readonly PermissionCode[]
}

/**
 * 路由级准入裁剪。
 *
 * 依赖的是"该页面所需的基础权限"这一显式清单，而不是页面里所有控件权限的并集——
 * 后者会让"加一个新按钮"意外地把整个页面的准入条件抬高。
 *
 * 它同时收着"未认证"与"无权限"两种拒绝，且**两者走向不同**：前者跳登录页并
 * 记住来路，后者跳无权限页。把它们分开而不是都跳首页，是因为"看起来像点击失灵"
 * 比一次明确的拒绝难排查得多。
 */
export function RequirePermission({ require }: RequirePermissionProps): React.ReactNode {
  const { status } = useSession()
  const required = require ?? []
  const hasPermission = useAnyPermission(required)
  const location = useLocation()

  // 未声明基础权限 = 只要已认证。不能靠"空数组让 some 恒为 false"来表达这件事
  // ——那会把一个语义写成一次巧合，而 hooks 不能条件调用，所以在这里显式短路。
  const allowed = required.length === 0 || hasPermission

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
