import { identityClient } from './transport'

/**
 * 身份认证面的调用封装。
 *
 * 请求与响应的类型全部来自 proto 生成（web/src/gen/proto/），
 * 这里只做"把一次调用包成有名字的函数"这一件事。
 */

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
 * 查询服务端当前启用了哪些登录方式。
 *
 * 公开方法，不需要凭证——调用方尚未认证，而这正是它要回答的问题的前提。
 *
 * 前端**不得**把客户端标识写进构建产物。它是服务端配置的派生结果，编一份
 * 进来就会与配置漂移，而漂移的表现是"改了服务端配置，前端还在用旧的"。
 */
export async function getAuthMethods() {
  return identityClient().getAuthMethods({})
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
