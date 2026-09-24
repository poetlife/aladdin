import { ConnectError, Code } from '@connectrpc/connect'

import { DenialDetailSchema } from '../gen/proto/aladdin/rbac/v1/errors_pb'
import { DenialReason } from '../gen/proto/aladdin/rbac/v1/errors_pb'

/**
 * 拒绝原因枚举。
 *
 * 它从 proto 生成，与 Go 侧的 rbac.Reason 是**同一份定义**。
 * 前端不得在此手写第二份枚举：两边对同一个原因的拼写不一致，
 * 会表现为"把权限不足当成网络错误"这类很难定位的问题。
 */
export { DenialReason }

/** 服务端返回的结构化错误。 */
export { ConnectError, Code }

/**
 * 从错误中取出服务端给出的拒绝原因。
 *
 * 走生成的消息 schema 解码错误详情，**不做错误文本匹配**——
 * 文本会变，枚举取值是契约。
 *
 * 返回 undefined 表示该错误不是服务端发出的鉴权拒绝
 * （网络中断、解码失败、服务端内部错误等）。
 */
export function denialReasonOf(error: unknown): DenialReason | undefined {
  const connectError = ConnectError.from(error)
  const details = connectError.findDetails(DenialDetailSchema)
  return details[0]?.reason
}

/** 报告是否为"未认证"类失败：调用方应走会话失效流程。 */
export function isUnauthenticated(error: unknown): boolean {
  return ConnectError.from(error).code === Code.Unauthenticated
}

/** 报告是否为"权限不足"类失败：调用方不应重试。 */
export function isPermissionDenied(error: unknown): boolean {
  return ConnectError.from(error).code === Code.PermissionDenied
}

/** 报告是否值得退避重试。 */
export function isRetryable(error: unknown): boolean {
  const code = ConnectError.from(error).code
  return code === Code.Unavailable || code === Code.DeadlineExceeded
}

/**
 * 取一条适合直接展示给用户的错误文案。
 *
 * 是鉴权拒绝就给出原因对应的解释，否则退回服务端原始消息。
 * 页面统一用它，不再各自写 `err instanceof Error ? err.message : String(err)`。
 */
export function messageOf(error: unknown): string {
  if (denialReasonOf(error) !== undefined) {
    return describeDenial(error)
  }
  return ConnectError.from(error).rawMessage
}

/**
 * 把拒绝原因转成对用户可读的提示。
 *
 * 与 CLI 的 describeDenial 语义一致（见 cmd/aladdin/permission-decl.go）：
 * 两端对同一原因给出同构的提示，用户不会因为换个入口就得到不同的解释。
 */
export function describeDenial(error: unknown): string {
  const reason = denialReasonOf(error)
  switch (reason) {
    case DenialReason.SCOPE_MISMATCH:
      return '当前作用域下权限不足，请切换到更高层级的作用域'
    case DenialReason.NO_MATCHING_GRANT:
      return '权限不足。若认为这是误判，请联系管理员核对角色授权'
    case DenialReason.SESSION_EXPIRED:
      return '凭证已失效，请重新登录'
    case DenialReason.SUBJECT_NOT_FOUND:
      return '主体不存在或已停用，请联系管理员'
    case DenialReason.STORE_UNAVAILABLE:
      return '权限服务暂时不可用，请稍后重试'
    case DenialReason.ANNOTATION_MISSING:
      return '服务端接口配置有误，请联系服务端开发'
    default:
      return ConnectError.from(error).rawMessage
  }
}
