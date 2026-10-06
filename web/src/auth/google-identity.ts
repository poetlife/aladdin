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
 * 挂载 Google 登录按钮所需的参数。
 *
 * `scheme`：GIS 的按钮配色是渲染时定死的，外部的明暗变量进不去，只能在渲染时
 * 选一个内置主题。暗色用 `outline_dark`（深底浅边），与 antd 在暗色下的默认按钮
 * 最接近；亮色用 `outline`。**没有"跟随 CSS"这一档**，因此主题变化必须重新挂载
 * 按钮，而不是改个类名（见 google-sign-in-button.tsx）。
 *
 * `size`：GIS 的档位名与 antd 不同（large/medium/small，没有 middle），但尺寸对得上
 * （40/32/24）。调用方按 antd 的档位决定，由组件折算成这里的名字。
 *
 * `borderRadius` / `fontSize`：GIS 的按钮带着自己的一套观感（4px 圆角、14px 字号），
 * 站在周围全是站点尺寸的界面上会显得小一号。它**只提供 `rectangular`（4px）与
 * `pill` 两种形状**，也没有字号这一档，所以这两项只能由调用方把站点的值传进来，
 * 由这里盖上去。
 *
 * `signal`：让调用方撤销一次尚未完成的挂载。挂载要等脚本加载，是异步的；期间
 * 组件可能已经卸载或重挂（开发期 StrictMode 每次都这么走），迟到的这一次若不
 * 作废，就会和后一次各画一个按钮。
 */
export interface GoogleButtonOptions {
  clientId: string
  onCredential: (idToken: string) => void
  scheme: 'light' | 'dark'
  size: 'large' | 'medium'
  borderRadius: number
  fontSize: number
  signal?: AbortSignal
}

/**
 * 在给定容器里挂载 Google 登录按钮。
 *
 * 拿到身份令牌后回调 `onCredential`——由调用方交给服务端，这里不碰它。
 */
export async function mountGoogleButton(parent: HTMLElement, options: GoogleButtonOptions): Promise<void> {
  await loadGoogleIdentity()

  if (options.signal?.aborted === true) {
    return
  }

  const api = googleIdentityServices()
  if (api === undefined) {
    throw new Error('Google 登录脚本已加载但未就绪')
  }

  if (initializedClientId !== options.clientId) {
    api.accounts.id.initialize({
      client_id: options.clientId,
      callback: (response) => {
        currentHandler?.(response.credential)
      },
    })
    initializedClientId = options.clientId
  }
  currentHandler = options.onCredential

  // GIS 只接受 200–400 的像素宽度，且不接受百分比。桌面端维持原来的 380；
  // 量不到容器宽度时（clientWidth 为 0，例如尚未布局的环境）同样按 380 处理，
  // 免得把"没量出来"当成"容器很窄"。
  const available = parent.clientWidth
  const width = available > 0 ? Math.min(380, Math.max(200, available)) : 380

  api.accounts.id.renderButton(parent, {
    theme: options.scheme === 'dark' ? 'outline_dark' : 'outline',
    size: options.size,
    text: 'signin_with',
    width,
  })

  // 把站点的圆角与字号盖到 GIS 画的按钮上，并裁掉它内部的高亮层，否则会出现
  // 内外两套圆角。按钮是 GIS 渲染出来的普通 DOM（不是 iframe）才做得到这件事。
  // 找不到按钮就不动它：一次第三方结构调整最多让圆角与字号退回 GIS 默认，
  // 不该让按钮消失。
  const button = parent.querySelector<HTMLElement>('[role="button"]')
  if (button !== null) {
    button.style.borderRadius = `${String(options.borderRadius)}px`
    button.style.overflow = 'hidden'
    // 字号设在按钮上即可：GIS 的文字节点不自己声明字号，会继承下去。
    button.style.fontSize = `${String(options.fontSize)}px`
  }
}
