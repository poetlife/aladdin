/**
 * 把字节数说成人话，用于展示与提示文案。
 *
 * 它是**唯一实现**：资料页（头像上限）、资产库（各类上限、文件大小）与技能详情页
 * （文件清单）说的是同一句话。三处各写一份的表现是同一次上传在不同页面上给出几个
 * 数字，而用户无从判断哪个才是服务端真正的上限。
 *
 * 入参同时接受 `number` 与 `bigint`：proto 把**上限**生成为 number（uint32），把
 * **实际大小**生成为 bigint（uint64）。让这一个入口吃下两者，调用侧就不必各自决定
 * "怎么把 bigint 变成一个能除的数"——那件事各做一遍，迟早有人的舍入与别人不同。
 *
 * 换算全程在 bigint 里做，不经过 number：`Number(bigint)` 超过 2^53 会静默丢精度。
 * 今天的取值够不着那个量级，但那是"服务端已有的上限恰好都是 uint32"给的，不是这个
 * 函数能保证的——一个展示函数不该把正确性建立在别人还没改的前提上。
 */
export function describeBytes(bytes: number | bigint): string {
  const value = typeof bytes === 'bigint' ? bytes : BigInt(bytes)

  const KIB = 1024n
  const MIB = KIB * 1024n

  // **小于 1 KiB 直接说字节数。** 换成 KB 会把它们一律舍成「0 KB」，而这一档恰恰
  // 是文件清单里最常见的那一类（SKILL.md 之类的小文件）——"0 KB" 看着像读取失败，
  // 而它其实是一个准确的读数被人为压掉了。
  if (value < KIB) return `${value} B`

  // 以下两处都按"最接近的那个单位值"舍入。bigint 的 `/` 是**截断**，所以先加上
  // 半个单位再除，才是四舍五入。
  if (value < MIB) return `${(value + KIB / 2n) / KIB} KB`
  if (value % MIB === 0n) return `${value / MIB} MiB`

  // 非整数 MiB 保留一位小数：按十分之一 MiB 算，再拼出那一位。
  const tenths = (value * 10n + MIB / 2n) / MIB
  return `${tenths / 10n}.${tenths % 10n} MiB`
}
