// 照 Go ledger 的 GET /v1/users/:id/balance-entries；流水只在本文件一份，调余额经 recordAdjust 追加。
// 门户假后端是另一个进程，钱包流水不与这里同步。

import { randomUUID } from 'node:crypto'
import type { MockRoute } from '../types.ts'
import type { User } from './users.ts'

export interface LedgerEntry {
  id: string
  kind: string
  delta: number
  balance_after: number
  memo: string
  order_id: string | null
  at: string
}

/** 按用户 id 存的流水，旧的在前；第一次读到某人时按他当前余额生成种子（vite 重启即复原） */
const store = new Map<string, LedgerEntry[]>()

export interface LedgerDeps {
  users: User[]
  /** 这个用户的订单（订单归 billing，经 users.ts 的 orderSource 取），种子里「下单用余额」挂在第一张上 */
  ordersOf: (u: User) => ReadonlyArray<{ id: string; created_at: string }>
}

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

function t(created: number, f: number): string {
  return new Date(created + f * (Date.now() - created)).toISOString()
}

function seed(u: User, deps: LedgerDeps): LedgerEntry[] {
  if (u.balance === 0) return []

  const created = Date.parse(u.created_at)
  const orders = [...deps.ordersOf(u)].sort((a, b) => a.created_at.localeCompare(b.created_at))
  const firstOrder = orders[0]

  let running = 0
  const mk = (kind: string, delta: number, memo: string, order_id: string | null, at: string): LedgerEntry => {
    running += delta
    return { id: randomUUID(), kind, delta, balance_after: running, memo, order_id, at }
  }

  // 中间余额都不为负（真库里用户余额有非负约束）：先充值、再补偿，最后一笔出账把余额落到 u.balance
  const e1 = mk('balance_topup', u.balance + 3000, '余额充值', null, t(created, 0.25))
  const e2 = mk('balance_adjusted', 1000, '补偿：节点故障 2 小时', null, t(created, 0.5))
  const e3 = firstOrder
    ? mk('balance_hold', -4000, '', firstOrder.id, t(created, 0.75))
    : mk('balance_adjusted', -4000, '线下退款后冲平余额', null, t(created, 0.75))

  return [e1, e2, e3]
}

function entriesOf(u: User, deps: LedgerDeps): LedgerEntry[] {
  let list = store.get(u.id)
  if (!list) {
    list = seed(u, deps)
    store.set(u.id, list)
  }
  return list
}

/**
 * 人工调账追加流水：必须在 users.ts 里改 u.balance 之前调用（先按旧余额补种子，再追加）。
 */
export function recordAdjust(u: User, deps: LedgerDeps, amount: number, reason: string): void {
  const list = entriesOf(u, deps)
  list.push({
    id: randomUUID(),
    kind: 'balance_adjusted',
    delta: amount,
    balance_after: u.balance + amount,
    memo: reason.trim(),
    order_id: null,
    at: new Date().toISOString(),
  })
}

function parseNonNeg(raw: string | null, min: number): number | null | undefined {
  if (raw === null || raw === '') return undefined
  if (!/^\d+$/.test(raw)) return null
  const n = Number(raw)
  if (n < min) return null
  return n
}

export function ledgerRoutes(deps: LedgerDeps): Record<string, MockRoute> {
  return {
    'GET /v1/users/:id/balance-entries': (ctx) => {
      const id = ctx.params.id ?? ''
      if (!UUID_RE.test(id)) {
        ctx.fail(404, 'not_found', '资源不存在或无权访问')
        return
      }

      const limitRaw = parseNonNeg(ctx.query.get('limit'), 1)
      if (limitRaw === null) {
        ctx.fail(422, 'validation_failed', '请求参数校验未通过', { limit: '格式不正确' })
        return
      }
      const offsetRaw = parseNonNeg(ctx.query.get('offset'), 0)
      if (offsetRaw === null) {
        ctx.fail(422, 'validation_failed', '请求参数校验未通过', { offset: '格式不正确' })
        return
      }

      const limit = Math.min(limitRaw ?? 20, 100)
      const offset = offsetRaw ?? 0

      const u = deps.users.find((x) => x.id === id)
      if (!u) {
        ctx.send(200, { entries: [], total: 0 })
        return
      }

      const all = [...entriesOf(u, deps)].reverse()
      ctx.send(200, { entries: all.slice(offset, offset + limit), total: all.length })
    },
  }
}
