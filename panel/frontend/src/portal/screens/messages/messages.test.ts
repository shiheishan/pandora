import { describe, expect, it } from 'vitest'
import { notificationSchema } from './api'
import { groupByDay, messageTab, notificationIcon, noticeTime, notificationTarget } from './model'

describe('消息', () => {
  it('通知 schema：sent_at 与 read_at 可为 null', () => {
    const n = notificationSchema.parse({ id: 'n', code: 'order.paid', subject: 's', body: 'b', sent_at: null, read_at: null })
    expect(n.read_at).toBeNull()
  })

  it('?tab=announcements 直达公告，其余都是通知', () => {
    expect(messageTab(new URLSearchParams('tab=announcements'))).toBe('announcements')
    expect(messageTab(new URLSearchParams('tab=x'))).toBe('notifications')
    expect(messageTab(new URLSearchParams())).toBe('notifications')
  })

  it('按 code 推导去处，不认识的不跳', () => {
    expect(notificationTarget('order.paid')).toBe('/orders')
    expect(notificationTarget('ticket.replied')).toBe('/tickets')
    expect(notificationTarget('subscription.expiring')).toBe('/subs')
    expect(notificationTarget('quota.warning')).toBe('/subs')
    expect(notificationTarget('subscription.link_rotated')).toBe('/subs')
    expect(notificationTarget('order.pending_created')).toBe('/orders')
    expect(notificationTarget('security.new_login')).toBeNull()
  })
})

describe('通知列表的分组、时间与图标', () => {
  const now = new Date(2026, 9, 16, 15, 0)
  const at = (d: Date) => d.toISOString()

  it('今天 / 更早：sent_at 为 null 归更早，空组不出现', () => {
    const list = [
      { id: 'a', sent_at: at(new Date(2026, 9, 16, 14, 54)) },
      { id: 'b', sent_at: null },
      { id: 'c', sent_at: at(new Date(2026, 9, 15, 23, 0)) },
    ]
    expect(groupByDay(list, now).map((g) => [g.label, g.items.map((x) => x.id)])).toEqual([
      ['今天', ['a']],
      ['更早', ['b', 'c']],
    ])
    expect(groupByDay(list.slice(0, 1), now).map((g) => g.label)).toEqual(['今天'])
    expect(groupByDay([], now)).toEqual([])
  })

  it('相对时间：分钟、小时、天前，一周以外写月-日，跨年带年', () => {
    expect(noticeTime(at(new Date(2026, 9, 16, 14, 59, 30)), now)).toBe('刚刚')
    expect(noticeTime(at(new Date(2026, 9, 16, 14, 54)), now)).toBe('6 分钟前')
    expect(noticeTime(at(new Date(2026, 9, 16, 12, 0)), now)).toBe('3 小时前')
    expect(noticeTime(at(new Date(2026, 9, 11, 15, 0)), now)).toBe('5 天前')
    expect(noticeTime(at(new Date(2026, 9, 9, 8, 0)), now)).toBe('10-09')
    expect(noticeTime(at(new Date(2025, 11, 30, 8, 0)), now)).toBe('2025-12-30')
  })

  it('图标按 code 取，安全类用盾牌，不认识的用铃铛', () => {
    expect(notificationIcon('ticket.replied')).toBe('chat')
    expect(notificationIcon('order.paid')).toBe('orders')
    expect(notificationIcon('quota.warning')).toBe('chart')
    expect(notificationIcon('subscription.expired')).toBe('subs')
    expect(notificationIcon('subscription.link_rotated')).toBe('subs')
    expect(notificationIcon('order.pending_created')).toBe('orders')
    expect(notificationIcon('security.new_login')).toBe('shield')
    expect(notificationIcon('admin.broadcast')).toBe('bell')
  })
})
