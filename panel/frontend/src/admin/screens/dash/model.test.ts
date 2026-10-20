import { describe, expect, it } from 'vitest'
import type { TaskItem } from '../../tasks'
import { nodeTrafficSchema, overviewSchema, type NodeTraffic, type Overview } from './api'
import {
  asOfText,
  formatDuration,
  formatPercent,
  greeting,
  integrityLabel,
  kpiTiles,
  percentChange,
  todoBoard,
} from './model'

const REVENUE_ROW = {
  today: 1,
  yesterday: 1,
  last_7_days: 1,
  last_30_days: 1,
  total: 1,
  actual_today: 1,
  actual_7_days: 1,
  actual_30_days: 1,
  actual_total: 1,
  adjustment_today: 0,
  adjustment_7_days: 0,
  adjustment_30_days: 0,
  adjustment_total: 0,
  actual_yesterday: 1,
}

const OVERVIEW: Overview = {
  users: { total: 100, active: 80, today: 2, last_7_days: 10 },
  subscriptions: { active: 50, expiring_7_days: 3, expired: 5, new_7_days: 8 },
  revenue: REVENUE_ROW,
  orders: { paid_today: 4, pending: 1, failed_today: 0 },
  ledger_drift_accounts: 0,
  nodes: { total: 10, online: 9 },
}

const TRAFFIC_24H: NodeTraffic = {
  range: '24h',
  snapshot_at: '2026-09-24T08:00:00.000000Z',
  from: '2026-09-23T08:00:00.000000Z',
  to: '2026-09-24T08:00:00.000000Z',
  basis: 'strict_raw_report_entries',
  items: [],
  totals: { reported_bytes: '1024000', attributed_bytes: '1024000', unattributed_bytes: '0' },
  ranking: { returned_bytes: '0', other_node_bytes: '0' },
  quality: { invalid_report_count: 0, invalid_entry_count: 0 },
}

describe('格式化', () => {
  it('formatPercent / percentChange', () => {
    expect(formatPercent(0.124)).toBe('+12.4%')
    expect(formatPercent(-0.031)).toBe('−3.1%')
    expect(formatPercent(0.0001)).toBe('0.0%')
    expect(percentChange(110, 100)).toBeCloseTo(0.1)
    expect(percentChange(50, 0)).toBeNull()
    expect(percentChange(-50, -100)).toBeCloseTo(0.5)
  })

  it('formatDuration', () => {
    expect(formatDuration(30)).toBe('不到 1 分钟')
    expect(formatDuration(26 * 60)).toBe('26 分钟')
    expect(formatDuration(3 * 3600 + 540)).toBe('3 小时')
    expect(formatDuration(50 * 3600)).toBe('2 天')
  })
})

describe('greeting', () => {
  const me = { display_name: '林舟', email: 'lin@example.test' }

  it('下午好 + 周五日期', () => {
    expect(greeting(new Date(2026, 9, 16, 14, 32), me)).toEqual({ date: '10 月 16 日 周五', hello: '下午好，林舟' })
  })

  it('display_name 为 null 时取邮箱本地部分', () => {
    expect(greeting(new Date(2026, 9, 16, 14, 32), { display_name: null, email: 'lin@example.test' }).hello).toBe('下午好，lin')
  })

  it('me 为 undefined 时只有时段词', () => {
    expect(greeting(new Date(2026, 9, 16, 14, 32), undefined).hello).toBe('下午好')
  })

  it('各时段问候', () => {
    expect(greeting(new Date(2026, 9, 16, 9, 0), me).hello).toBe('早上好，林舟')
    expect(greeting(new Date(2026, 9, 16, 12, 0), me).hello).toBe('中午好，林舟')
    expect(greeting(new Date(2026, 9, 16, 20, 0), me).hello).toBe('晚上好，林舟')
  })
})

describe('todoBoard', () => {
  it('undefined → 三格 0、没有待处理的事', () => {
    const board = todoBoard(undefined)
    expect(board.cards).toHaveLength(3)
    expect(board.cards.every((c) => c.count === 0)).toBe(true)
    expect(board.summary).toBe('没有待处理的事')
  })

  it('工单 + 提现 + 超时订单', () => {
    const items: TaskItem[] = [
      { kind: 'tickets_open', count: 7, high_priority: 2, oldest_wait_seconds: 10800 },
      { kind: 'withdrawals_pending', count: 3, amount: 128000 },
      { kind: 'orders_pending_stale', count: 2, threshold_seconds: 1800 },
    ]
    const board = todoBoard(items)
    expect(board.summary).toBe('3 项 · 1 项紧急')
    expect(board.cards[0]).toMatchObject({
      meta: '高优先级 2 个 · 最久等了 3 小时',
      hot: true,
    })
    expect(board.cards[1]).toMatchObject({ meta: '合计 ¥1,280.00' })
    expect(board.cards[2]).toMatchObject({
      meta: '超过 30 分钟没到账，可能是回调丢失',
      target: { module: 'billing', tab: 'orders', query: { s: 'pending' } },
    })
  })

  it('integrity：全为 0 不出第四格；有违例出红卡、紧急数 +1、列中文名', () => {
    const base: TaskItem[] = [
      { kind: 'tickets_open', count: 1, high_priority: 0, oldest_wait_seconds: 60 },
      { kind: 'withdrawals_pending', count: 0, amount: 0 },
      { kind: 'orders_pending_stale', count: 0, threshold_seconds: 1800 },
    ]
    const at = '2026-10-20T08:00:00Z'
    const clean: TaskItem = {
      kind: 'integrity',
      checks: [
        { code: 'ledger_drift', count: 0, checked_at: at },
        { code: 'order_paid_unfulfilled', count: 0, checked_at: at },
      ],
    }
    expect(todoBoard([...base, clean]).cards).toHaveLength(3)
    expect(todoBoard(base).cards).toHaveLength(3)

    const failing: TaskItem = {
      kind: 'integrity',
      checks: [
        { code: 'order_paid_unfulfilled', count: 1, checked_at: at },
        { code: 'ledger_drift', count: 2, checked_at: at },
        { code: 'giftcard_redemption_mismatch', count: 0, checked_at: at },
      ],
    }
    const board = todoBoard([...base, failing])
    expect(board.cards).toHaveLength(4)
    expect(board.summary).toBe('2 项 · 2 项紧急')
    expect(board.cards[3]).toMatchObject({
      key: 'integrity',
      title: '数据巡检',
      count: 3,
      meta: '账本漂移 2 · 已付订单没落地 1',
      hot: true,
      target: { module: 'billing', tab: null },
    })
  })

  it('integrity：超过两项写「等 N 项」，去处跟第一项走，不认识的代号原样显示、排最后', () => {
    const at = '2026-10-20T08:00:00Z'
    const board = todoBoard([
      {
        kind: 'integrity',
        checks: [
          { code: 'future_check', count: 4, checked_at: at },
          { code: 'subscription_expire_overdue', count: 1, checked_at: at },
          { code: 'commission_unpaid_order', count: 2, checked_at: at },
        ],
      },
    ])
    const card = board.cards.find((c) => c.key === 'integrity')
    expect(card).toMatchObject({
      count: 7,
      meta: '佣金挂在没付的订单上 2 · 订阅到期没停 1 等 3 项',
      target: { module: 'marketing', tab: 'commission' },
    })
    expect(integrityLabel('future_check')).toBe('future_check')
    expect(integrityLabel('payment_unsettled')).toBe('收款了订单却没付')
  })

  it('nodes_offline 与 notifications_backlog 不出卡', () => {
    const items: TaskItem[] = [
      { kind: 'tickets_open', count: 0, high_priority: 0, oldest_wait_seconds: null },
      { kind: 'withdrawals_pending', count: 0, amount: 0 },
      { kind: 'orders_pending_stale', count: 0, threshold_seconds: 1800 },
      { kind: 'nodes_offline', count: 5, sample: [{ id: 'n1', name: '节点' }], longest_offline_seconds: 100 },
      { kind: 'notifications_backlog', queued: 10, failed_total: 0, backlog_state: 'backlogged' },
    ]
    expect(todoBoard(items).cards.map((c) => c.key)).toEqual(['tickets_open', 'withdrawals_pending', 'orders_pending_stale'])
  })
})

describe('kpiTiles', () => {
  it('四格顺序、收入拆分与 compare', () => {
    const tiles = kpiTiles(OVERVIEW)
    expect(tiles.map((t) => t.key)).toEqual(['revenue', 'orders', 'signups', 'subs'])
    expect(tiles[0]).toMatchObject({ value: '¥0', cents: '.01', compare: { label: '昨天全天', value: '¥0.01' } })
    expect(tiles[1]).toMatchObject({ compare: { label: '待支付', value: '1' } })
    expect(tiles[2]).toMatchObject({ compare: { label: '近 7 天', value: '10' } })
    expect(tiles[3]).toMatchObject({ compare: { label: '7 天内到期', value: '3' } })
  })
})

describe('asOfText', () => {
  it('补零到两位', () => {
    expect(asOfText(new Date(2026, 9, 16, 9, 5))).toBe('截至 09:05')
  })
})

describe('schema 守住契约形状', () => {
  it('概览：Go 必回的字段齐全才过，缺 yesterday / new_7_days / nodes 判为不符', () => {
    const full = {
      users: { total: 1, active: 1, today: 0, last_7_days: 0 },
      subscriptions: { active: 1, expiring_7_days: 0, expired: 0, new_7_days: 0 },
      nodes: { total: 2, online: 1 },
      revenue: REVENUE_ROW,
      orders: { paid_today: 0, pending: 0, failed_today: 0 },
      ledger_drift_accounts: 0,
    }
    expect(overviewSchema.safeParse(full).success).toBe(true)
    const without = <T extends object>(o: T, key: keyof T) => Object.fromEntries(Object.entries(o).filter(([k]) => k !== key))
    expect(overviewSchema.safeParse({ ...full, revenue: without(REVENUE_ROW, 'yesterday') }).success).toBe(false)
    expect(overviewSchema.safeParse({ ...full, subscriptions: { active: 1, expiring_7_days: 0, expired: 0 } }).success).toBe(false)
    expect(overviewSchema.safeParse(without(full, 'nodes')).success).toBe(false)
  })

  it('数字形态的字节判为不符约定', () => {
    const numeric = {
      ...TRAFFIC_24H,
      totals: { ...TRAFFIC_24H.totals, reported_bytes: 1010 },
    }
    expect(nodeTrafficSchema.safeParse(numeric).success).toBe(false)
  })
})
