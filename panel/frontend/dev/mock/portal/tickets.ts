/**
 * [INPUT]: 依赖 node:crypto 的 randomBytes / randomUUID，依赖 ../types 的 MockModule / MockContext，依赖 ./fixtures 的 gate / portalState / scenario / PortalState，依赖 ./billing 的 BillingError / isUuid / readStrict
 * [OUTPUT]: 对外提供 tickets 模块的假接口 MockModule
 * [POS]: dev/mock/portal 的「工单支持（门户-07）」假接口，归门户前端；形状、错误文案与幂等照 api-contract.md（修订 R25、R60）与 domain/support：分类、列表（不带 messages，related_order 恒为 null）、详情（message_count 0、last_reply_at 零值、related_order）、新建（support_ticket_create，未结满 5 个 409，订单须是本人的）、回复（support_ticket_user_reply，resolved 重开、closed 409）、关闭（support_ticket_user_close，已关闭 404）、撤回（必须带 JSON 体、客服回复过 409）；legacy 去掉 R60 字段
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomBytes, randomUUID } from 'node:crypto'
import type { MockContext, MockModule } from '../types.ts'
import { BillingError, isUuid, readStrict } from './billing.ts'
import { gate, portalState, scenario, type PortalState } from './fixtures.ts'

const DAY_MS = 86_400_000
const ZERO_TIME = '0001-01-01T00:00:00Z'

// domain/support.Categories 的固定顺序
const CATEGORIES: ReadonlyArray<[string, string]> = [
  ['general', '一般咨询'],
  ['technical', '连接与技术'],
  ['subscription', '订阅与套餐'],
  ['billing', '账单与支付'],
  ['account', '账号与安全'],
  ['abuse', '举报与投诉'],
]
const CATEGORY_CODES = new Set(CATEGORIES.map(([c]) => c))

type Status = 'open' | 'pending_user' | 'pending_agent' | 'escalated' | 'resolved' | 'closed'
type ClosedReason = 'user_closed' | 'withdrawn' | 'agent_closed'

interface MessageFixture {
  id: string
  author_kind: 'user' | 'agent' | 'system'
  body: string
  created_at: string
}

interface TicketFixture {
  id: string
  ticket_no: string
  subject: string
  category: string
  priority: string
  status: Status
  created_at: string
  updated_at: string
  resolved_at: string | null
  closed_reason: ClosedReason | null
  order: { id: string; order_no: string } | null
  messages: MessageFixture[]
}

// ---------------------------------------------------------------------------
// 状态：挂在 PortalState 上（WeakMap），切场景重建 PortalState 时跟着重建
// ---------------------------------------------------------------------------
const states = new WeakMap<PortalState, TicketFixture[]>()

function ticketsOf(userId: string): TicketFixture[] {
  const portal = portalState(userId)
  let list = states.get(portal)
  if (!list) {
    list = build(portal)
    states.set(portal, list)
  }
  return list
}

const ago = (ms: number) => new Date(Date.now() - ms).toISOString()
const ticketNo = () => `TK${new Date().toISOString().slice(0, 10).replaceAll('-', '')}-${randomBytes(5).toString('hex').toUpperCase().slice(0, 8)}`
const priorityOf = (category: string) => (category === 'billing' || category === 'account' ? 'high' : category === 'abuse' ? 'urgent' : 'normal')
const msg = (author_kind: MessageFixture['author_kind'], body: string, at: string): MessageFixture => ({ id: randomUUID(), author_kind, body, created_at: at })

function seed(init: Omit<TicketFixture, 'id' | 'ticket_no' | 'priority' | 'updated_at' | 'resolved_at'> & { resolved_at?: string | null }): TicketFixture {
  return { id: randomUUID(), ticket_no: ticketNo(), priority: priorityOf(init.category), updated_at: init.messages.at(-1)!.created_at, resolved_at: init.resolved_at ?? null, ...init }
}

function build(portal: PortalState): TicketFixture[] {
  const s = scenario()
  if (s === 'empty') return []
  const paid = portal.orders.find((o) => o.status === 'fulfilled' || o.status === 'paid')
  const H = 3_600_000
  const list = [
    seed({
      subject: '香港节点晚高峰丢包严重',
      category: 'technical',
      status: 'open',
      created_at: ago(3 * H),
      closed_reason: null,
      order: null,
      messages: [msg('user', '每天 9 点到 11 点，香港 01 丢包大概 15%，看视频卡顿。换到香港 02 也一样。', ago(3 * H))],
    }),
    seed({
      subject: '订阅链接导入 Clash 失败',
      category: 'technical',
      status: 'pending_user',
      created_at: ago(2 * DAY_MS),
      closed_reason: null,
      order: null,
      messages: [
        msg('user', 'Clash Verge 导入订阅时提示解析失败，已经更新到最新版本。', ago(2 * DAY_MS)),
        msg('agent', '请在「我的订阅」点击 Clash Verge 一键导入；如果仍然失败，把客户端日志截图发上来，我们帮你看。', ago(2 * DAY_MS - 20 * 60_000)),
      ],
    }),
    seed({
      subject: '年付订单需要开发票',
      category: 'billing',
      status: 'closed',
      created_at: ago(20 * DAY_MS),
      resolved_at: ago(19 * DAY_MS),
      closed_reason: 'agent_closed',
      order: paid ? { id: paid.id, order_no: paid.order_no } : null,
      messages: [
        msg('user', '上个月的年付订单需要开具电子发票，抬头是个人。', ago(20 * DAY_MS)),
        msg('agent', '电子发票已发送到你的注册邮箱，请查收。', ago(19 * DAY_MS)),
        msg('system', '客服已关闭此工单', ago(19 * DAY_MS - 60_000)),
      ],
    }),
    seed({
      subject: '续费后流量没有重置',
      category: 'subscription',
      status: 'closed',
      created_at: ago(34 * DAY_MS),
      closed_reason: 'withdrawn',
      order: null,
      messages: [msg('user', '续费成功了，但是本期已用流量还是上个周期的数字。', ago(34 * DAY_MS)), msg('system', '用户已撤回此工单', ago(34 * DAY_MS - 30 * 60_000))],
    }),
  ]
  // multi：未结工单凑满 5 个，新建回 409
  if (s === 'multi') {
    for (const [i, subject] of ['节点列表里看不到东京 03', '设备数上限能否临时提高', '支付完成但订单显示处理中'].entries()) {
      list.push(seed({ subject, category: 'general', status: 'pending_agent', created_at: ago((5 + i) * DAY_MS), closed_reason: null, order: null, messages: [msg('user', `${subject}，麻烦看一下，谢谢。`, ago((5 + i) * DAY_MS))] }))
    }
  }
  return list.sort((a, b) => b.updated_at.localeCompare(a.updated_at))
}

// ---------------------------------------------------------------------------
// 读形状：列表行不带 messages；legacy 去掉修订 R60 的 closed_reason / related_order
// ---------------------------------------------------------------------------
function rowView(t: TicketFixture) {
  const last = t.messages.reduce((m, x) => (x.created_at > m ? x.created_at : m), t.created_at)
  const base = { id: t.id, ticket_no: t.ticket_no, subject: t.subject, category: t.category, priority: t.priority, status: t.status, created_at: t.created_at, updated_at: t.updated_at, resolved_at: t.resolved_at, message_count: t.messages.length, last_reply_at: last }
  return scenario() === 'legacy' ? base : { ...base, closed_reason: t.closed_reason, related_order: null }
}

function detailView(t: TicketFixture, displayName: string | null) {
  const base = {
    ...rowView(t),
    message_count: 0,
    last_reply_at: ZERO_TIME,
    messages: t.messages.map((m) => ({ ...m, author_name: m.author_kind === 'user' ? displayName : null })),
  }
  return scenario() === 'legacy' ? base : { ...base, related_order: t.order }
}

const find = (ctx: MockContext) => (isUuid(ctx.params.id) ? ticketsOf(ctx.user.userId).find((t) => t.id === ctx.params.id) : undefined)
const notFound = () => new BillingError(404, 'not_found', '资源不存在或无权访问').result()
const touch = (t: TicketFixture) => {
  t.updated_at = new Date().toISOString()
}

export const tickets: MockModule = {
  routes: {
    'GET /v1/support/categories': (ctx) => ctx.send(200, { categories: CATEGORIES.map(([code, name]) => ({ code, name })) }),

    'GET /v1/support/tickets': async (ctx) => {
      if (!(await gate(ctx))) return
      const list = [...ticketsOf(ctx.user.userId)].sort((a, b) => b.updated_at.localeCompare(a.updated_at))
      ctx.send(200, { tickets: list.slice(0, 100).map(rowView) })
    },

    'GET /v1/support/tickets/:id': async (ctx) => {
      if (!(await gate(ctx))) return
      const t = find(ctx)
      if (!t) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      ctx.send(200, detailView(t, ctx.user.displayName))
    },

    'POST /v1/support/tickets': async (ctx) => {
      const body = await readStrict(ctx, ['subject', 'category', 'body', 'order_id'])
      if (!body) return
      const str = (v: unknown) => (typeof v === 'string' ? v.trim() : '')
      await ctx.idempotent('support_ticket_create', () => {
        const text = str(body.body)
        const category = str(body.category) || 'general'
        let subject = str(body.subject)
        if (!subject) {
          // subjectFromBody：正文首个非空行，截 120 字，短于 4 字加前缀
          const line = text.split('\n').map((l) => l.trim()).find(Boolean) ?? ''
          subject = line ? ([...line].length < 4 ? `用户咨询：${line}` : [...line].slice(0, 120).join('')) : ''
        }
        const fields: Record<string, string> = {}
        if ([...subject].length < 4 || [...subject].length > 120) fields.subject = '标题需在 4–120 字之间'
        if ([...text].length < 10 || [...text].length > 5000) fields.body = '内容需在 10–5000 字之间'
        if (!CATEGORY_CODES.has(category)) fields.category = '无效的分类'
        if (Object.keys(fields).length) return new BillingError(422, 'validation_failed', '请求参数校验未通过', fields).result()
        const list = ticketsOf(ctx.user.userId)
        if (list.filter((t) => t.status !== 'resolved' && t.status !== 'closed').length >= 5) return new BillingError(409, 'conflict', '您有 5 个进行中的工单，请先处理完再提交新的').result()
        let order: TicketFixture['order'] = null
        const orderId = str(body.order_id)
        if (orderId) {
          const o = portalState(ctx.user.userId).orders.find((x) => x.id === orderId)
          if (!o) return new BillingError(422, 'validation_failed', '请求参数校验未通过', { order_id: '订单不存在' }).result()
          order = { id: o.id, order_no: o.order_no }
        }
        const now = new Date().toISOString()
        const t: TicketFixture = { id: randomUUID(), ticket_no: ticketNo(), subject, category, priority: priorityOf(category), status: 'open', created_at: now, updated_at: now, resolved_at: null, closed_reason: null, order, messages: [msg('user', text, now)] }
        list.unshift(t)
        return { status: 201, body: rowView(t) }
      })
    },

    'POST /v1/support/tickets/:id/reply': async (ctx) => {
      const body = await readStrict(ctx, ['body'])
      if (!body) return
      await ctx.idempotent('support_ticket_user_reply', () => {
        const text = typeof body.body === 'string' ? body.body.trim() : ''
        if ([...text].length < 1 || [...text].length > 5000) return new BillingError(422, 'validation_failed', '请求参数校验未通过', { body: '内容需在 1–5000 字之间' }).result()
        const t = find(ctx)
        if (!t) return notFound()
        if (t.status === 'closed') return new BillingError(409, 'conflict', '工单已关闭，如需继续请新建工单').result()
        t.messages.push(msg('user', text, new Date().toISOString()))
        // 球回到客服；已解决的被追问即重开（修订 R25：重开清空关闭原因）
        t.status = 'pending_agent'
        t.resolved_at = null
        t.closed_reason = null
        touch(t)
        return { status: 200, body: { ok: true } }
      })
    },

    // 不解码请求体；已关闭回 404（不是 409）
    'POST /v1/support/tickets/:id/close': async (ctx) => {
      await ctx.idempotent('support_ticket_user_close', () => {
        const t = find(ctx)
        if (!t || t.status === 'closed') return notFound()
        t.status = 'closed'
        t.closed_reason = 'user_closed'
        t.messages.push(msg('system', '用户已关闭此工单', new Date().toISOString()))
        touch(t)
        return { status: 200, body: { ok: true } }
      })
    },

    // 不幂等；必须带 JSON 体（至少 {}），空体 400
    'POST /v1/support/tickets/:id/withdraw': async (ctx) => {
      if (ctx.req.headers['content-length'] === '0' || !ctx.req.headers['content-length']) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON 对象')
      const body = await readStrict(ctx, ['reason'])
      if (!body) return
      const reason = typeof body.reason === 'string' ? body.reason.trim() : ''
      if ([...reason].length > 500) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { reason: '撤回说明不能超过 500 字' })
      const t = find(ctx)
      if (!t) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      if (t.status === 'closed') return ctx.fail(409, 'conflict', '这个工单已经关闭了')
      if (t.messages.some((m) => m.author_kind === 'agent')) return ctx.fail(409, 'conflict', '客服已经回复过这个工单，不能再撤回。如果问题已解决，请直接关闭')
      t.status = 'closed'
      t.closed_reason = 'withdrawn'
      t.messages.push(msg('system', '用户已撤回此工单', new Date().toISOString()))
      touch(t)
      ctx.send(200, { withdrawn: true })
    },
  },
}
