import { randomUUID } from 'node:crypto'
import type { MockContext, MockModule, MockResult } from '../types.ts'
import { orders } from './billing-store.ts'

// ---------------------------------------------------------------------------
// 状态放在模块级变量里，vite 重启即复原。时间相对启动时刻生成。
// ---------------------------------------------------------------------------
type Status = 'open' | 'pending_user' | 'pending_agent' | 'escalated' | 'resolved' | 'closed'
type Priority = 'low' | 'normal' | 'high' | 'urgent'

interface Msg {
  id: string
  author_kind: 'user' | 'agent' | 'system'
  author_name: string | null
  body: string
  internal_note: boolean
  created_at: string
}

interface Row {
  id: string
  ticket_no: string
  subject: string
  category: string
  priority: Priority
  status: Status
  created_at: string
  updated_at: string
  resolved_at: string | null
  user_id: string
  user_email: string
  user_active_plan: string | null
  assigned_to: string | null
  sla_first_response_due: string
  sla_resolution_due: string
  first_responded_at: string | null
  escalated_at: string | null
  closed_reason: 'user_closed' | 'withdrawn' | 'agent_closed' | 'auto_closed' | null
  /** 用户建单时关联的订单（related_order_id）；详情联表出 { id, order_no } */
  related_order_id: string | null
  messages: Msg[]
}

const MIN = 60_000
const at = (minutesAgo: number) => new Date(Date.now() - minutesAgo * MIN).toISOString()
// support.slaHours：[首次响应, 解决] 小时
const SLA: Record<Priority, [number, number]> = { urgent: [1, 8], high: [4, 24], normal: [12, 72], low: [24, 168] }
const PRIORITY_ORDER: Record<Priority, number> = { urgent: 0, high: 1, normal: 2, low: 3 }
const AGENT_SETTABLE = new Set(['resolved', 'closed', 'escalated', 'pending_agent'])

/** 固定的另外三位客服；当前登录的管理员也会出现在可指派目录里 */
const STAFF = [
  { id: '0b6f1c2a-1111-4a00-8000-000000000001', email: 'zhoumin@example.test', display_name: '周敏' },
  { id: '0b6f1c2a-1111-4a00-8000-000000000002', email: 'tangyiming@example.test', display_name: '唐一鸣' },
  { id: '0b6f1c2a-1111-4a00-8000-000000000003', email: 'night-shift@example.test' },
] as const

function msg(kind: Msg['author_kind'], name: string | null, body: string, minutesAgo: number, internal = false): Msg {
  return { id: randomUUID(), author_kind: kind, author_name: name, body, internal_note: internal, created_at: at(minutesAgo) }
}

function seed(
  n: number,
  subject: string,
  email: string,
  plan: string | null,
  category: string,
  priority: Priority,
  status: Status,
  createdMinutesAgo: number,
  messages: Msg[],
  extra: Partial<Row> = {},
): Row {
  const created = Date.now() - createdMinutesAgo * MIN
  const [first, resolve] = SLA[priority]
  return {
    id: randomUUID(),
    ticket_no: `TK20260924-${String(n).padStart(8, 'A')}`,
    subject,
    category,
    priority,
    status,
    created_at: new Date(created).toISOString(),
    updated_at: messages.at(-1)?.created_at ?? new Date(created).toISOString(),
    resolved_at: null,
    user_id: randomUUID(),
    user_email: email,
    user_active_plan: plan,
    assigned_to: null,
    sla_first_response_due: new Date(created + first * 3_600_000).toISOString(),
    sla_resolution_due: new Date(created + resolve * 3_600_000).toISOString(),
    first_responded_at: messages.find((m) => m.author_kind === 'agent' && !m.internal_note)?.created_at ?? null,
    escalated_at: null,
    closed_reason: null,
    related_order_id: null,
    messages,
    ...extra,
  }
}

const rows: Row[] = [
  seed(4821, '香港节点晚高峰丢包严重', 'wu.qing@163.com', '标准版', 'technical', 'high', 'open', 18, [
    msg('user', null, '每天 9 点到 11 点，HK-HKG-01 丢包大概 15%，看视频卡顿。换了 JP 节点正常。', 18),
  ]),
  seed(4820, '订阅链接在 Clash 中导入失败', 'tomato@sina.com', '家庭版', 'technical', 'normal', 'open', 62, [msg('user', null, 'Clash Verge 提示「解析失败」，截图附上。', 62)], {
    assigned_to: STAFF[0].id,
  }),
  seed(
    4818,
    '支付宝付款后订单仍显示待支付',
    'lin.xiao@foxmail.com',
    '标准版',
    'billing',
    'urgent',
    'escalated',
    184,
    [
      msg('user', null, '已经扣款了，订单 PD-2609-3290 还是待支付。', 184),
      msg('system', null, '首次响应超时，系统自动升级', 120),
      msg('agent', '林舟', '已收到，正在和支付渠道核对回调，稍后给您答复。', 110),
      msg('agent', '林舟', '渠道那边回调确实丢了，等对账结果再补单。', 100, true),
      msg('user', null, '好的，麻烦尽快。', 95),
    ],
    { escalated_at: at(120) },
  ),
  seed(
    4815,
    '想把套餐从标准版升级到专业版',
    'grace.h@icloud.com',
    '专业版',
    'subscription',
    'low',
    'pending_user',
    300,
    [msg('user', null, '剩余天数可以折算吗？', 300), msg('agent', '周敏', '可以，按剩余价值折算后补差价。需要我帮您操作吗？', 270)],
    { assigned_to: STAFF[0].id },
  ),
  seed(4812, '账号被盗，登录记录里有陌生 IP', 'sec.check@outlook.com', null, 'account', 'high', 'open', 7 * 60, [msg('user', null, '昨晚收到异地登录提醒，我已经改了密码。', 7 * 60)]),
  seed(
    4809,
    '设备数超限无法登录',
    'k.liu@proton.me',
    '家庭版',
    'account',
    'normal',
    'resolved',
    26 * 60,
    [
      msg('user', null, '家里第 7 台设备连不上。', 26 * 60),
      msg('agent', '林舟', '已将您的订阅设备上限临时调整为 8 台。', 25 * 60),
      msg('system', null, '客服将工单状态改为 resolved', 25 * 60),
    ],
    { resolved_at: at(25 * 60) },
  ),
  seed(4801, '误提交，已自己解决', 'mira@gmail.com', '标准版', 'general', 'low', 'closed', 50 * 60, [msg('user', null, '不好意思，已经可以用了。', 50 * 60), msg('system', null, '用户撤回了工单', 49 * 60)], {
    closed_reason: 'withdrawn',
  }),
  // support:auto_close：客服回复后用户 24 小时没动静，系统自动关闭
  seed(
    4800,
    '换设备后如何迁移订阅',
    'kai@example.com',
    '标准版',
    'subscription',
    'low',
    'closed',
    55 * 60,
    [
      msg('user', null, '新手机上要重新导入订阅吗？旧手机还能继续用吗？', 55 * 60),
      msg('agent', '林舟', '在新设备上复制订阅链接导入即可，旧设备不受影响。', 54 * 60),
      msg('system', null, '客服回复后 24 小时内没有新的回复，工单已自动关闭；如还有问题请新建工单', 30 * 60),
    ],
    { closed_reason: 'auto_closed' },
  ),
]

// R114：4818（付款后仍待支付）关联一张待支付订单，详情头能看到「关联订单」
const unpaid = orders.find((o) => o.status === 'pending_payment')
const billingTicket = rows.find((r) => r.ticket_no.endsWith('4818'))
if (unpaid && billingTicket) {
  billingTicket.related_order_id = unpaid.id
  billingTicket.messages[0]!.body = `已经扣款了，订单 ${unpaid.order_no} 还是待支付。`
}

// 再造一批旧工单，让「加载更多」有东西可翻
for (let i = 0; i < 26; i++) {
  rows.push(
    seed(4700 - i, `历史咨询 #${i + 1}：节点切换后速度变化`, `user${i + 1}@example.com`, i % 3 ? '标准版' : null, 'general', i % 4 === 0 ? 'normal' : 'low', i % 5 === 0 ? 'pending_user' : 'resolved', (60 + i) * 60, [
      msg('user', null, '切换节点后速度有变化，想确认一下是不是正常的。', (60 + i) * 60),
      msg('agent', '唐一鸣', '属于正常波动，不同线路出口不同。', (59 + i) * 60),
    ]),
  )
}

// 设计稿给了 13 条快捷回复，撑起「前 4 个 + 全部 N」浮层
let macros = [
  { id: randomUUID(), title: '线路排查', body: '您好，我们已记录该线路问题，请提供本地运营商与测速截图，我们会在 2 小时内反馈。', sort_order: 10, created_at: at(900), updated_at: at(900) },
  { id: randomUUID(), title: '重新导入', body: '请在用户端「我的订阅」中复制最新订阅地址，删除旧配置后重新导入。', sort_order: 20, created_at: at(800), updated_at: at(800) },
  { id: randomUUID(), title: '已退款', body: '款项已原路退回，预计 1–3 个工作日到账。', sort_order: 30, created_at: at(700), updated_at: at(700) },
  { id: randomUUID(), title: '流量已重置', body: '已为您重置本期流量，重新连接即可生效。', sort_order: 40, created_at: at(600), updated_at: at(600) },
  { id: randomUUID(), title: '设备数超限', body: '您的套餐最多同时在线 3 台设备，请在用户端「设备管理」里下线旧设备。', sort_order: 50, created_at: at(590), updated_at: at(590) },
  { id: randomUUID(), title: '客户端推荐', body: '推荐 iOS 用 Stash、Android 用 Clash Meta、Windows 用 Clash Verge。', sort_order: 60, created_at: at(580), updated_at: at(580) },
  { id: randomUUID(), title: '续费指引', body: '在用户端「套餐」里选择当前套餐点「续费」，剩余时长会自动叠加。', sort_order: 70, created_at: at(570), updated_at: at(570) },
  { id: randomUUID(), title: '发票申请', body: '请提供抬头与税号，我们会在 3 个工作日内开具并发送到邮箱。', sort_order: 80, created_at: at(560), updated_at: at(560) },
  { id: randomUUID(), title: '节点维护中', body: '该节点正在维护，预计 30 分钟内恢复，请先切换到同地区其他节点。', sort_order: 90, created_at: at(550), updated_at: at(550) },
  { id: randomUUID(), title: '修改邮箱', body: '出于安全考虑，请用旧邮箱发信说明新邮箱地址，我们核实后为您修改。', sort_order: 100, created_at: at(540), updated_at: at(540) },
  { id: randomUUID(), title: '已转技术', body: '已转交技术同事处理，有进展会第一时间回复您。', sort_order: 110, created_at: at(530), updated_at: at(530) },
  { id: randomUUID(), title: '请补充日志', body: '请在客户端「日志」页导出最近 10 分钟的日志并附在回复里。', sort_order: 120, created_at: at(520), updated_at: at(520) },
  { id: randomUUID(), title: '感谢反馈', body: '感谢您的反馈，已记录在改进计划中。', sort_order: 130, created_at: at(510), updated_at: at(510) },
]

// ---------------------------------------------------------------------------
// 视图：与 Go 的 json tag 一致——omitempty 的字段为空时省略，closed_reason / related_order 恒在
// ---------------------------------------------------------------------------
function breached(t: Row): boolean {
  return t.first_responded_at === null && Date.parse(t.sla_first_response_due) < Date.now() && t.status !== 'resolved' && t.status !== 'closed'
}

function relatedOrder(t: Row): { id: string; order_no: string } | null {
  const o = t.related_order_id ? orders.find((x) => x.id === t.related_order_id) : undefined
  return o ? { id: o.id, order_no: o.order_no } : null
}

function lastReplyAt(t: Row): string {
  return t.messages.reduce((max, m) => (m.created_at > max ? m.created_at : max), t.created_at)
}

function view(t: Row, assignees: ReadonlyArray<{ id: string; email: string }>, detail: boolean): Record<string, unknown> {
  const out: Record<string, unknown> = {
    id: t.id,
    ticket_no: t.ticket_no,
    subject: t.subject,
    category: t.category,
    priority: t.priority,
    status: t.status,
    created_at: t.created_at,
    updated_at: t.updated_at,
    resolved_at: t.resolved_at,
    user_id: t.user_id,
    user_email: t.user_email,
    sla_first_response_due: t.sla_first_response_due,
    sla_resolution_due: t.sla_resolution_due,
    // R114：详情的计数与最后回复与队列同口径（内部备注、系统消息都算，没有消息时取建单时间）
    message_count: t.messages.length,
    last_reply_at: lastReplyAt(t),
    closed_reason: t.closed_reason,
    // R114：详情按 related_order_id 联表出 { id, order_no }；队列恒为 null
    related_order: detail ? relatedOrder(t) : null,
  }
  if (t.assigned_to) {
    out.assigned_to = t.assigned_to
    const who = assignees.find((a) => a.id === t.assigned_to)
    if (who) out.assignee_email = who.email
  }
  if (t.first_responded_at) out.first_responded_at = t.first_responded_at
  if (t.escalated_at) out.escalated_at = t.escalated_at
  if (breached(t)) out.sla_breached = true
  if (detail) {
    if (t.user_active_plan) out.user_active_plan = t.user_active_plan
    out.messages = t.messages.map((m) => ({ id: m.id, author_kind: m.author_kind, author_name: m.author_name, body: m.body, ...(m.internal_note ? { internal_note: true } : {}), created_at: m.created_at }))
  } else {
    const last = [...t.messages].reverse().find((m) => !m.internal_note && m.author_kind !== 'system')
    if (last) out.last_message_author_kind = last.author_kind
  }
  return out
}

function assigneesFor(ctx: MockContext) {
  // 后台不分权限：当前管理员总在可指派名单里
  return [{ id: ctx.user.userId, email: ctx.user.email, ...(ctx.user.displayName ? { display_name: ctx.user.displayName } : {}) }, ...STAFF]
}

function find(ctx: MockContext): Row | undefined {
  return rows.find((t) => t.id === ctx.params.id)
}

function envelope(code: string, message: string, fields?: Record<string, string>): MockResult['body'] {
  return { error: { code, message, ...(fields ? { fields } : {}) } }
}

/** 全部工单 id（实时事件模拟取真实行用，见 events.ts） */
export const ticketIds = () => rows.map((r) => r.id)

const notFound: MockResult = { status: 404, body: envelope('not_found', '工单不存在') }
const touch = (t: Row) => {
  t.updated_at = new Date().toISOString()
}

const supportSettings = { wait_for_reply: false, auto_close_enabled: false, auto_close_hours: 24, max_open_tickets: 5 }

async function saveSupportSettings(ctx: MockContext): Promise<MockResult> {
  const body = await ctx.body()
  if (!body) return { status: 400, body: envelope('bad_request', '请求体不是合法的 JSON') }
  const unknown = Object.keys(body).filter((k) => !['wait_for_reply', 'auto_close_enabled', 'auto_close_hours', 'max_open_tickets'].includes(k))
  if (unknown.length) return { status: 400, body: envelope('bad_request', `未知字段：${unknown.join(', ')}`) }
  const fields: Record<string, string> = {}
  if (typeof body.wait_for_reply !== 'boolean') fields.wait_for_reply = '必填'
  if (typeof body.auto_close_enabled !== 'boolean') fields.auto_close_enabled = '必填'
  if (typeof body.auto_close_hours !== 'number' || !Number.isInteger(body.auto_close_hours)) fields.auto_close_hours = '必填'
  else if (body.auto_close_hours < 1 || body.auto_close_hours > 720) fields.auto_close_hours = '自动关闭时长要在 1 到 720 小时之间'
  if (typeof body.max_open_tickets !== 'number' || !Number.isInteger(body.max_open_tickets)) fields.max_open_tickets = '必填'
  else if (body.max_open_tickets < 1 || body.max_open_tickets > 50) fields.max_open_tickets = '同时打开的工单上限要在 1 到 50 之间'
  if (Object.keys(fields).length) return { status: 422, body: envelope('validation_failed', '请求参数校验未通过', fields) }
  Object.assign(supportSettings, {
    wait_for_reply: body.wait_for_reply === true,
    auto_close_enabled: body.auto_close_enabled === true,
    auto_close_hours: body.auto_close_hours as number,
    max_open_tickets: body.max_open_tickets as number,
  })
  return { status: 200, body: { ...supportSettings } }
}

export const tickets: MockModule = {
  routes: {
    'GET /v1/settings/support': (ctx) => {
      ctx.send(200, { ...supportSettings })
    },
    'POST /v1/settings/support': async (ctx) => {
      if (!ctx.requireReauth()) return
      const r = await saveSupportSettings(ctx)
      ctx.send(r.status, r.body)
    },

    'GET /v1/tickets/assignees': (ctx) => {
      ctx.send(200, { assignees: assigneesFor(ctx) })
    },

    // 队列：status 逗号分隔（R60）、assigned_to、q（编号 / 标题 / 邮箱模糊）、breached=1、limit ≤ 100（默认 25）、offset
    'GET /v1/tickets': (ctx) => {
      const qs = ctx.query
      const statuses = (qs.get('status') ?? '').replaceAll(' ', '').split(',').filter(Boolean)
      const priority = qs.get('priority') ?? ''
      const category = qs.get('category') ?? ''
      const assignee = qs.get('assigned_to') ?? ''
      const text = (qs.get('q') ?? '').toLowerCase()
      const onlyBreach = qs.get('breached') === '1'
      let limit = Number.parseInt(qs.get('limit') ?? '', 10)
      if (!(limit > 0 && limit <= 100)) limit = 25
      const offset = Math.max(0, Number.parseInt(qs.get('offset') ?? '', 10) || 0)
      const hits = rows
        .filter((t) => statuses.length === 0 || statuses.includes(t.status))
        .filter((t) => !priority || t.priority === priority)
        .filter((t) => !category || t.category === category)
        .filter((t) => !assignee || t.assigned_to === assignee)
        .filter((t) => !text || `${t.ticket_no} ${t.subject} ${t.user_email}`.toLowerCase().includes(text))
        .filter((t) => !onlyBreach || breached(t))
        .sort((a, b) => Number(b.status === 'escalated') - Number(a.status === 'escalated') || PRIORITY_ORDER[a.priority] - PRIORITY_ORDER[b.priority] || a.created_at.localeCompare(b.created_at))
      const staff = assigneesFor(ctx)
      ctx.send(200, { tickets: hits.slice(offset, offset + limit).map((t) => view(t, staff, false)), total: hits.length })
    },

    // 立即执行 SLA 扫描：静态段，要排在 :id 之前（findRoute 按键顺序匹配）
    'POST /v1/tickets/escalate': async (ctx) => {
      await ctx.idempotent('admin_ticket_escalate', () => {
        let n = 0
        for (const t of rows) {
          if (!breached(t) || t.status === 'escalated') continue
          t.status = 'escalated'
          t.escalated_at ??= new Date().toISOString()
          t.priority = t.priority === 'low' ? 'normal' : t.priority === 'normal' ? 'high' : 'urgent'
          t.messages.push(msg('system', null, '首次响应超时，系统自动升级', 0))
          touch(t)
          n++
        }
        return { status: 200, body: { escalated: n } }
      })
    },

    'GET /v1/tickets/:id': (ctx) => {
      const t = find(ctx)
      if (!t) return ctx.fail(404, 'not_found', '工单不存在')
      ctx.send(200, view(t, assigneesFor(ctx), true))
    },

    // 回复 / 内部备注：非内部回复 → pending_user、清 resolved_at，首回记 first_responded_at；备注不改状态
    'POST /v1/tickets/:id/reply': async (ctx) => {
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('admin_ticket_reply', () => {
        const text = typeof body.body === 'string' ? body.body.trim() : ''
        const internal = body.internal_note === true
        const n = [...text].length
        if (n < 1 || n > 5000) return { status: 422, body: envelope('validation_failed', '请求参数校验未通过', { body: '内容需在 1–5000 字之间' }) }
        const t = find(ctx)
        if (!t) return notFound
        t.messages.push(msg('agent', ctx.user.displayName, text, 0, internal))
        if (!internal) {
          t.status = 'pending_user'
          t.resolved_at = null
          t.closed_reason = null
          t.first_responded_at ??= new Date().toISOString()
        }
        touch(t)
        return { status: 200, body: { ok: true } }
      })
    },

    'POST /v1/tickets/:id/assign': async (ctx) => {
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('admin_ticket_assign', () => {
        const target = typeof body.assigned_to === 'string' ? body.assigned_to : ''
        if (target && !assigneesFor(ctx).some((a) => a.id === target)) {
          return { status: 422, body: envelope('validation_failed', '请求参数校验未通过', { assigned_to: '该用户没有工单处理权限' }) }
        }
        const t = find(ctx)
        if (!t) return notFound
        t.assigned_to = target || null
        touch(t)
        return { status: 200, body: { ok: true } }
      })
    },

    // 改状态：只收 resolved / closed / escalated / pending_agent；写 system 消息；
    // 人工升级把优先级提到至少 high（R60）；客服关闭写 agent_closed，重新打开清空（R25）
    'POST /v1/tickets/:id/status': async (ctx) => {
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('admin_ticket_status', () => {
        const status = typeof body.status === 'string' ? body.status.trim() : ''
        const reason = typeof body.reason === 'string' ? body.reason.trim() : ''
        if (!AGENT_SETTABLE.has(status)) return { status: 422, body: envelope('validation_failed', '请求参数校验未通过', { status: '不支持直接设置该状态' }) }
        if ([...reason].length > 500) return { status: 422, body: envelope('validation_failed', '请求参数校验未通过', { reason: '原因不能超过 500 个字符' }) }
        const t = find(ctx)
        if (!t) return notFound
        const before = t.status
        const next = status as Status
        const now = new Date().toISOString()
        t.status = next
        t.resolved_at = next === 'resolved' ? (t.resolved_at ?? now) : next === 'closed' ? t.resolved_at : null
        t.closed_reason = next !== 'closed' ? null : before === 'closed' ? (t.closed_reason ?? 'agent_closed') : 'agent_closed'
        if (next === 'escalated') {
          t.escalated_at ??= now
          if (t.priority === 'low' || t.priority === 'normal') t.priority = 'high'
        }
        t.messages.push(msg('system', null, `客服将工单状态改为 ${next}${reason ? `：${reason}` : ''}`, 0))
        touch(t)
        return { status: 200, body: { ok: true } }
      })
    },

    // 快捷回复（R42）：配置类写操作，不带幂等、不要 reauth
    'GET /v1/ticket-macros': (ctx) => {
      const sorted = [...macros].sort((a, b) => a.sort_order - b.sort_order || a.created_at.localeCompare(b.created_at))
      ctx.send(200, { macros: sorted.map((m) => ({ id: m.id, title: m.title, body: m.body, sort_order: m.sort_order, updated_at: m.updated_at })) })
    },
    'POST /v1/ticket-macros': (ctx) => saveMacro(ctx, null),
    'POST /v1/ticket-macros/:id': (ctx) => saveMacro(ctx, ctx.params.id ?? null),
    'DELETE /v1/ticket-macros/:id': (ctx) => {
      const before = macros.length
      macros = macros.filter((m) => m.id !== ctx.params.id)
      if (macros.length === before) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      ctx.send(200, { ok: true })
    },
  },
}

async function saveMacro(ctx: MockContext, id: string | null) {
  const body = await ctx.body()
  if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
  const unknown = Object.keys(body).filter((k) => !['title', 'body', 'sort_order'].includes(k))
  if (unknown.length) return ctx.fail(400, 'bad_request', `未知字段：${unknown.join(', ')}`)
  const title = typeof body.title === 'string' ? body.title.trim() : ''
  const text = typeof body.body === 'string' ? body.body.trim() : ''
  const sortOrder = typeof body.sort_order === 'number' && Number.isInteger(body.sort_order) ? body.sort_order : 0
  const fields: Record<string, string> = {}
  if ([...title].length < 1 || [...title].length > 20) fields.title = '标题必须为 1 到 20 个字'
  if ([...text].length < 1 || [...text].length > 5000) fields.body = '内容必须为 1 到 5000 个字'
  if (Object.keys(fields).length) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', fields)
  const now = new Date().toISOString()
  if (id === null) {
    const created = { id: randomUUID(), title, body: text, sort_order: sortOrder, created_at: now, updated_at: now }
    macros.push(created)
    return ctx.send(200, { id: created.id })
  }
  const hit = macros.find((m) => m.id === id)
  if (!hit) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
  Object.assign(hit, { title, body: text, sort_order: sortOrder, updated_at: now })
  ctx.send(200, { id })
}
