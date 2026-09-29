import { rbacClient } from './transport'

/**
 * 权限管理面的调用封装。
 *
 * 与 identity.ts 同理：类型来自 proto 生成，这里只提供有名字的调用入口。
 */

/** 列出角色定义全集。范围只用于鉴权，不过滤结果（见 docs/design/rbac/management-ui.md）。 */
export async function listRoles(scope: string) {
  return rbacClient().listRoles({ scope })
}

/** 列出某个主体的全部角色绑定。绑定不按范围过滤——他在别处持有的也会列出来。 */
export async function listSubjectBindings(scope: string, subjectId: string) {
  return rbacClient().listSubjectBindings({ scope, subjectId })
}

/** 为主体授予（grant=true）或回收（grant=false）一个角色。 */
export async function assignRole(
  scope: string,
  subjectId: string,
  roleId: string,
  grant: boolean,
) {
  return rbacClient().assignRole({ scope, subjectId, roleId, grant })
}

/** 列出已登记的范围。不含全局：它是模型的根，不是目录里的一条。 */
export async function listScopes(scope: string) {
  return rbacClient().listScopes({ scope })
}

/** 登记一个范围，或改它的显示名。路径是标识，传进来是什么就是什么（不可改）。 */
export async function putScope(scope: string, path: string, displayName: string) {
  return rbacClient().putScope({ scope, path, displayName })
}

/** 删除一个范围。范围内或其后代上仍有绑定时服务端拒绝。 */
export async function deleteScope(scope: string, path: string) {
  return rbacClient().deleteScope({ scope, path })
}
