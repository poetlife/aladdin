/**
 * Google Identity Services（GIS）的加载与按钮挂载。
 *
 * 这是本仓库对 `accounts.google.com` 的**唯一**接触点。之所以要单独一个模块
 * 而不是在页面里随手写，是因为引入第三方脚本这件事本身需要一个可审查的边界：
 *
 * - **只在真的要用时才加载。** 没启用 Google 登录的部署，登录页不会向 Google
 *   发出任何请求。一个"用不到也先连一次第三方"的登录页是没必要的暴露。
 * - **只加载一次。** 重复挂载（切换路由、重渲染）不会重复插入脚本。
 * - 加载失败不抛出到界面：登录页还有别的方式，为一次第三方脚本的失败弹错
 *   只会让人以为整个登录坏了。
 *
 * 脚本返回的身份令牌**不是**信任来源：它由服务端校验（签名、签发方、受众、
 * 有效期），前端拿不到也不需要知道如何校验。
 */

const GIS_SRC = 'https://accounts.google.com/gsi/client'

/**
 * GIS 在全局挂载 `google` 命名空间。
 *
 * 只声明我们实际用到的部分——把整个第三方类型定义搬进来，等于让它的 API
 * 变更变成我们的编译错误，而我们要的只是"调两个方法"。
 */
interface GoogleIdentityServices {
  accounts: {
    id: {
      initialize(config: { client_id: string; callback: (response: { credential: string }) => void }): void
      renderButton(parent: HTMLElement, options: Record<string, unknown>): void
    }
  }
}

function googleIdentityServices(): GoogleIdentityServices | undefined {
  return (globalThis as { google?: GoogleIdentityServices }).google
}

let loading: Promise<void> | null = null

/**
 * 已经 initialize 过的客户端标识。
 *
 * `initialize` **每个客户端标识只能调一次**：它注册的回调是全局的（不挂在任何
 * DOM 节点上），重复调用时 GIS 只会保留最后一次，并明确警告行为不可预期。
 * 而组件每次挂载都会调到这里，所以"注册"与"挂载"必须是两件事。
 */
let initializedClientId: string | null = null

/** 当前生效的回调。组件重新挂载时换的是这个引用，不是注册本身。 */
let currentHandler: ((idToken: string) => void) | null = null

/** 加载 GIS 脚本；重复调用复用同一个 Promise。 */
function loadGoogleIdentity(): Promise<void> {
  if (googleIdentityServices() !== undefined) {
    return Promise.resolve()
  }
  if (loading !== null) {
    return loading
  }

  loading = new Promise<void>((resolve, reject) => {
    const script = document.createElement('script')
    script.src = GIS_SRC
    script.async = true
    script.onload = () => {
      resolve()
    }
    script.onerror = () => {
      // 失败后清掉缓存的 Promise：下一次挂载应当重试，
      // 否则一次网络抖动会让本次会话永远看不到登录入口。
      loading = null
      reject(new Error('无法加载 Google 登录脚本'))
    }
    document.head.appendChild(script)
  })
  return loading
}

/**
 * 在给定容器里挂载 Google 登录按钮。
 *
 * 拿到身份令牌后回调 `onCredential`——由调用方交给服务端，这里不碰它。
 */
export async function mountGoogleButton(
  parent: HTMLElement,
  clientId: string,
  onCredential: (idToken: string) => void,
): Promise<void> {
  await loadGoogleIdentity()

  const api = googleIdentityServices()
  if (api === undefined) {
    throw new Error('Google 登录脚本已加载但未就绪')
  }

  if (initializedClientId !== clientId) {
    api.accounts.id.initialize({
      client_id: clientId,
      callback: (response) => {
        currentHandler?.(response.credential)
      },
    })
    initializedClientId = clientId
  }
  currentHandler = onCredential

  api.accounts.id.renderButton(parent, {
    theme: 'outline',
    size: 'large',
    text: 'signin_with',
    width: 380,
  })
}
