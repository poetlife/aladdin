import { createHash, createHmac } from 'node:crypto'

/**
 * 腾讯云对象存储的请求签名（q-sign-algorithm=sha1）。
 *
 * **为什么自己算而不用 SDK：** 浏览器那一侧用的是官方的 `cos-js-sdk-v5`，而它
 * 明确不支持在 node 里跑；把它连同 shim 一起塞进调用面，换来的是一个只在
 * 一半环境下成立的依赖。这里要的只是"按已知规则算一段签名"，因此按官方算法
 * 实现，并由测试与官方 SDK 逐字对齐（见 cos-signature.test.ts）——**两侧算出来
 * 的 Authorization 必须完全相同**，否则表现是"上传被存储侧拒绝"，而那句话里
 * 看不出是签名错了。
 *
 * 三处容易写错、且错了都只表现为一句"拒绝"的地方，取值一律照官方 SDK：
 *
 *   - 参与计算的路径名是**原样的对象键**，不百分号编码（对象的键由服务端分配，
 *     本来就是 URL 安全的；编码与否在这里不是风格问题——编码了签名就对不上
 *     实际发出的请求行）；
 *   - 请求头那一段是 `键=值` 的**连接**（不是只有键名），按 camSafe 规则编码；
 *   - `q-header-list` 那一段才是只有键名的那一份。
 *
 * 签名里出现的密钥与令牌**绝不进错误信息与日志**：写进日志等于把一次写入的
 * 能力留在了日志文件里（与 cmd/aladdin/direct-upload.go 同一条）。
 */

/** 一次直传请求的签名输入。 */
export interface CosSignatureInput {
  secretID: string
  secretKey: string
  /** HTTP 方法，大小写不敏感。 */
  method: string
  /** 对象键，形如 `galaxy/<工程标识>/assets/<类别>/<资产标识>`（不带桶与地域）。 */
  key: string
  /**
   * 参与签名的请求头，**键必须已经是小写**。
   *
   * 只有列在这里的头会被校验，因此列进来的那些必须与真正发出去的值逐字相同。
   * `host` 必须列进来。
   */
  headers: Record<string, string>
  /** 签名时间，形如 `1700000000;1700003600`（起始;结束）。 */
  signTime: string
}

/** 由起始时刻与有效期算出一个签名时间。 */
export function cosSignTime(nowSeconds: number, lifetimeSeconds: number): string {
  return `${nowSeconds};${nowSeconds + lifetimeSeconds}`
}

/** 算出一个请求的 `Authorization` 头。 */
export function cosAuthorization(input: CosSignatureInput): string {
  const headerNames = Object.keys(input.headers)
    .map((key) => camSafeEncode(key).toLowerCase())
    .sort(compareLowercase)

  const signKey = hmacSha1Hex(input.secretKey, input.signTime)
  const httpString = [
    input.method.toLowerCase(),
    // 原样的键，只补开头那一个斜杠。
    input.key.startsWith('/') ? input.key : `/${input.key}`,
    // 直传的请求上没有查询串：桶、地域与键在地址与签名里各有一处，
    // 再挂一串查询参数就是第二处会漂的来源。
    '',
    headerPairs(input.headers),
    '',
  ].join('\n')
  const stringToSign = ['sha1', input.signTime, sha1Hex(httpString), ''].join('\n')
  const signature = hmacSha1Hex(signKey, stringToSign)

  return [
    'q-sign-algorithm=sha1',
    `q-ak=${input.secretID}`,
    `q-sign-time=${input.signTime}`,
    `q-key-time=${input.signTime}`,
    `q-header-list=${headerNames.join(';')}`,
    'q-url-param-list=',
    `q-signature=${signature}`,
  ].join('&')
}

/** 请求头那一段：`键=值` 按编码后的键排序连接。 */
function headerPairs(headers: Record<string, string>): string {
  return Object.keys(headers)
    .map((key) => ({ key: camSafeEncode(key).toLowerCase(), value: headers[key] ?? '' }))
    .sort((left, right) => compareLowercase(left.key, right.key))
    .map((pair) => `${pair.key}=${camSafeEncode(pair.value)}`)
    .join('&')
}

/**
 * 官方 SDK 用的那一种百分号编码：在 `encodeURIComponent` 之上把 `!'()*` 也编掉。
 *
 * 逐字照抄它的取值不是风格问题：签名是逐字的，差一个字符就是一次拒绝。
 */
function camSafeEncode(value: string): string {
  return encodeURIComponent(value)
    .replace(/!/g, '%21')
    .replace(/'/g, '%27')
    .replace(/\(/g, '%28')
    .replace(/\)/g, '%29')
    .replace(/\*/g, '%2A')
}

function compareLowercase(left: string, right: string): number {
  const a = left.toLowerCase()
  const b = right.toLowerCase()
  if (a === b) {
    return 0
  }
  return a > b ? 1 : -1
}

function sha1Hex(value: string): string {
  return createHash('sha1').update(value, 'utf8').digest('hex')
}

function hmacSha1Hex(key: string, value: string): string {
  return createHmac('sha1', key).update(value, 'utf8').digest('hex')
}
