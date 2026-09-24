import type { PermissionCode } from '../gen/permission-codes'
import { useSession } from './session'
import { PermissionSet } from './permission-set'

/**
 * 报告当前会话是否持有指定权限。
 *
 * **这不是安全边界**：它只决定"要不要渲染"。真正的判定在服务端，
 * 隐藏一个按钮不阻止任何人直接调用接口（见 docs/design/rbac/frontend-permissions.md）。
 *
 * 判定语义刻意保持为单纯的集合成员测试——继承、通配与作用域包含
 * 已由服务端在返回权限码集合时展开完毕。
 */
export function usePermission(code: PermissionCode): boolean {
  const { permissions } = useSession()
  return permissions.has(code)
}

/** 报告是否持有其中任意一个权限。 */
export function useAnyPermission(codes: readonly PermissionCode[]): boolean {
  const { permissions } = useSession()
  return permissions.hasAny(codes)
}

/** 报告是否持有全部权限。 */
export function useAllPermissions(codes: readonly PermissionCode[]): boolean {
  const { permissions } = useSession()
  return permissions.hasAll(codes)
}

/** 取回原始权限码集合，用于展示与排查。 */
export function usePermissionSet(): PermissionSet {
  const { permissions } = useSession()
  return permissions
}
