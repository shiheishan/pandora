import { randomUUID } from 'node:crypto'
import type { MockModule } from '../types.ts'
import { isUuid } from './billing.ts'
import { gate, portalState, scenario, type PortalState } from './fixtures.ts'

interface NotificationFixture {
  id: string
  code: string
  subject: string
  body: string
  sent_at: string
  read_at: string | null
}

// 挂在 PortalState 上（WeakMap），切场景重建时跟着重建
const states = new WeakMap<PortalState, NotificationFixture[]>()

function inbox(userId: string): NotificationFixture[] {
  const portal = portalState(userId)
  let list = states.get(portal)
  if (!list) {
    list = build()
    states.set(portal, list)
  }
  return list
}

function build(): NotificationFixture[] {
  if (scenario() === 'empty') return []
  const ago = (min: number) => new Date(Date.now() - min * 60_000).toISOString()
  const n = (code: string, subject: string, body: string, minutes: number, read: boolean): NotificationFixture => ({ id: randomUUID(), code, subject, body, sent_at: ago(minutes), read_at: read ? ago(minutes - 1) : null })
  return [
    n('ticket.replied', '工单有新回复', '你的工单「订阅链接导入 Clash 失败」客服已回复，点击查看。', 6, false),
    n('order.paid', '订单支付成功', '订单 PD-23392 已支付 ¥25.00，流量包 50 GB 已到账。', 42, false),
    n('quota.warning', '本期流量已用 80%', '专业版本期已用 320 GB / 400 GB，距离重置还有 3 天。', 60 * 26, true),
    n('security.new_login', '新设备登录提醒', '你的账号于昨天 22:14 在一台新的 macOS 设备上登录。如果不是你本人，请立即修改密码。', 60 * 30, true),
    n('subscription.expiring', '订阅将在 7 天后到期', '专业版将于 7 天后到期，续费后订阅地址不变。', 60 * 24 * 5, true),
  ]
}

export const messages: MockModule = {
  routes: {
    'GET /v1/me/notifications': async (ctx) => {
      if (!(await gate(ctx))) return
      const list = inbox(ctx.user.userId)
      const raw = Number.parseInt(ctx.query.get('limit') ?? '', 10)
      const limit = raw > 0 && raw <= 100 ? raw : 30
      const pool = ctx.query.get('unread') === '1' ? list.filter((x) => !x.read_at) : list
      ctx.send(200, { notifications: pool.slice(0, limit), unread: list.filter((x) => !x.read_at).length })
    },
    'POST /v1/me/notifications/read-all': (ctx) => {
      const now = new Date().toISOString()
      for (const x of inbox(ctx.user.userId)) x.read_at ??= now
      ctx.send(200, { ok: true })
    },
    'POST /v1/me/notifications/:id/read': (ctx) => {
      if (!isUuid(ctx.params.id)) return ctx.fail(500, 'internal_error', '服务暂时不可用')
      const hit = inbox(ctx.user.userId).find((x) => x.id === ctx.params.id)
      if (hit) hit.read_at ??= new Date().toISOString()
      ctx.send(200, { ok: true })
    },
    // 最多 20 条，置顶优先、再按发布时间倒序
    'GET /v1/me/announcements': async (ctx) => {
      if (!(await gate(ctx))) return
      const list = [...portalState(ctx.user.userId).announcements].sort((a, b) => Number(b.pinned) - Number(a.pinned) || (b.published_at ?? '').localeCompare(a.published_at ?? ''))
      ctx.send(200, { announcements: list.slice(0, 20) })
    },
  },
}
