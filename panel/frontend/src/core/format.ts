/**
 * [INPUT]: 无外部依赖
 * [OUTPUT]: 对外提供 formatMoney、relativeTime
 * [POS]: core 的展示格式化，外框与第 3 阶段页面共用：金额是币种最小单位整数（契约 1.8），时间是 RFC3339 或 Date
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// ---------------------------------------------------------------------------
// 金额：后端一律给最小单位 int64（分 / 美分），展示 ÷100 保留两位；
// 币种只有 CNY / USD，其它按代码前缀显示，不猜符号。
// ---------------------------------------------------------------------------
const SYMBOL: Record<string, string> = { CNY: '¥', USD: '$' }

export function formatMoney(minor: number, currency = 'CNY'): string {
  const sign = minor < 0 ? '-' : ''
  const abs = Math.abs(minor)
  const units = Math.floor(abs / 100)
  const cents = String(abs % 100).padStart(2, '0')
  const grouped = String(units).replace(/\B(?=(\d{3})+(?!\d))/g, ',')
  const symbol = SYMBOL[currency] ?? `${currency} `
  return `${sign}${symbol}${grouped}.${cents}`
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
