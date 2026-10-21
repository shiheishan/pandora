import type { IconName } from '../../kit'

export type MessageTab = 'notifications' | 'announcements'

export const messageTab = (query: URLSearchParams): MessageTab => (query.get('tab') === 'announcements' ? 'announcements' : 'notifications')

const TARGETS: Readonly<Record<string, string>> = {
  'order.paid': '/orders',
  'ticket.replied': '/tickets',
  'subscription.expiring': '/subs',
  'quota.warning': '/subs',
  'subscription.link_rotated': '/subs',
  'order.pending_created': '/orders',
}

/** 通知点开后跳到哪一页；不认识的 code 返回 null，只标已读不跳转 */
export const notificationTarget = (code: string): string | null => TARGETS[code] ?? null

// ---------------------------------------------------------------------------
// 样稿 .nt：每条左侧一个圆形图标，按 code 取；不认识的 code 用铃铛兜底
// ---------------------------------------------------------------------------
const ICONS: Readonly<Record<string, IconName>> = {
  'order.paid': 'orders',
  'ticket.replied': 'chat',
  'subscription.expiring': 'subs',
  'subscription.expired': 'subs',
  'subscription.recall': 'subs',
  'quota.warning': 'chart',
  'subscription.link_rotated': 'subs',
  'order.pending_created': 'orders',
}

export function notificationIcon(code: string): IconName {
  const known = ICONS[code]
  if (known) return known
  if (code.startsWith('security.')) return 'shield'
  return 'bell'
}

/** 样稿口径：一小时内「N 分钟前」、一天内「N 小时前」、一周内「N 天前」，再早写月-日（跨年带年份）；浏览器本地时区 */
export function noticeTime(at: string, now: Date = new Date()): string {
  const d = new Date(at)
  if (Number.isNaN(d.getTime())) return ''
  const seconds = Math.max(0, Math.floor((now.getTime() - d.getTime()) / 1000))
  if (seconds < 60) return '刚刚'
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes} 分钟前`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} 小时前`
  const days = Math.floor(hours / 24)
  if (days < 7) return `${days} 天前`
  const p = (n: number) => String(n).padStart(2, '0')
  const md = `${p(d.getMonth() + 1)}-${p(d.getDate())}`
  return d.getFullYear() === now.getFullYear() ? md : `${d.getFullYear()}-${md}`
}

/** 「今天 / 更早」分组（按浏览器本地日期）；sent_at 为 null 的放进「更早」；空组不出现，组内保持接口顺序 */
export function groupByDay<T extends { sent_at: string | null }>(list: readonly T[], now: Date = new Date()): { label: '今天' | '更早'; items: T[] }[] {
  const today: T[] = []
  const earlier: T[] = []
  for (const n of list) {
    const d = n.sent_at ? new Date(n.sent_at) : null
    if (d && !Number.isNaN(d.getTime()) && d.toDateString() === now.toDateString()) today.push(n)
    else earlier.push(n)
  }
  const groups: { label: '今天' | '更早'; items: T[] }[] = []
  if (today.length) groups.push({ label: '今天', items: today })
  if (earlier.length) groups.push({ label: '更早', items: earlier })
  return groups
}
