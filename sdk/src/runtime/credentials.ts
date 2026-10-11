import {
  FRAME_CONTENT_CREDENTIAL_REQUEST_TYPE,
  frameMessage,
  parseContentCredential,
} from './frame-channel.js'
import type { FrameContentCredential } from './frame-channel.js'

/**
 * 取一份取内容用的凭证。
 *
 * **两条来源，同一条链**（见 docs/design/galaxy/sdk.md）：
 *
 *   - 页面在访客浏览器里 ⇒ 向**宿主**（主站壳、工作台）索取，走接入桥的
 *     宿主 → 页面方向；
 *   - 本地调试 ⇒ 由调用方**直接给一份**（例如从命令行取来的那一份）。
 *
 * **凭证不是主站的会话凭证。** 这一页跑在不透明源的沙箱里，里面是用户自己的
 * 脚本；把主站会话交给它，等于让用户的内容可以替这位访客调用主站的接口。宿主
 * 因此只能下发一份**只够取这一份内容**的凭证，它的签发属于内容保密那一条
 * （#87）的服务端一半。
 *
 * 取不到凭证**不是致命错误**：发布物里的普通素材本来就公开可取，凭证是给受保护
 * 那一类用的。因此这里把"没拿到"如实返回，由调用方决定要不要继续。
 */

export interface ContentCredentialOptions {
  /** 已经到手的一份凭证（本地调试用）。给了它就不再问宿主。 */
  credential?: string
  /** 等宿主回答的超时。超过它就当这一次没拿到。 */
  timeoutMs?: number
  /** 发消息的目标窗口。默认页面自己所在的那个窗口的 parent。 */
  host?: WindowProxy
  /** 收消息的窗口。默认页面自己所在的窗口。 */
  self?: Window
}

/** 等宿主回答的默认超时。 */
const DEFAULT_TIMEOUT_MS = 2000

/** 一个已到手的凭证，或一次明确的"没拿到"。 */
export type ContentCredential = FrameContentCredential

export async function contentCredential(
  options: ContentCredentialOptions = {},
): Promise<ContentCredential> {
  if (options.credential !== undefined && options.credential !== '') {
    return { credential: options.credential, expiresAt: '', reason: '' }
  }

  const host = options.host ?? (typeof window === 'undefined' ? undefined : window.parent)
  const self = options.self ?? (typeof window === 'undefined' ? undefined : window)
  if (host === undefined || self === undefined || host === self) {
    // 页面没有被嵌在宿主里（直接打开发布域，或本地跑在一个普通标签页里）。
    // 这不是故障：**高级用户照旧可以直连发布域**（见 publication.md），
    // 只是那一条路上没有人能给它凭证。如实说清楚，调用方据此降级。
    return { credential: '', expiresAt: '', reason: '这一页没有被宿主嵌进来，取不到凭证' }
  }

  const timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS
  return await new Promise<ContentCredential>((resolve) => {
    let settled = false
    const finish = (result: ContentCredential): void => {
      if (settled) {
        return
      }
      settled = true
      clearTimeout(timer)
      self.removeEventListener('message', onMessage)
      resolve(result)
    }

    const onMessage = (event: MessageEvent): void => {
      // **只认来自我们自己那个宿主窗口的消息。** 不透明源的 event.origin 是
      // 字符串 `null`，任何沙箱帧都是它，那个值在这条通道上认不出是谁发的。
      if (event.source !== host) {
        return
      }
      const answer = parseContentCredential(event.data)
      if (answer !== null) {
        finish(answer)
      }
    }
    self.addEventListener('message', onMessage)

    const timer = setTimeout(() => {
      finish({ credential: '', expiresAt: '', reason: '宿主没有在时限内回答凭证请求' })
    }, timeoutMs)

    host.postMessage(frameMessage(FRAME_CONTENT_CREDENTIAL_REQUEST_TYPE, {}), '*')
  })
}
