import type { AuthMethod, GetAuthMethodsResponse } from '../gen/proto/aladdin/identity/v1/identity_pb'
import { traceCallOptions, type TraceCapture } from './call-trace'
import { identityClient } from './transport'

/**
 * 身份认证面的调用封装。
 *
 * 请求与响应的类型全部来自 proto 生成（web/src/gen/proto/），
 * 这里只做"把一次调用包成有名字的函数"这一件事。
 */

export type { AuthMethod, GetAuthMethodsResponse }

/**
 * 登录并换取访问凭证。这是公开方法，不需要凭证即可调用。
 *
 * `trace` 是这次调用的链路标识捕获（见 ./call-trace）：登录页要上报一条
 * `auth.login` 事件，失败与成功都该能指回这一次请求。
 */
export async function login(token: string, trace?: TraceCapture) {
  return identityClient().login(
    {
      credential: { case: 'token', value: { token } },
    },
    traceCallOptions(trace),
  )
}

/** 查询当前凭证对应的主体。用于校验凭证是否仍然有效。 */
export async function whoAmI() {
  return identityClient().whoAmI({})
}

/**
 * 查询当前作用域下生效的权限码集合。
 *
 * 服务端返回的**已是展开结果**（含继承、通配、作用域包含），
 * 前端直接做集合成员判断即可，不得自行实现展开逻辑。
 */
export async function getSessionPermissions(scope: string) {
  return identityClient().getSessionPermissions({ scope })
}

/**
 * 渠道来源标识。
 *
 * 它们与服务端的 `identity.Source*` 一一对应。前端只在"该渲染哪个入口、
 * 该往哪个地址跳"这件事上用它们——**身份归属与任何判定都不在前端**。
 *
 * 两个渠道都是重定向型：登录与绑定都只由一次浏览器导航发起，没有"把渠道凭证
 * 随 RPC 交给服务端"的入口（见 docs/design/identity/channel-login.md）。
 */
export const AuthSource = {
  Google: 'google',
  Github: 'github',
} as const

/**
 * 查询服务端当前启用了哪些登录方式。
 *
 * 公开方法，不需要凭证——调用方尚未认证，而这正是它要回答的问题的前提。
 *
 * **它是"有哪些登录方式"的唯一来源**：返回什么就渲染什么，未启用的渠道不在
 * 返回值里，前端因此不渲染对应入口（见 docs/design/identity/channel-login.md）。
 * 前端**不得**把渠道清单或客户端标识写进构建产物：那是服务端配置的派生结果，
 * 编一份进来就会与配置漂移，而漂移的表现是"界面上有个入口，点了一直报错"。
 */
export async function getAuthMethods(): Promise<AuthMethod[]> {
  return (await getAuthOptions()).methods
}

/**
 * 服务端下发的**全部登录选项**：已启用的渠道清单、命令行登录是否可用，以及
 * 站点的准入姿态（见 docs/design/identity/registration.md）。
 *
 * 登录页用它一次拿到三样东西。`getAuthMethods` 是它的一个投影，供只关心渠道
 * 清单的地方使用（个人资料页的渠道区）。
 *
 * 准入姿态是**公开**取值：它本来就会展示给任何一个打开登录页的人。它只说准入，
 * 不说进来拿什么——默认角色是权限信息，不在这里下发。
 */
export async function getAuthOptions(): Promise<GetAuthMethodsResponse> {
  return identityClient().getAuthMethods({})
}

/** 列出当前主体已绑定的全部登录渠道。 */
export async function listIdentities() {
  return identityClient().listIdentities({})
}

/**
 * 兑换一份"待绑定凭据"（重定向型渠道的绑定）。
 *
 * 渠道凭证已经由服务端在浏览器回调里校验过，并记成一份只在 HttpOnly cookie
 * 里的待绑定凭据；本调用只带 source，用来与凭据里记下的来源互相印证。
 * **目标主体不在请求里**：服务端只认当前会话代表的主体。
 */
export async function completeIdentityBinding(source: string) {
  return identityClient().completeIdentityBinding({ source })
}

/** 从当前主体上摘掉一个登录渠道。 */
export async function unbindIdentity(source: string, externalId: string) {
  return identityClient().unbindIdentity({ source, externalId })
}

/**
 * 批准一次命令行的设备码登录。
 *
 * 归属只由**当前会话**决定：请求里只有短码，没有主体——不存在"替某个主体
 * 批准"的形状。若存在，任何拿到别人短码的人都能让别人的终端登进自己指定的
 * 账号（见 docs/design/identity/device-login.md）。
 *
 * 交付的会话其作用域是当前主体既有的默认作用域快照，页面不指定、也无从指定。
 */
export async function approveDeviceLogin(userCode: string) {
  return identityClient().approveDeviceLogin({ userCode })
}

/** 拒绝一次命令行的设备码登录。与批准同一条归属规则。 */
export async function denyDeviceLogin(userCode: string) {
  return identityClient().denyDeviceLogin({ userCode })
}
