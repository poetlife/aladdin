/**
 * 把字节数说成人话，用于展示与提示文案。
 *
 * 它是**唯一实现**：资料页（头像上限）与资产库（各类上限、文件大小）说的是同一
 * 句话。两处各写一份的表现是同一次上传在两个页面上给出两个数字，而用户无从判断
 * 哪个才是服务端真正的上限。
 */
export function describeBytes(bytes: number): string {
  const mib = bytes / (1024 * 1024)
  if (mib >= 1) {
    return `${Number.isInteger(mib) ? mib : mib.toFixed(1)} MiB`
  }
  return `${Math.round(bytes / 1024)} KB`
}
