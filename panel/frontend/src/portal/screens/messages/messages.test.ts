import { describe, expect, it } from 'vitest'
import { notificationSchema } from './api'
import { messageTab, notificationTarget } from './model'

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
    expect(notificationTarget('security.new_login')).toBeNull()
  })
})
