/**
 * 发布根：内容与构建产物在发布域上的地址空间（形如
 * `https://<发布域>/g/<工程标识>/`，`docs` 槽是它下面的 `docs/`）。
 *
 * **它由调用方给出，SDK 不自己拼。** 发布根在服务端只有一处派生入口，命令行
 * 也是从那里取的（`galaxy project base`）；SDK 这一侧同样只做规范化与校验，
 * 不参与拼接——多一处拼接就是多一处会漂的来源（见 docs/design/galaxy/cli.md）。
 */

/** 把一个发布根规范化：必须是绝对地址，且以 `/` 结尾。 */
export function normalizePublishRoot(value: string): string {
  let url: URL
  try {
    url = new URL(value)
  } catch {
    throw new Error(`发布根必须是一个绝对地址（形如 https://发布域/g/<工程标识>/），得到 ${value}`)
  }
  if (url.protocol !== 'https:' && url.protocol !== 'http:') {
    throw new Error(`发布根必须是 http 或 https 的地址，得到 ${value}`)
  }
  if (url.search !== '' || url.hash !== '') {
    throw new Error(`发布根不带查询串与 fragment，得到 ${value}`)
  }
  // 结尾斜杠不能靠"用的时候再补"：拼接一处漏一处，而漏掉的表现是产物里出现
  // 一个少一段的地址（图裂开、脚本 404），且只在那一处才看得出来。
  if (!url.pathname.endsWith('/')) {
    url.pathname = `${url.pathname}/`
  }
  return url.href
}

/** 把一条站点内的路径拼成它对外的那条地址。**发布根之外的拼接只有这一处。** */
export function siteURL(publishRoot: string, path: string): string {
  return new URL(path.replace(/^\/+/, ''), normalizePublishRoot(publishRoot)).href
}
