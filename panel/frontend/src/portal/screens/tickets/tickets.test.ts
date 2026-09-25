/**
 * [INPUT]: 依赖 vitest，依赖 ./api 的 schema，依赖 ./model 的纯逻辑
 * [OUTPUT]: 无（测试）
 * [POS]: 第 ⑤ 步工单的单元测试：列表与详情 schema（R60 字段可缺席、列表不收 null、详情零值 last_reply_at）、子路由、状态文案（closed 按 closed_reason 分）、三个按钮的可见条件、作者名（D-F-2 未决前）、消息时间、新建表单校验
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it } from 'vitest'
import { ticketDetailSchema, ticketRowSchema, type TicketDetail } from './api'
import { authorLabel, canClose, canReply, canWithdraw, messageTime, ticketRoute, ticketStatus, validateTicket } from './model'

const ROW = {
  id: 't1',
  ticket_no: 'TK20260924-ABCD1234',
  subject: '香港节点晚高峰丢包',
  category: 'technical',
  priority: 'normal',
  status: 'open',
  created_at: '2026-09-24T01:00:00Z',
  updated_at: '2026-09-24T01:00:00Z',
  resolved_at: null,
  message_count: 1,
  last_reply_at: '2026-09-24T01:00:00Z',
  closed_reason: null,
  related_order: null,
}

function detail(over: Partial<TicketDetail> = {}): TicketDetail {
  return ticketDetailSchema.parse({ ...ROW, message_count: 0, last_reply_at: '0001-01-01T00:00:00Z', messages: [{ id: 'm1', author_kind: 'user', author_name: null, body: 'x', created_at: ROW.created_at }], ...over })
}

describe('工单 schema', () => {
  it('列表行：related_order 恒为 null；修订 R60 字段缺席也能过（旧后端）', () => {
    expect(ticketRowSchema.parse(ROW).related_order).toBeNull()
    const legacy: Record<string, unknown> = { ...ROW }
    delete legacy.closed_reason
    delete legacy.related_order
    expect(ticketRowSchema.parse(legacy).closed_reason).toBeUndefined()
    expect(ticketRowSchema.safeParse({ ...ROW, status: 'withdrawn' }).success).toBe(false)
  })

  it('详情：related_order 是对象或 null，messages 不收 null', () => {
    expect(detail({ related_order: { id: 'o1', order_no: 'PD-1' } }).related_order?.order_no).toBe('PD-1')
    expect(ticketDetailSchema.safeParse({ ...ROW, messages: null }).success).toBe(false)
  })
})

describe('子路由', () => {
  const q = (s = '') => new URLSearchParams(s)
  it('列表 / 新建（可带订单）/ 某张工单', () => {
    expect(ticketRoute([], q())).toEqual({ kind: 'list' })
    expect(ticketRoute(['new'], q('order=o1'))).toEqual({ kind: 'new', orderId: 'o1' })
    expect(ticketRoute(['new'], q())).toEqual({ kind: 'new', orderId: null })
    expect(ticketRoute(['t1'], q())).toEqual({ kind: 'ticket', id: 't1' })
  })
})

describe('状态与按钮', () => {
  it('状态文案按契约映射，closed 按 closed_reason 分', () => {
    const label = (status: string, closed_reason: 'withdrawn' | 'user_closed' | null = null) => ticketStatus(ticketRowSchema.parse({ ...ROW, status, closed_reason })).label
    expect(['open', 'pending_agent', 'escalated'].map((s) => label(s))).toEqual(['等待回复', '等待回复', '等待回复'])
    expect(label('pending_user')).toBe('客服已回复')
    expect(label('resolved')).toBe('已解决')
    expect(label('closed', 'withdrawn')).toBe('已撤回')
    expect(label('closed', 'user_closed')).toBe('已关闭')
    expect(ticketStatus({ status: 'closed' }).label).toBe('已关闭')
  })

  it('撤回只在未关闭且客服没回复过时出现；关闭与回复只看是否已关闭（resolved 仍可回复）', () => {
    const agent = { id: 'm2', author_kind: 'agent' as const, author_name: null, body: 'y', created_at: ROW.created_at }
    expect(canWithdraw(detail())).toBe(true)
    expect(canWithdraw(detail({ messages: [...detail().messages, agent] }))).toBe(false)
    expect(canWithdraw(detail({ status: 'closed' }))).toBe(false)
    expect(canClose(detail({ status: 'resolved' }))).toBe(true)
    expect(canReply(detail({ status: 'resolved' }))).toBe(true)
    expect(canReply(detail({ status: 'closed' }))).toBe(false)
  })

  it('作者：客服不显示姓名（D-F-2 未决前）', () => {
    expect(authorLabel({ author_kind: 'user' })).toBe('我')
    expect(authorLabel({ author_kind: 'agent' })).toBe('客服')
  })
})

describe('消息时间', () => {
  const now = new Date(2026, 8, 24, 15, 0)
  it('今天只写时分，今年写月-日，更早带年', () => {
    expect(messageTime(new Date(2026, 8, 24, 9, 5).toISOString(), now)).toBe('09:05')
    expect(messageTime(new Date(2026, 8, 12, 20, 0).toISOString(), now)).toBe('09-12 20:00')
    expect(messageTime(new Date(2025, 11, 31, 8, 0).toISOString(), now)).toBe('2025-12-31 08:00')
  })
})

describe('新建表单校验（与 support.create 同口径）', () => {
  it('标题 4–120 字、正文 10–5000 字，去首尾空白按字数计', () => {
    expect(validateTicket({ subject: '', body: '' })).toEqual({ subject: '请用一句话描述问题', body: '请写下详细描述' })
    expect(validateTicket({ subject: '丢包', body: '晚上很卡' }).subject).toBe('标题需在 4–120 字之间')
    expect(validateTicket({ subject: '节点丢包', body: '  晚上很卡  ' }).body).toBe('详细描述至少 10 个字，方便客服定位问题')
    expect(validateTicket({ subject: '节点丢包严重', body: '每天晚上九点到十一点丢包' })).toEqual({})
  })
})
