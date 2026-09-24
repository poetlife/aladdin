import { rbacClient } from './transport'

/**
 * 权限管理面的调用封装。
 *
 * 与 identity.ts 同理：类型来自 proto 生成，这里只提供有名字的调用入口。
 */

/** 列出指定作用域下可见的角色。 */
export async function listRoles(scope: string) {
  return rbacClient().listRoles({ scope })
}
