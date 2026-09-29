/**
 * 管理范围的显示文案。
 *
 * 模型里空串就是「全局」：界面上**一律**显示「全局」，`<global>` 这类内部写法
 * 不面向使用者。
 *
 * 这对转换必须是唯一的：谁各自写一份 `scope === '' ? '全局' : scope`，
 * 就会有人漏掉其中一侧，于是同一个范围在不同页面上写法不同。
 *
 * 注意它与"请求里带空范围"不是一回事：管理面接口把空范围当成全局，
 * 而会话查询把空范围当成"不指定、回落到凭证默认范围"（见
 * docs/design/rbac/management-ui.md）。
 */

/** 全局范围的界面文案。 */
export const GLOBAL_SCOPE_LABEL = '全局'

/** 范围值 → 界面文案。 */
export function formatScope(scope: string): string {
  return scope === '' ? GLOBAL_SCOPE_LABEL : scope
}

/** 用户输入 → 范围值。输入「全局」或留空都表示全局。 */
export function parseScope(input: string): string {
  const trimmed = input.trim()
  return trimmed === GLOBAL_SCOPE_LABEL ? '' : trimmed
}
