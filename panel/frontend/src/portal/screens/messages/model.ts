export type MessageTab = 'notifications' | 'announcements'

export const messageTab = (query: URLSearchParams): MessageTab => (query.get('tab') === 'announcements' ? 'announcements' : 'notifications')

const TARGETS: Readonly<Record<string, string>> = {
  'order.paid': '/orders',
  'ticket.replied': '/tickets',
  'subscription.expiring': '/subs',
  'quota.warning': '/subs',
}

/** 通知点开后跳到哪一页；不认识的 code 返回 null，只标已读不跳转 */
export const notificationTarget = (code: string): string | null => TARGETS[code] ?? null
