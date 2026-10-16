// ---------------------------------------------------------------------------
// 金额：后端一律给分（int64），展示 ÷100 保留两位。整个系统只有人民币（10-16），没有币种参数。
// ---------------------------------------------------------------------------
export function formatMoney(minor: number): string
/**
 * @deprecated 10-16 起只有人民币，第二个参数被忽略，一律显示 ¥。
 * 只为各模块块改完调用方之前保持能编译；全部改完后删掉这个重载。
 */
export function formatMoney(minor: number, currency: string | undefined): string
export function formatMoney(minor: number): string {
  const sign = minor < 0 ? '-' : ''
  const abs = Math.abs(minor)
  const units = Math.floor(abs / 100)
  const cents = String(abs % 100).padStart(2, '0')
  const grouped = String(units).replace(/\B(?=(\d{3})+(?!\d))/g, ',')
  return `${sign}¥${grouped}.${cents}`
}

// ---------------------------------------------------------------------------
// 计数：整数千分位，12408 → 12,408（小数截掉，计数不该有小数）。
// ---------------------------------------------------------------------------
export function formatCount(n: number): string {
  const sign = n < 0 ? '-' : ''
  return sign + String(Math.abs(Math.trunc(n))).replace(/\B(?=(\d{3})+(?!\d))/g, ',')
}

// ---------------------------------------------------------------------------
// 流量：字节 → 「18.6 TB / 1.80 TB / 540 GB」，三位有效数字、1024 进制。
// DASH-01 冻结契约的字节是 numeric 聚合出的十进制字符串，可能超过 2^53，
// 要求 BigInt 安全：不先转 number，全程整数运算再拼小数，四舍五入。
// ---------------------------------------------------------------------------
const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB', 'EB'] as const

export function formatBytes(value: string | number | bigint): string {
  let n = BigInt(value)
  const sign = n < 0n ? '-' : ''
  if (n < 0n) n = -n
  let unit = 1n
  let i = 0
  while (i < BYTE_UNITS.length - 1 && n >= unit * 1024n) {
    unit *= 1024n
    i++
  }
  if (i === 0) return `${sign}${n} B`
  const whole = n / unit
  const decimals = whole < 10n ? 2 : whole < 100n ? 1 : 0
  const scale = 10n ** BigInt(decimals)
  const scaled = (n * scale + unit / 2n) / unit
  const frac = decimals ? `.${String(scaled % scale).padStart(decimals, '0')}` : ''
  return `${sign}${scaled / scale}${frac} ${BYTE_UNITS[i]}`
}

// ---------------------------------------------------------------------------
// 相对时间：设计稿的「刚刚 / 2 分钟 / 6 分钟」口径，超过一天给 MM-DD。
// ---------------------------------------------------------------------------
export function relativeTime(at: Date | string, now: Date = new Date()): string {
  const then = typeof at === 'string' ? new Date(at) : at
  const seconds = Math.max(0, Math.floor((now.getTime() - then.getTime()) / 1000))
  if (seconds < 60) return '刚刚'
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes} 分钟`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} 小时`
  const mm = String(then.getMonth() + 1).padStart(2, '0')
  const dd = String(then.getDate()).padStart(2, '0')
  return `${mm}-${dd}`
}

// ---------------------------------------------------------------------------
// 绝对时间（浏览器本地时区）：2026-09-24 08:30；解析不了原样返回，不抛错。
// ---------------------------------------------------------------------------
export function formatDateTime(at: Date | string): string {
  const d = typeof at === 'string' ? new Date(at) : at
  if (Number.isNaN(d.getTime())) return String(at)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

/** 后端两位小数的百分比串去掉多余的 0："2.50" → "2.5"，"0.00" → "0"，"10.00" → "10"；不是十进制数时回 "0"。按字符串处理，不经过浮点 */
export function trimPercent(s: string): string {
  const t = s.trim()
  if (!/^\d+(\.\d+)?$/.test(t)) return '0'
  const [w = '0', f = ''] = t.split('.')
  const whole = w.replace(/^0+(?=\d)/, '') || '0'
  const frac = f.replace(/0+$/, '')
  return frac ? `${whole}.${frac}` : whole
}
