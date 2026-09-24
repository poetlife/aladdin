import type { ReactNode } from 'react'

import type { PermissionCode } from '../gen/permission-codes'
import { useAnyPermission, usePermission } from './use-permission'

interface PermissionGateProps {
  /** 需要的权限码。传入数组时表示"任意一个"。 */
  require: PermissionCode | readonly PermissionCode[]
  /**
   * 无权限时渲染的内容。
   *
   * 默认为 null，即**不渲染**——界面上不出现灰按钮，
   * 除非确实需要向用户传达"这个功能存在但你没权限"。
   */
  fallback?: ReactNode
  children: ReactNode
}

/**
 * 控件级权限裁剪。
 *
 * 这是前端唯一被允许的控件裁剪入口：页面不得自行写条件判断，
 * 否则同一个权限会在多处出现不同的判定写法。
 */
export function PermissionGate({ require, fallback = null, children }: PermissionGateProps): ReactNode {
  const single = usePermission(typeof require === 'string' ? require : ('' as PermissionCode))
  const multiple = useAnyPermission(typeof require === 'string' ? [] : require)
  return (typeof require === 'string' ? single : multiple) ? children : fallback
}
