/**
 * 没有头像时，用展示名的首字符做占位。
 *
 * **唯一入口**：页头与档案页都用它。两处各写一份的下场是"同一个名字在页头
 * 是一个字、在档案页是另一个字"——那是纯粹的噪声，而且没人会想到去比对。
 *
 * 按**码点**取而不是按 UTF-16 码元：中文之外还有 emoji 这类由代理对构成的
 * 字符，用 `name[0]` 会切出半个字符，界面上是一个乱码方块。
 */
export function avatarFallbackInitial(displayName: string): string {
  return Array.from(displayName)[0] ?? '?'
}
