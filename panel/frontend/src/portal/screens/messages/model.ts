/**
 * [INPUT]: 无
 * [OUTPUT]: 对外提供 MessageTab / messageTab、notificationTarget
 * [POS]: portal/screens/messages 的纯映射（契约门户-08）：标签页取自 ?tab=announcements（概览公告卡的「全部」入口用它），通知 code 推导点击后去哪一页（order.paid → 订单、ticket.replied → 工单、subscription.expiring / quota.warning → 我的订阅，其余只标已读）；有单元测试
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
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
