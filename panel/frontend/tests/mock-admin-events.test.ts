import { describe, expect, it, vi } from 'vitest'
import { orders } from '../dev/mock/admin/billing-store'
import { pickAdminEvent } from '../dev/mock/admin/events'
import { liveNodeIds } from '../dev/mock/admin/nodes'
import { plans } from '../dev/mock/admin/plans-store'
import { ticketIds } from '../dev/mock/admin/tickets'
import { userStore } from '../dev/mock/admin/users'

// 期望集合在测试里独立算一遍（不从 events.ts 导出内部函数）：按 ISO 时间降序取前 3，null 排最后
function top3IdsByDesc<T extends { id: string }>(items: readonly T[], field: (item: T) => string | null): readonly string[] {
  return [...items]
    .sort((a, b) => {
      const va = field(a)
      const vb = field(b)
      if (va === null || vb === null) return va === vb ? 0 : va === null ? 1 : -1
      return vb.localeCompare(va)
    })
    .slice(0, 3)
    .map((item) => item.id)
}

const allSubs = () => userStore.flatMap((u) => u.subs)

const freshPendingOrderIds = () =>
  top3IdsByDesc(
    orders.filter((o) => o.status === 'pending_payment'),
    (o) => o.created_at,
  )

const freshActiveSubIds = () =>
  top3IdsByDesc(
    allSubs().filter((s) => s.status === 'active'),
    (s) => s.current_period_start,
  )

const freshDraftPlanIds = () =>
  top3IdsByDesc(
    plans.filter((p) => p.status === 'draft'),
    (p) => p.created_at,
  )

// 后台实时事件模拟：载荷照 Go 的 { table, op, id }，id 取种子里真有的行，页面点事件能打开那一条
describe('mock api · admin realtime events', () => {
  const known: Record<string, () => readonly string[]> = {
    'orders.changed': () => orders.map((o) => o.id),
    'tickets.changed': ticketIds,
    'nodes.changed': liveNodeIds,
    'subscriptions.changed': () => allSubs().map((s) => s.id),
    'plans.changed': () => plans.map((p) => p.id),
  }

  it('picks real seeded rows with the Go payload shape', () => {
    for (let i = 0; i < 300; i++) {
      const { topic, payload } = pickAdminEvent()
      expect(Object.keys(payload).sort()).toEqual(['id', 'op', 'table'])
      expect(['INSERT', 'UPDATE']).toContain(payload.op)
      expect(known[topic]?.()).toContain(payload.id)
    }
  })

  it('INSERT only picks rows that look newly created', () => {
    const pendingTop = freshPendingOrderIds()
    const activeTop = freshActiveSubIds()
    const draftTop = freshDraftPlanIds()

    for (let i = 0; i < 300; i++) {
      const { topic, payload } = pickAdminEvent()
      if (payload.op !== 'INSERT') continue

      switch (topic) {
        case 'orders.changed': {
          const order = orders.find((o) => o.id === payload.id)
          expect(order?.status).toBe('pending_payment')
          expect(pendingTop).toContain(payload.id)
          break
        }
        case 'subscriptions.changed': {
          const sub = allSubs().find((s) => s.id === payload.id)
          expect(sub?.status).toBe('active')
          expect(activeTop).toContain(payload.id)
          break
        }
        case 'plans.changed': {
          const plan = plans.find((p) => p.id === payload.id)
          expect(plan?.status).toBe('draft')
          expect(draftTop).toContain(payload.id)
          break
        }
        case 'tickets.changed':
        case 'nodes.changed':
          expect.fail(`INSERT should not appear on ${topic}`)
          break
        default:
          break
      }
    }
  })

  it('deterministic INSERT picks a fresh pending order when random selects orders + INSERT', () => {
    const fresh = [...freshPendingOrderIds()]
    expect(fresh.length).toBeGreaterThan(0)

    const randomSpy = vi.spyOn(Math, 'random')
    randomSpy.mockReturnValueOnce(0) // SOURCES[0] orders
    randomSpy.mockReturnValueOnce(0) // op INSERT
    randomSpy.mockReturnValueOnce(0) // first id in fresh list

    const { topic, payload } = pickAdminEvent()
    expect(topic).toBe('orders.changed')
    expect(payload.op).toBe('INSERT')
    expect(fresh).toContain(payload.id)

    randomSpy.mockRestore()
  })
})
