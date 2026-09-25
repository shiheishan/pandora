/**
 * [INPUT]: 依赖 vitest，依赖 ./model 的全部纯函数，依赖 ./api 的 queueSchema
 * [OUTPUT]: 工单映射与 schema 的单元测试
 * [POS]: admin/screens/tickets 的纯逻辑测试：分段筛选到后端 query、状态下拉的可设与置灰、等待时长与 SLA 文案、消息气泡角色与发言人、消息时间、指派下拉；schema 守住后端 omitempty 与封闭枚举
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it } from 'vitest'
import { queueSchema } from './api'
import { assigneeOptions, isFilter, messageTime, messageView, queueParams, slaLines, STATUS_OPTIONS, statusView, systemText, waitLabel } from './model'

const NOW = new Date('2026-09-24T12:00:00')
const ago = (minutes: number) => new Date(NOW.getTime() - minutes * 60_000).toISOString()
const later = (minutes: number) => new Date(NOW.getTime() + minutes * 60_000).toISOString()

describe('分段筛选 → 后端 query', () => {
  it('按契约映射，搜索词去空白后带上', () => {
    expect(queueParams('active', 'me', '')).toEqual({ status: 'open,pending_user,pending_agent,escalated' })
    expect(queueParams('todo', 'me', '  TK2026 ')).toEqual({ status: 'open,pending_agent,escalated', q: 'TK2026' })
    expect(queueParams('mine', 'u-1', '')).toEqual({ status: 'open,pending_user,pending_agent,escalated', assigned_to: 'u-1' })
    expect(queueParams('breached', 'me', '')).toEqual({ breached: '1' })
    expect(queueParams('all', 'me', 'x')).toEqual({ q: 'x' })
  })

  it('「我的」在拿不到自己 id 时不退化成全部未解决', () => {
    expect(queueParams('mine', undefined, '').assigned_to).toBe('-')
  })

  it('地址里的筛选值只认已知的', () => {
    expect(isFilter('mine')).toBe(true)
    expect(isFilter('open')).toBe(false)
    expect(isFilter(null)).toBe(false)
  })
})

describe('状态', () => {
  it('escalated 显示为处理中；closed 是设计稿没有的「已关闭」', () => {
    expect(statusView('escalated')).toEqual({ label: '处理中', tone: 'info' })
    expect(statusView('closed').label).toBe('已关闭')
  })

  it('下拉：open / pending_user 置灰；escalated 只在当前就是它时出现且置灰', () => {
    const plain = STATUS_OPTIONS('open')
    expect(plain.map((o) => [o.value, o.disabled])).toEqual([
      ['open', true],
      ['pending_user', true],
      ['pending_agent', false],
      ['resolved', false],
      ['closed', false],
    ])
    expect(STATUS_OPTIONS('escalated').find((o) => o.value === 'escalated')).toEqual({ value: 'escalated', label: '处理中 · 已升级', disabled: true })
  })
})

describe('等待时长与 SLA', () => {
  it('等待 = now − last_reply_at；SLA 超时标红；结束的不算等待', () => {
    expect(waitLabel({ status: 'open', last_reply_at: ago(18) }, NOW)).toEqual({ text: '等待 18 分钟', late: false })
    expect(waitLabel({ status: 'pending_agent', last_reply_at: ago(184), sla_breached: true }, NOW)).toEqual({ text: '等待 3 小时 4 分 · SLA 超时', late: true })
    expect(waitLabel({ status: 'resolved', last_reply_at: ago(5) }, NOW)).toEqual({ text: '已结束', late: false })
  })

  it('首次响应：已响应 / 剩余 / 超时；解决时限随状态', () => {
    expect(slaLines({ status: 'open', sla_first_response_due: later(30), sla_resolution_due: later(600), resolved_at: null }, NOW)).toEqual([
      { label: '首次响应', text: '剩 30 分钟', tone: 'warn' },
      { label: '解决时限', text: '剩 10 小时', tone: 'neutral' },
    ])
    expect(slaLines({ status: 'escalated', sla_first_response_due: ago(125), sla_resolution_due: later(3000), resolved_at: null }, NOW)[0]).toEqual({
      label: '首次响应',
      text: '已超时 2 小时 5 分',
      tone: 'danger',
    })
    expect(slaLines({ status: 'resolved', sla_first_response_due: ago(600), first_responded_at: ago(590), sla_resolution_due: ago(10), resolved_at: ago(30) }, NOW)).toEqual([
      { label: '首次响应', text: '已响应 · 02:10', tone: 'ok' },
      { label: '解决时限', text: '已解决 · 11:30', tone: 'ok' },
    ])
  })
})

describe('消息', () => {
  const base = { id: 'm', body: 'x', created_at: ago(5) }

  it('发言人：author_name ?? (user ? 用户邮箱 : 客服)；备注与系统单独成类', () => {
    expect(messageView({ ...base, author_kind: 'user', author_name: null }, 'a@b.c', NOW)).toEqual({ kind: 'user', who: 'a@b.c', at: '11:55' })
    expect(messageView({ ...base, author_kind: 'agent', author_name: null }, 'a@b.c', NOW).who).toBe('客服')
    expect(messageView({ ...base, author_kind: 'agent', author_name: '林舟', internal_note: true }, 'a@b.c', NOW)).toMatchObject({ kind: 'note', who: '林舟' })
    expect(messageView({ ...base, author_kind: 'system', author_name: null }, 'a@b.c', NOW).kind).toBe('system')
  })

  it('系统消息里的状态码换成界面叫法，其余原样', () => {
    expect(systemText('客服将工单状态改为 resolved')).toBe('客服将工单状态改为「已解决」')
    expect(systemText('客服将工单状态改为 closed：用户确认已恢复')).toBe('客服将工单状态改为「已关闭」：用户确认已恢复')
    expect(systemText('客服将工单状态改为 escalated')).toBe('客服将工单状态改为「已升级」')
    expect(systemText('首次响应超时，系统自动升级')).toBe('首次响应超时，系统自动升级')
  })

  it('时间：今天只给时分，更早给月日，跨年带年份', () => {
    expect(messageTime('2026-09-24T09:05:00', NOW)).toBe('09:05')
    expect(messageTime('2026-09-22T11:20:00', NOW)).toBe('09-22 11:20')
    expect(messageTime('2025-12-31T23:59:00', NOW)).toBe('2025-12-31 23:59')
  })
})

describe('指派下拉', () => {
  it('display_name ?? email；当前指派人不在目录里也能显示', () => {
    const list = [
      { id: 'a', email: 'zhou@x', display_name: '周敏' },
      { id: 'b', email: 'night@x' },
    ]
    expect(assigneeOptions(list, {})).toEqual([
      { value: '', label: '未指派' },
      { value: 'a', label: '周敏' },
      { value: 'b', label: 'night@x' },
    ])
    expect(assigneeOptions(list, { id: 'gone', email: 'old@x' }).at(-1)).toEqual({ value: 'gone', label: 'old@x（已不可指派）' })
  })
})

describe('schema', () => {
  const row = {
    id: 't',
    ticket_no: 'TK20260924-AAAA4821',
    subject: 's',
    category: 'technical',
    priority: 'high',
    status: 'open',
    created_at: ago(10),
    updated_at: ago(10),
    resolved_at: null,
    message_count: 1,
    last_reply_at: ago(10),
    closed_reason: null,
    related_order: null,
  }

  it('omitempty 字段可缺（后端空值时整个键不出现）', () => {
    expect(queueSchema.safeParse({ tickets: [row], total: 1 }).success).toBe(true)
  })

  it('封闭枚举之外的状态、缺 closed_reason 都判为不符约定', () => {
    expect(queueSchema.safeParse({ tickets: [{ ...row, status: 'withdrawn' }], total: 1 }).success).toBe(false)
    const missing: Record<string, unknown> = { ...row }
    delete missing.closed_reason
    expect(queueSchema.safeParse({ tickets: [missing], total: 1 }).success).toBe(false)
  })
})
