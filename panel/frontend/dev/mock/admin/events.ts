import { randomUUID } from 'node:crypto'
import { orders } from './billing-store.ts'
import { liveNodeIds } from './nodes.ts'
import { plans } from './plans-store.ts'
import { ticketIds } from './tickets.ts'
import { userStore } from './users.ts'

// ---------------------------------------------------------------------------
// 后台 SSE 的模拟变更（mock-api.ts 的 stream 定时调用）。载荷形状照 Go realtime.Publisher.Changed：
// { table, op, id }，表名照各模块 service/realtime.go。id 尽量取种子里真有的那一行，
// 页面上点事件能直接打开那一条；对应的种子空了才用随机 id（页面显示「不存在或已删除」）。
// ---------------------------------------------------------------------------

export interface MockEvent {
  topic: string
  payload: { table: string; op: string; id: string }
}

const pickOne = <T>(list: readonly T[]): T | undefined => list[Math.floor(Math.random() * list.length)]

const SOURCES: ReadonlyArray<readonly [topic: string, table: string, ids: () => readonly string[]]> = [
  ['orders.changed', 'orders', () => orders.map((o) => o.id)],
  ['tickets.changed', 'tickets', ticketIds],
  ['nodes.changed', 'nodes', liveNodeIds],
  ['subscriptions.changed', 'subscriptions', () => userStore.flatMap((u) => u.subs.map((s) => s.id))],
  ['plans.changed', 'plans', () => plans.map((p) => p.id)],
]

/** 随机挑一条后台变更：多数是 UPDATE；只发 INSERT / UPDATE，删除不随机模拟（真删除由各写接口触发不到这里） */
export function pickAdminEvent(): MockEvent {
  const [topic, table, ids] = pickOne(SOURCES)!
  const op = pickOne(['INSERT', 'UPDATE', 'UPDATE'])!
  return { topic, payload: { table, op, id: pickOne(ids()) ?? randomUUID() } }
}
