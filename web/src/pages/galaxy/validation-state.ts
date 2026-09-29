import type { ValidationProblem } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'

/**
 * 校验结论的状态。
 *
 * **"正文有问题"与"校验没跑成"是两件事**，因此分成 `problems` 与 `failed` 两档：
 * 前者是服务端给出的结论（用户要照着改），后者是这次调用本身没拿到结论
 * （网络、权限——用户要重试）。把后者渲染成前者，会让用户以为自己的工程坏了
 * （见 docs/design/galaxy/authoring.md 的"能不能发布是一个自动产生的状态"）。
 *
 * `stale` 是第三种情形：正文被改过而还没保存。此时上一次的结论描述的是**另一份字节**，
 * 继续显示它等于给出一个不成立的保证。
 */
export interface ValidationState {
  status: 'pending' | 'ok' | 'problems' | 'failed' | 'stale'
  problems: readonly ValidationProblem[]
}

/** 正在等结论：页面打开时与重新校验期间都是它。 */
export const VALIDATION_PENDING: ValidationState = { status: 'pending', problems: [] }

/**
 * 正文被改动、结论待重新产生时的状态。
 *
 * 保存草稿之后页面会重新问一次服务端，因此它不会久留。
 */
export const VALIDATION_STALE: ValidationState = { status: 'stale', problems: [] }

/** 结论的色调，取值对应 antd `Typography.Text` 的 type。 */
export type ValidationTone = 'secondary' | 'success' | 'warning'

/**
 * 校验结论的文案与色调的**唯一入口**。
 *
 * 状态条与侧栏概览呈现的是同一份结论，两边各写一句文案就会出现"同一件事两种说法"
 * ——这正是 SSOT 原则要避免的（见 docs/ssot-registry.md）。
 */
export function describeValidation(state: ValidationState): {
  tone: ValidationTone
  text: string
} {
  switch (state.status) {
    case 'pending':
      return { tone: 'secondary', text: '校验中…' }
    case 'ok':
      return { tone: 'success', text: '可以发布' }
    case 'problems':
      return { tone: 'warning', text: `${state.problems.length} 处问题` }
    case 'failed':
      return { tone: 'warning', text: '校验未完成' }
    case 'stale':
      return { tone: 'secondary', text: '有改动，保存后重新校验' }
  }
}
