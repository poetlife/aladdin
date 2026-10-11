/**
 * 取一份内容。
 *
 * **请求只能发到发布域。** 发布物的内容安全策略把脚本能发往的地方限在发布域上
 * （`connect-src 'self'`），因此接收端只能落在发布域上——好处是不必再开第二份
 * 来源白名单。这一条不是本包加的约束，是发布物的既有边界（见
 * docs/design/galaxy/publication.md）；本包只是照它做事。
 *
 * **普通素材不需要凭证**：发布物里的素材走公开链，地址即凭据。凭证是给受保护
 * 的那一类用的，见 runtime/credentials.ts。
 */

export interface FetchContentOptions {
  /**
   * 取内容用的凭证。留空表示匿名取——普通素材本来就公开可取。
   *
   * 它的形状与签发属于内容保密那一条的服务端一半（#87），本包只负责带上它。
   */
  credential?: string
  signal?: AbortSignal
}

/** 取一份内容。失败以可重试的错误抛出，不静默降级成一份空内容。 */
export async function fetchContent(url: string, options: FetchContentOptions = {}): Promise<Response> {
  if (!isSameOriginAsSelf(url)) {
    // 发布域之外取不到东西（内容安全策略会拦），因此这里如实早退，而不是
    // 让浏览器在控制台里报一条与调用点无关的 CSP 违规。
    throw new Error(`只能从发布域取内容，得到 ${url}`)
  }
  const headers: Record<string, string> = {}
  if (options.credential !== undefined && options.credential !== '') {
    headers.Authorization = `Bearer ${options.credential}`
  }
  const response = await fetch(url, {
    headers,
    ...(options.signal === undefined ? {} : { signal: options.signal }),
  })
  if (!response.ok) {
    throw new Error(`取内容失败：HTTP ${response.status} ${url}`)
  }
  return response
}

/**
 * 一条地址是不是落在这一页自己的源上（也就是发布域上）。
 *
 * 判据是**这一页自己的源**，不是某个配置值：页面本来就跑在发布域上，而
 * "发布域是哪个"这件事在页面里根本不必知道——浏览器已经把它告诉我了。
 */
export function isSameOriginAsSelf(url: string): boolean {
  if (typeof location === 'undefined') {
    // 不在浏览器里（测试或本地脚本）：这一条判断没有依据，不做假判断。
    return true
  }
  try {
    return new URL(url, location.href).origin === location.origin
  } catch {
    return false
  }
}
