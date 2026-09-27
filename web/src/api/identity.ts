import type { AuthMethod } from '../gen/proto/aladdin/identity/v1/identity_pb'
import { identityClient } from './transport'

/**
 * 身份认证面的调用封装。
 *
 * 请求与响应的类型全部来自 proto 生成（web/src/gen/proto/），
 * 这里只做"把一次调用包成有名字的函数"这一件事。
 */

export type { AuthMethod }

/** 登录并换取访问凭证。这是公开方法，不需要凭证即可调用。 */
export async function login(token: string) {
  return identityClient().login({
    credential: { case: 'token', value: { token } },
  })
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
  const resp = await identityClient().getAuthMethods({})
  return resp.methods
}

/**
 * 用 Google 签发的身份令牌换取会话凭证。
 *
 * 令牌由 Google 在浏览器内签发（见 ../auth/google-identity），这里只是搬运：
 * 它的可信度完全由服务端校验，前端的任何字段都不参与信任决策。
 */
export async function loginWithGoogle(idToken: string) {
  return identityClient().login({
    credential: { case: 'google', value: { idToken } },
  })
}

/** 列出当前主体已绑定的全部登录渠道。 */
export async function listIdentities() {
  return identityClient().listIdentities({})
}

/**
 * 用 Google 签发的身份令牌把该渠道绑到**当前主体**。
 *
 * 这是搬运型渠道的绑定：令牌直接随请求到达服务端，由与登录相同的校验器
 * 校验；归属只取当前会话的主体，请求里没有主体字段。
 */
export async function bindGoogleIdentity(idToken: string) {
  return identityClient().bindIdentity({
    credential: { case: 'google', value: { idToken } },
  })
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
