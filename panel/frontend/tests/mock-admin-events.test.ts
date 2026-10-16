import { describe, expect, it } from 'vitest'
import { orders } from '../dev/mock/admin/billing-store'
import { pickAdminEvent } from '../dev/mock/admin/events'
import { liveNodeIds } from '../dev/mock/admin/nodes'
import { plans } from '../dev/mock/admin/plans-store'
import { ticketIds } from '../dev/mock/admin/tickets'
import { userStore } from '../dev/mock/admin/users'

// 后台实时事件模拟：载荷照 Go 的 { table, op, id }，id 取种子里真有的行，页面点事件能打开那一条
describe('mock api · admin realtime events', () => {
  const known: Record<string, () => readonly string[]> = {
    'orders.changed': () => orders.map((o) => o.id),
    'tickets.changed': ticketIds,
    'nodes.changed': liveNodeIds,
    'subscriptions.changed': () => userStore.flatMap((u) => u.subs.map((s) => s.id)),
    'plans.changed': () => plans.map((p) => p.id),
  }

  it('picks real seeded rows with the Go payload shape', () => {
    for (let i = 0; i < 200; i++) {
      const { topic, payload } = pickAdminEvent()
      expect(Object.keys(payload).sort()).toEqual(['id', 'op', 'table'])
      expect(['INSERT', 'UPDATE']).toContain(payload.op)
      expect(known[topic]?.()).toContain(payload.id)
    }
  })
})
