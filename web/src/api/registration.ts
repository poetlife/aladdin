import type {
  Invite,
  RegistrationPolicy,
} from '../gen/proto/aladdin/identity/v1/registration_pb'
import { RegistrationMode } from '../gen/proto/aladdin/identity/v1/registration_pb'
import { registrationClient } from './transport'

/**
 * 注册面的调用封装。
 *
 * 与 identity.ts 同理：类型来自 proto 生成，这里只提供有名字的调用入口。
 * **前端不判准入**：这一页能不能注册、进来拿什么，服务端说了算；这里只把
 * 管理员的改动送过去、把现状读回来。
 */

export type { Invite, RegistrationPolicy }
export { RegistrationMode }

/**
 * 读取当前注册策略。
 *
 * 策略值是站点级的，`scope` 只用于鉴权（"你在哪个范围上有资格读"），与
 * 角色页同一条约定（见 docs/design/rbac/management-ui.md）。
 */
export async function getRegistrationPolicy(scope: string): Promise<RegistrationPolicy> {
  const resp = await registrationClient().getRegistrationPolicy({ scope })
  return resp.policy ?? defaultPolicy()
}

/**
 * 修改注册策略。
 *
 * 默认角色与默认范围**同进同退**：两者都空表示不给默认角色。服务端还会校验
 * 角色必须存在、范围必须已登记，以及这个角色不能自我放大——前端不预判这些，
 * 拿到拒绝原因照实显示即可。
 */
export async function putRegistrationPolicy(
  scope: string,
  mode: RegistrationMode,
  defaultRoleId: string,
  defaultScope: string,
): Promise<RegistrationPolicy> {
  const resp = await registrationClient().putRegistrationPolicy({
    scope,
    mode,
    defaultRoleId,
    defaultScope,
  })
  return resp.policy ?? defaultPolicy()
}

/** 列出已签发的邀请码。**返回值里没有明文**，也读不回来。 */
export async function listInvites(scope: string): Promise<Invite[]> {
  const resp = await registrationClient().listInvites({ scope })
  return resp.invites
}

/**
 * 签发一份邀请码，返回记录与**码的明文**。
 *
 * 明文只在这一个返回值里出现一次：库里存的是摘要，`listInvites` 读不回来。
 * 调用方必须把它显示给管理员，否则这份码就白发了。
 */
export async function createInvite(
  scope: string,
  label: string,
  maxUses: number,
  expiresAt: string,
): Promise<{ invite: Invite; code: string }> {
  const resp = await registrationClient().createInvite({ scope, label, maxUses, expiresAt })
  return { invite: resp.invite as Invite, code: resp.code }
}

/** 撤销一份邀请码。只撤销、不删除：用量与留痕留着。 */
export async function revokeInvite(scope: string, id: string): Promise<Invite> {
  const resp = await registrationClient().revokeInvite({ scope, id })
  return resp.invite as Invite
}

/**
 * 完成一次因邀请码而暂停的注册。
 *
 * 公开方法：调用方正是那个还没登录、也还没有账号的人。**没有目标主体这个参数**
 * ——要登记哪个渠道身份，只由服务端在回调时记下的那份一次性凭据决定；这个请求
 * 里只有一个邀请码。
 *
 * 凭据在 HttpOnly cookie 里，因此这次调用必须带上 cookie（同源默认带上）。
 */
export async function completeRegistration(inviteCode: string) {
  return registrationClient().completeRegistration({ inviteCode })
}

/** 一份缺省策略，用于服务端没有返回内容时兜底。取值与服务端的零值一致。 */
function defaultPolicy(): RegistrationPolicy {
  return {
    $typeName: 'aladdin.identity.v1.RegistrationPolicy',
    mode: RegistrationMode.OPEN,
    defaultRoleId: '',
    defaultScope: '',
    updatedBySubjectId: '',
    updatedAt: '',
  }
}
