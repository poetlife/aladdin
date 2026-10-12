import { readFileSync, writeFileSync } from 'node:fs'
import { contentDigest } from '../content-digest.js'

/**
 * 本地资产记录：**本地的一份字节对应平台上的哪一份资产**。
 *
 * 它在构建与调用之间那条缝上：调用面（要联网）把素材送进资产库时写下一条，
 * 构建期插件（**不联网**）读它，把源里的标准写法解析成文件组里的资产条目。
 * 构建因此不必去问服务端"这一份是哪个资产"——那条路要走网络，而构建一旦联网
 * 就绑定了目标环境（见 docs/design/galaxy/sdk.md 的三条红线）。
 *
 * **按内容摘要作键，不按本地路径。** 两个理由：
 *
 *   - 与平台的内容寻址同源：同一份字节在平台上是同一个资产，"这份字节是谁"
 *     因此是一个可判定的问题，而"这个文件叫什么"不是；
 *   - 移动或改名一份素材不会让记录失效，而按路径作键时它必然失效。
 *
 * 记录是**每个部署各一份**的：资产标识由某一个部署分配，换一个部署就换一批。
 * 把它提交进版本库因此要自己想清楚（见 README）。
 */
export const ASSET_LOCK_FILE = 'aladdin.assets.json'

/** 本地资产记录。 */
export interface AssetLock {
  /** 记录格式的版本。不认识的版本一律拒绝，而不是猜着读。 */
  version: 1
  /** 内容摘要（SHA-256 十六进制）→ 资产标识。 */
  assets: Record<string, string>
}

/** 一份空记录：还没有送过任何素材。 */
export function emptyAssetLock(): AssetLock {
  return { version: 1, assets: {} }
}

/**
 * 读一份记录文件。**文件不存在是合法的**（空记录），别的失败一律抛。
 *
 * "不存在"与"读不出来"必须分开：前者是"还没送过素材"这条常态，后者是记录被
 * 写坏了或权限不对。把后者也当成空记录，表现是构建期安静地把每一处素材都当成
 * 没送过——而用户手里那份记录看起来好好的。
 */
export function readAssetLock(file: string): AssetLock {
  let text: string
  try {
    text = readFileSync(file, 'utf8')
  } catch (err) {
    if (isNotFound(err)) {
      return emptyAssetLock()
    }
    throw err
  }
  return parseAssetLock(text, file)
}

/** 解析记录文件的内容。形状不对即抛，并指出是哪一份文件。 */
export function parseAssetLock(text: string, file: string): AssetLock {
  let raw: unknown
  try {
    raw = JSON.parse(text)
  } catch {
    throw new Error(`${file} 不是合法的 JSON，无法作为本地资产记录读出`)
  }
  if (typeof raw !== 'object' || raw === null) {
    throw new Error(`${file} 的形状不对：本地资产记录是一个对象`)
  }
  const record = raw as { version?: unknown; assets?: unknown }
  if (record.version !== 1) {
    throw new Error(`${file} 的记录版本不是 1（读到 ${String(record.version)}），请重新生成`)
  }
  if (typeof record.assets !== 'object' || record.assets === null) {
    throw new Error(`${file} 的形状不对：assets 是一个以内容摘要为键的对象`)
  }
  const assets: Record<string, string> = {}
  for (const [digest, assetID] of Object.entries(record.assets as Record<string, unknown>)) {
    if (typeof assetID !== 'string' || assetID === '') {
      throw new Error(`${file} 里 ${digest} 对应的资产标识不是一个非空字符串`)
    }
    assets[digest] = assetID
  }
  return { version: 1, assets }
}

/**
 * 写一份记录。**按摘要排序**，好让两次内容相同的记录逐字相同——否则每次上传
 * 都会产生一次只因键序不同而不同的改动，把版本库里真正的改动淹掉。
 */
export function writeAssetLock(file: string, lock: AssetLock): void {
  const assets: Record<string, string> = {}
  for (const digest of Object.keys(lock.assets).sort()) {
    const assetID = lock.assets[digest]
    if (assetID !== undefined) {
      assets[digest] = assetID
    }
  }
  writeFileSync(file, `${JSON.stringify({ version: 1, assets }, null, 2)}\n`, 'utf8')
}

/** 记下一份字节对应哪个资产，返回一份新的记录（不改原对象）。 */
export function withAsset(lock: AssetLock, digest: string, assetID: string): AssetLock {
  return { version: 1, assets: { ...lock.assets, [digest]: assetID } }
}

/** 读一份文件、算它的摘要。**构建期插件唯一的取摘要方式**（不联网）。 */
export function digestOfFile(file: string): string {
  return contentDigest(readFileSync(file))
}

/** 一份本地文件的摘要是不是已经在记录里；在的话给出资产标识。 */
export function assetIDFor(lock: AssetLock, digest: string): string | undefined {
  return lock.assets[digest]
}

function isNotFound(err: unknown): boolean {
  return typeof err === 'object' && err !== null && (err as { code?: unknown }).code === 'ENOENT'
}
