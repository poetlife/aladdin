/**
 * 计算一段字节的 SHA-256，返回**小写十六进制**（64 位）。
 *
 * 它是资产在公开区的地址键：同一份字节在任何工程、任何版本里都得到同一个摘要
 * （见 docs/design/galaxy/asset-library.md）。服务端在直传路径上没有字节可以算它，
 * 因此由上传方在这里算好、随提交声明；服务端会在上架到公开区时读回对象重算一遍
 * 核对，不一致即拒绝发布。所以这个值必须算对——算错的表现是"这个版本发布不出来"。
 */
export async function sha256Hex(bytes: ArrayBuffer): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', bytes)
  return Array.from(new Uint8Array(digest))
    .map((byte) => byte.toString(16).padStart(2, '0'))
    .join('')
}
