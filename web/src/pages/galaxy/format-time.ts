/**
 * 把服务端下发的 RFC3339 时间串格式化成本地时间，供展示用。
 *
 * 时间字段一律是字符串（见 proto 生成类型）：**不假定它是 Date**，也不做
 * 任何业务判断，解析不出来就原样显示，免得把一个排障用的时间显示成
 * "Invalid Date"。
 */
export function formatTime(value: string): string {
  if (value === '') {
    return '—'
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return value
  }
  return date.toLocaleString(undefined, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  })
}
