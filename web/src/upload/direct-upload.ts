import COS from 'cos-js-sdk-v5'

import type { DirectUploadCredential } from '../gen/proto/aladdin/objectstore/v1/upload_pb'

/**
 * 用一份服务端签发的直传凭证，把一段字节写到凭证指定的那个键上。
 *
 * 这是**"上传的字节怎么进对象存储"的唯一实现**：头像与 galaxy 资产都调它，
 * 不再各写一份（见 docs/design/objectstore/README.md）。
 *
 * 凭证的性质决定了它的用法：
 * - **短时有效**：它随时可能过期，缓存下来的那一份之后只会换来一次失败，所以用完即弃；
 * - **只对一个键有效**：它被钉死在一个对象键上，另一次上传需要另一份凭证，
 *   所以**不要把它复用到别的上传**，也不要拿它去写别的键——策略在存储侧就会拒绝。
 *
 * 这里刻意不记录、不对外暴露凭证里的任何密钥字段。把 secretKey / sessionToken
 * 写进日志或错误信息，等于把一次写入的能力留在了日志文件里。
 *
 * 字节的传输失败（网络中断、存储侧拒绝）会以**可重试**的错误抛出：调用方
 * 应当把它呈现给用户并允许重来一次，而不是静默吞掉。
 */
export async function directUpload(
  credential: DirectUploadCredential,
  body: Blob,
  contentType: string,
): Promise<void> {
  const cos = new COS({
    SecretId: credential.secretId,
    SecretKey: credential.secretKey,
    SecurityToken: credential.sessionToken,
  })

  // SDK 的 putObject 也提供 Promise 形式，但这里显式包一层回调：错误信息由
  // 我们自己组织，且在错误里只取错误码/状态码这类**不含凭证**的字段。
  await new Promise<void>((resolve, reject) => {
    cos.putObject(
      {
        Bucket: credential.bucket,
        Region: credential.region,
        Key: credential.key,
        Body: body,
        ContentType: contentType,
      },
      (err) => {
        if (err !== null) {
          reject(new Error(failureMessage(err)))
          return
        }
        resolve()
      },
    )
  })
}

/** 把 SDK 的错误压成一句可展示的文案，只取不会泄露凭证的字段。 */
function failureMessage(err: { code?: string; message?: string; statusCode?: number }): string {
  const parts = [err.message, err.code, err.statusCode === undefined ? '' : `HTTP ${err.statusCode}`]
  const detail = parts.filter((part) => part !== undefined && part !== '').join(' / ')
  return detail === '' ? '直传失败，请重试' : `直传失败：${detail}`
}
