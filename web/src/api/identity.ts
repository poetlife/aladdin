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
