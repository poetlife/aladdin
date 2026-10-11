import type { DirectUploadCredential } from '../gen/proto/aladdin/objectstore/v1/upload_pb.js'
import { cosAuthorization, cosSignTime } from './cos-signature.js'

/**
 * 用一份服务端签发的直传凭证，把一段字节写到凭证指定的那个键上。
 *
 * 这是**调用面这一侧"字节怎么进对象存储"的唯一实现**，与浏览器那一条同源：
 * 两者读的是同一份凭证定义（`DirectUploadCredential`），实现在各自的语言里，
 * 跨语言能共享的正是那份形状（见 docs/design/objectstore/README.md）。
 *
 * 凭证的性质决定了它的用法：
 *
 *   - **短时有效**：缓存下来的那一份之后只会换来一次失败，因此用完即弃；
 *   - **只对一个键有效**：不要拿它去写别的键，策略在存储侧就会拒绝；
 *   - **它的密钥与令牌绝不进错误信息**（见 cos-signature.ts）。
 *
 * 它是长程动作（一份素材可能几十上百 MB），因此超时**不跟着 RPC 的钟走**：
 * 用 RPC 那 30 秒的尺度会让稍大一点的素材必然超时。
 */

/** 一次直传的超时。与命令行那一侧同一个尺度与同一条理由（见 cmd/aladdin/direct-upload.go）。 */
const UPLOAD_TIMEOUT_MS = 30 * 60 * 1000

/** 签名有效期。它只需覆盖一次上传，取一小时。 */
const SIGNATURE_LIFETIME_SECONDS = 3600

export async function directUpload(
  credential: DirectUploadCredential,
  bytes: Uint8Array,
  contentType: string,
): Promise<void> {
  if (credential.key === '' || credential.secretId === '') {
    throw new Error('服务端没有下发可用的直传凭证')
  }
  const host = `${credential.bucket}.cos.${credential.region}.myqcloud.com`
  const url = `https://${host}/${credential.key.replace(/^\/+/, '')}`

  const signTime = cosSignTime(Math.floor(Date.now() / 1000), SIGNATURE_LIFETIME_SECONDS)
  const headers: Record<string, string> = {
    host,
    'x-cos-security-token': credential.sessionToken,
  }
  // **仅当不存在时写入**：内容对象按内容摘要寻址，覆盖一个已存在的键就是改写
  // 另一个版本正在用的字节——那是"版本不可变"的反面。凭证带这个条件时，客户端
  // 在请求上带禁止覆盖的头，由对象存储执行（策略条件表达不了它）。
  if (credential.forbidOverwrite) {
    headers['x-cos-forbid-overwrite'] = 'true'
  }
  const authorization = cosAuthorization({
    secretID: credential.secretId,
    secretKey: credential.secretKey,
    method: 'PUT',
    key: credential.key,
    headers,
    signTime,
  })

  const sent: Record<string, string> = {
    ...headers,
    authorization,
    'content-type': contentType,
  }
  delete sent['host']

  let response: Response
  try {
    response = await fetch(url, {
      method: 'PUT',
      headers: sent,
      // 一段字节要被当作请求体，而 DOM 的 BodyInit 那套类型不认 Uint8Array
      // 的一般形式（它只认 ArrayBuffer 支撑的那些）。运行时两者是同一件事，
      // 这里断言一次；**不复制成第二份**——一份素材可能几十上百 MB，
      // 为了类型而多拷一遍是不值得的。
      body: bytes as unknown as BodyInit,
      signal: AbortSignal.timeout(UPLOAD_TIMEOUT_MS),
    })
  } catch (err) {
    // 网络层的失败不携带凭证，可以照原样带出去。
    throw new Error(`直传失败，请重试：${err instanceof Error ? err.message : String(err)}`)
  }

  if (!response.ok) {
    throw new Error(`直传失败：${await describeFailure(response)}`)
  }
}

/**
 * 把一次失败的响应压成一句可展示的文案。
 *
 * **只取状态码与存储侧给出的错误码**：响应体里可能回显这次请求的细节，而请求头
 * 里带着一份还能用的写入凭证。把整段响应打出去，等于把一次写入的能力留在了
 * 终端与日志里。
 */
async function describeFailure(response: Response): Promise<string> {
  const body = await response.text().catch(() => '')
  const code = /<Code>([^<]*)<\/Code>/.exec(body)?.[1]
  const parts = [`HTTP ${response.status}`]
  if (code !== undefined && code !== '') {
    parts.push(code)
  }
  return parts.join(' / ')
}
