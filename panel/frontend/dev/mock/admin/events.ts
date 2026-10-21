import { randomUUID } from 'node:crypto'
import { orders } from './billing-store.ts'
import { liveNodeIds } from './nodes.ts'
import { plans } from './plans-store.ts'
import { freshTicketIds, ticketIds } from './tickets.ts'
import { userStore } from './users.ts'

// ---------------------------------------------------------------------------
// 后台 SSE 的模拟变更（mock-api.ts 的 stream 定时调用）。载荷形状照 Go realtime.Publisher.Changed：
// { table, op, id }，表名照各模块 service/realtime.go。id 尽量取种子里真有的那一行，
// 页面上点事件能直接打开那一条；对应的种子空了才用随机 id（页面显示「不存在或已删除」）。
// INSERT 只挑「看起来刚建」的行（待支付单、生效订阅、草稿套餐、没人接手的工单）；节点模块只导出 id，
// 看不出新旧，不发 INSERT，避免「新节点」却点到早就在跑的旧节点。
// ---------------------------------------------------------------------------

export interface MockEvent {
  topic: string
  payload: { table: string; op: string; id: string }
}

interface Source {
  topic: string
  table: string
  /** UPDATE 任挑的 id */
  all: () => readonly string[]
  /** INSERT 只从这里挑：看起来刚建的行；不给 = 这张表不发 INSERT */
  fresh?: () => readonly string[]
}

const pickOne = <T>(list: readonly T[]): T | undefined => list[Math.floor(Math.random() * list.length)]

/** 按字段（ISO 时间）降序取前 3 行的 id，字段为 null 的排最后 */
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

const SOURCES: readonly Source[] = [
  {
    topic: 'orders.changed',
    table: 'orders',
    all: () => orders.map((o) => o.id),
    fresh: () =>
      top3IdsByDesc(
        orders.filter((o) => o.status === 'pending_payment'),
        (o) => o.created_at,
      ),
  },
  {
    topic: 'tickets.changed',
    table: 'tickets',
    all: ticketIds,
    fresh: freshTicketIds,
  },
  {
    topic: 'nodes.changed',
    table: 'nodes',
    all: liveNodeIds,
    // nodes.ts 只导出 id，看不出新旧，不发 INSERT
  },
  {
    topic: 'subscriptions.changed',
    table: 'subscriptions',
    all: () => allSubs().map((s) => s.id),
    fresh: () =>
      top3IdsByDesc(
        allSubs().filter((s) => s.status === 'active'),
        (s) => s.current_period_start,
      ),
  },
  {
    topic: 'plans.changed',
    table: 'plans',
    all: () => plans.map((p) => p.id),
    fresh: () =>
      top3IdsByDesc(
        plans.filter((p) => p.status === 'draft'),
        (p) => p.created_at,
      ),
  },
]

/**
 * 随机挑一条后台变更：多数是 UPDATE。
 * INSERT 仅从 orders / subscriptions / plans / tickets 的 fresh 集合挑；nodes 只发 UPDATE。
 * 删除不随机模拟（真删除由各写接口触发，不到这里）。
 */
export function pickAdminEvent(): MockEvent {
  const source = pickOne(SOURCES)!
  const fresh = pickOne(['INSERT', 'UPDATE', 'UPDATE']) === 'INSERT' ? (source.fresh?.() ?? []) : []
  const id = pickOne(fresh)
  if (id !== undefined) return { topic: source.topic, payload: { table: source.table, op: 'INSERT', id } }
  // 抽到 UPDATE，或这张表没有看起来刚建的行：退回 UPDATE 任挑
  return { topic: source.topic, payload: { table: source.table, op: 'UPDATE', id: pickOne(source.all()) ?? randomUUID() } }
}
