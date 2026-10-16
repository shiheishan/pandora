import { describe, expect, it } from 'vitest'
import type { TaskItem } from '../../tasks'
import { nodeTrafficSchema, overviewSchema, systemStatusSchema, type Backlog, type NodeTraffic, type Overview, type SystemStatus } from './api'
import {
  backupSummary,
  formatDuration,
  formatLatency,
  formatPercent,
  kpiRevenueDelta,
  kpiTiles,
  percentChange,
  systemRows,
  taskBoard,
  taskCards,
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

  it('formatDuration / formatLatency', () => {
    expect(formatDuration(30)).toBe('不到 1 分钟')
    expect(formatDuration(26 * 60)).toBe('26 分钟')
    expect(formatDuration(3 * 3600 + 540)).toBe('3 小时')
    expect(formatDuration(50 * 3600)).toBe('2 天')
    expect(formatLatency(0.43)).toBe('0.4 ms')
    expect(formatLatency(3.2)).toBe('3 ms')
  })
})

const TASKS: TaskItem[] = [
  { kind: 'tickets_open', count: 7, high_priority: 2, oldest_wait_seconds: 3 * 3600 },
  { kind: 'withdrawals_pending', count: 3, amount: 128000 },
  { kind: 'nodes_offline', count: 3, sample: [{ id: 'n1', name: 'JP-TYO-03' }], longest_offline_seconds: 26 * 60 },
  { kind: 'orders_pending_stale', count: 0, threshold_seconds: 1800 },
  { kind: 'notifications_backlog', queued: 214, failed_total: 9, backlog_state: 'backlogged' },
  { kind: 'ledger_drift', count: 0 },
]

const BACKLOG: Backlog = {
  as_of: '2026-09-24T08:00:00.000000Z',
  backlog_state: 'backlogged',
  processor_state: 'unobservable',
  scanner_interval_seconds: 300,
  counts: { ready: 186, ready_retry: 12, scheduled: 28, scheduled_retry: 9, sending_unobservable: 0, failed_total: 9, suppressed_total: 4, bounced_total: 2 },
  oldest_ready_at: '2026-09-24T07:39:00.000000Z',
  max_ready_lag_seconds: 1260,
  last_sent_at: null,
  assessment: { threshold_seconds: 600, reason: 'lag_exceeded' },
}

describe('需要处理', () => {
  it('按设计稿映射副标题，账本漂移为 0 不出卡', () => {
    const cards = taskCards(TASKS, undefined)
    expect(cards.map((c) => c.key)).toEqual(['tickets_open', 'withdrawals_pending', 'nodes_offline', 'orders_pending_stale', 'notifications_backlog'])
    const byKey = Object.fromEntries(cards.map((c) => [c.key, c]))
    expect(byKey.tickets_open!.sub).toBe('最久已等 3 小时 · 2 个高优先级')
    expect(byKey.withdrawals_pending!.sub).toBe('合计 ¥1,280.00')
    expect(byKey.nodes_offline!.sub).toBe('JP-TYO-03 等 · 最长 26 分钟')
    expect(byKey.orders_pending_stale!).toMatchObject({ sub: '目前没有', tone: 'ok', pending: false, target: { module: 'billing', tab: 'orders', query: { s: 'pending' } } })
    expect(byKey.notifications_backlog!).toMatchObject({ count: 214, tone: 'warn', pending: true })
    expect(cards.filter((c) => c.pending)).toHaveLength(4)
  })

  it('各卡 unit 与 stats', () => {
    const cards = taskCards(TASKS, BACKLOG)
    const byKey = Object.fromEntries(cards.map((c) => [c.key, c]))
    expect(byKey.tickets_open).toMatchObject({
      unit: '个未回复',
      stats: [{ label: '高优先级', value: '2 个' }, { label: '最久等待', value: '3 小时' }],
    })
    expect(byKey.withdrawals_pending).toMatchObject({
      unit: '笔待审核',
      stats: [{ label: '合计金额', value: '¥1,280.00' }],
    })
    expect(byKey.nodes_offline).toMatchObject({
      unit: '个节点离线',
      stats: [{ label: '最长离线', value: '26 分钟' }, { label: '包括', value: 'JP-TYO-03' }],
    })
    expect(byKey.notifications_backlog!.stats).toEqual([
      { label: '最久等待', value: '21 分钟' },
      { label: '累计失败', value: '9 封' },
    ])
  })

  it('账本漂移大于 0 时排在最前、红色', () => {
    const cards = taskCards([...TASKS.slice(0, 5), { kind: 'ledger_drift', count: 2 }], undefined)
    expect(cards[0]).toMatchObject({ key: 'ledger_drift', tone: 'danger', count: 2, target: { module: 'billing', tab: null } })
  })

  it('通知积压优先用 backlog 明细：积压数 = ready + scheduled，不写「worker 正常」', () => {
    const backlogged = taskCards(TASKS, BACKLOG).find((c) => c.key === 'notifications_backlog')!
    expect(backlogged.count).toBe(214)
    expect(backlogged.sub).toBe('最久等待 21 分钟 · 重试中 21 · 9 封失败')
    const clear = taskCards(undefined, { ...BACKLOG, backlog_state: 'clear', counts: { ...BACKLOG.counts, failed_total: 0 } })
    expect(clear).toHaveLength(1)
    expect(clear[0]).toMatchObject({ sub: '无超阈值到期积压', tone: 'ok', pending: false })
    expect(clear[0]!.hint).toContain('不可观测')
  })

  it('每张卡都链到去处理的页面', () => {
    const cards = taskCards(TASKS, BACKLOG)
    const byKey = Object.fromEntries(cards.map((c) => [c.key, c]))
    expect(byKey.tickets_open!.target).toEqual({ module: 'tickets', tab: null })
    expect(byKey.withdrawals_pending!.target).toEqual({ module: 'marketing', tab: 'commission' })
    expect(byKey.notifications_backlog!.target).toEqual({ module: 'system', tab: 'notify' })
  })
})

describe('taskBoard', () => {
  it('hero 选 danger 的工单，summary 含紧急项', () => {
    const cards = taskCards(TASKS, BACKLOG)
    const board = taskBoard(cards)
    expect(board.hero?.key).toBe('tickets_open')
    expect(board.summary).toBe('4 项 · 1 项紧急')
    expect(board.pending).toBe(4)
    expect(board.urgent).toBe(1)
    expect(board.rest.filter((c) => c.pending).length).toBe(3)
    expect(board.rest[0]?.pending).toBe(true)
  })

  it('全部为 0 时 hero 为 null 且 summary 为 0 项', () => {
    const none: TaskItem[] = [
      { kind: 'tickets_open', count: 0, high_priority: 0, oldest_wait_seconds: null },
      { kind: 'withdrawals_pending', count: 0, amount: 0 },
      { kind: 'nodes_offline', count: 0, sample: [], longest_offline_seconds: null },
      { kind: 'orders_pending_stale', count: 0, threshold_seconds: 1800 },
      { kind: 'ledger_drift', count: 0 },
    ]
    const board = taskBoard(taskCards(none, undefined))
    expect(board.hero).toBeNull()
    expect(board.summary).toBe('0 项')
    expect(board.pending).toBe(0)
  })

  it('rest 里 pending 在前', () => {
    const cards = taskCards(TASKS, BACKLOG)
    const { rest } = taskBoard(cards)
    const firstPendingIdx = rest.findIndex((c) => c.pending)
    const lastNonPendingIdx = rest.map((c, i) => (c.pending ? -1 : i)).filter((i) => i >= 0).pop()!
    expect(firstPendingIdx).toBeLessThan(lastNonPendingIdx)
  })
})

describe('经营 KPI', () => {
  it('较昨日：昨日为 0 时不算百分比', () => {
    expect(kpiRevenueDelta(1000, 0)).toEqual({ text: '昨日无收入', tone: 'neutral' })
    expect(kpiRevenueDelta(0, 0).text).toBe('昨日今日均无收入')
    expect(kpiRevenueDelta(1124, 1000)).toEqual({ text: '较昨日 +12.4%', tone: 'ok' })
    expect(kpiRevenueDelta(969, 1000)).toEqual({ text: '较昨日 −3.1%', tone: 'danger' })
  })

  it('kpiTiles 正常', () => {
    const tiles = kpiTiles(OVERVIEW, TRAFFIC_24H)
    expect(tiles.map((t) => t.key)).toEqual(['revenue', 'subs', 'traffic'])
    expect(tiles[0]).toMatchObject({ label: '今日收入', muted: false })
    expect(tiles[0]!.value).toBe('¥0.01')
    expect(tiles[1]).toMatchObject({ label: '有效订阅', value: '50', muted: false })
    expect(tiles[2]).toMatchObject({ muted: false, sub: '9 / 10 节点在线' })
  })

  it('kpiTiles 收入为空 muted', () => {
    const o = { ...OVERVIEW, revenue: { ...REVENUE_ROW, today: 0, total: 0 } }
    const rev = kpiTiles(o, TRAFFIC_24H)[0]!
    expect(rev).toMatchObject({ value: '—', sub: '还没有订单', muted: true })
  })

  it('kpiTiles 订阅为空 muted', () => {
    const o = { ...OVERVIEW, subscriptions: { active: 0, expiring_7_days: 0, expired: 0, new_7_days: 0 } }
    const subs = kpiTiles(o, TRAFFIC_24H)[1]!
    expect(subs).toMatchObject({ value: '0', sub: '还没有人购买套餐', muted: true })
  })

  it('kpiTiles traffic 为 undefined 与 reported_bytes 为 0', () => {
    const noQuery = kpiTiles(OVERVIEW, undefined)[2]!
    expect(noQuery).toMatchObject({ value: '—', sub: '', muted: true })
    const zero: NodeTraffic = { ...TRAFFIC_24H, totals: { ...TRAFFIC_24H.totals, reported_bytes: '0' } }
    const noReport = kpiTiles(OVERVIEW, zero)[2]!
    expect(noReport).toMatchObject({ value: '—', sub: '还没有节点上报', muted: true })
  })
})

const STATUS_BASE: SystemStatus = {
  backup: {
    dir: '/var/backups/pandora',
    readable: true,
    count: 2,
    total_bytes: 2048,
    latest: { name: 'a.dump.age', size: 1024, created_at: '2026-09-24T03:15:00Z', has_checksum: true, has_seal: true },
    latest_age_hours: 5,
    stale: false,
    missing_checksum: 0,
    missing_seal: 0,
    recent: [],
    identity_configured: true,
    offsite_configured: true,
  },
  database: { size_bytes: 3 * 1024 ** 3, connections: 23, max_connections: 200 },
  state: 'ok',
  components: [
    { key: 'postgres', state: 'ok', latency_ms: 3.2, metrics: { size_bytes: 3 * 1024 ** 3, connections: 23, max_connections: 200 } },
    { key: 'backup', state: 'ok', metrics: { latest_age_hours: 5, stale: false, identity_configured: true, offsite_configured: true } },
  ],
}

describe('系统状态', () => {
  it('备份摘要：读不到是未知、不是没有；多个问题一起列', () => {
    expect(backupSummary({ dir: '/x', readable: false, message: '读不到' })).toEqual({ state: 'unknown', meta: '读不到备份目录' })
    expect(backupSummary({ dir: '/x', readable: true, count: 0, stale: true, recent: [] })).toEqual({ state: 'warn', meta: '还没有备份' })
    expect(backupSummary(STATUS_BASE.backup)).toEqual({ state: 'ok', meta: '最近一份 5 小时前' })
    expect(backupSummary({ ...STATUS_BASE.backup, stale: true, identity_configured: false, offsite_configured: false, missing_checksum: 2 }).meta).toBe('过期 · 未配解密私钥 · 未配异地 · 2 份缺校验')
    expect(backupSummary({ ...STATUS_BASE.backup, latest_age_hours: 72 }).meta).toBe('最近一份 3 天前')
  })

  it('备份摘要：缺封条的单独说出来；有文件却没有能恢复的一份，不说「还没有」', () => {
    expect(backupSummary({ ...STATUS_BASE.backup, missing_seal: 1 })).toEqual({ state: 'warn', meta: '1 份缺封条' })
    expect(backupSummary({ ...STATUS_BASE.backup, missing_checksum: 1, missing_seal: 2 }).meta).toBe('1 份缺校验 · 2 份缺封条')
    const none = { dir: '/x', readable: true, count: 3, stale: true, missing_seal: 3, recent: [] }
    expect(backupSummary(none)).toEqual({ state: 'warn', meta: '3 份都恢复不了（缺封条或校验）' })
  })

  it('行来自 components，备份行排最后、文案取 backup 段', () => {
    const view = systemRows(STATUS_BASE)
    expect(view.rows.map((r) => [r.key, r.state, r.meta])).toEqual([
      ['postgres', 'ok', '3 ms · 3.00 GB · 连接 23/200'],
      ['backup', 'ok', '最近一份 5 小时前'],
    ])
    expect(view).toMatchObject({ label: '全部正常', tone: 'ok', alert: null, footer: '共 2 项服务' })
    expect(view.attention).toHaveLength(0)
    expect(view.healthy).toHaveLength(2)
  })

  it('attention / healthy / alert：node_fabric、mail、backup 三项 warn', () => {
    const status: SystemStatus = {
      ...STATUS_BASE,
      state: 'degraded',
      components: [
        { key: 'backup', state: 'warn', metrics: { latest_age_hours: 5, stale: false, identity_configured: true, offsite_configured: false }, message: '未配异地' },
        { key: 'postgres', state: 'ok', latency_ms: 3.2, metrics: { size_bytes: 1024 ** 3, connections: 23, max_connections: 200 } },
        { key: 'redis', state: 'ok', latency_ms: 0.4, metrics: {} },
        { key: 'node_fabric', state: 'warn', metrics: { total: 44, online: 41, config_lagging: 2 } },
        { key: 'payment_callbacks', state: 'ok', metrics: { pending: 0 } },
        { key: 'mail', state: 'down', metrics: { queued: 214, retrying: 21, failed_total: 9 } },
        { key: 'telegram', state: 'ok', metrics: { queued: 48, retrying: 0, failed_total: 0 } },
        { key: 'sse', state: 'unknown', metrics: { connections: 38 } },
      ],
    }
    const view = systemRows(status)
    expect(view.attention.map((r) => r.key)).toEqual(['node_fabric', 'mail', 'sse', 'backup'])
    expect(view.healthy.map((r) => r.key)).toEqual(['postgres', 'redis', 'payment_callbacks', 'telegram'])
    expect(view.alert).toMatchObject({ title: '节点调度与邮件投递受影响' })
    expect(view.alert!.body.startsWith('节点调度：')).toBe(true)
    expect(view.alert!.body.endsWith('。')).toBe(true)
    expect(view.footer).toBe('共 8 项服务')
    expect(view.rows.at(-1)).toMatchObject({ key: 'backup', state: 'warn', hint: '未配异地', action: { kind: 'backup' } })
    expect(view).toMatchObject({ degraded: 3, label: '3 项降级', tone: 'warn' })
    expect(view.rows.find((r) => r.key === 'mail')!.action).toEqual({ kind: 'go', target: { module: 'system', tab: 'notify' } })
    expect(view.rows.find((r) => r.key === 'node_fabric')!.action).toEqual({ kind: 'go', target: { module: 'nodes', tab: 'nodes' } })
    expect(view.rows.find((r) => r.key === 'postgres')!.action).toBeUndefined()
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

  it('系统状态：后端读不到备份目录时只有 dir / readable / message', () => {
    const components = [{ key: 'postgres', state: 'down', metrics: {}, message: '数据库不可达' }, { key: 'backup', state: 'unknown', metrics: {}, message: '读不到' }]
    const res = systemStatusSchema.safeParse({ backup: { dir: '/x', readable: false, message: '读不到' }, database: { error: '读取数据库状态失败' }, state: 'degraded', components })
    expect(res.success).toBe(true)
    expect(systemStatusSchema.safeParse({ backup: { dir: '/x', readable: false }, database: { error: 'x' } }).success).toBe(false)
  })
})

describe('修订 R52：down / unknown 时 metrics 字段可缺', () => {
  it('schema 放行缺字段与空 metrics，界面缺失处显示 —', () => {
    const raw = {
      ...STATUS_BASE,
      state: 'degraded',
      components: [
        { key: 'postgres', state: 'down', metrics: {} },
        { key: 'node_fabric', state: 'unknown', metrics: { total: 44 } },
        { key: 'mail', state: 'down', metrics: { failed_total: 3 } },
        { key: 'sse', state: 'unknown', latency_ms: 2, metrics: {} },
        { key: 'backup', state: 'unknown', metrics: {} },
      ],
    }
    const parsed = systemStatusSchema.safeParse(raw)
    expect(parsed.success).toBe(true)
    const view = systemRows(parsed.data!)
    expect(view.rows.map((r) => [r.key, r.meta])).toEqual([
      ['postgres', '—'],
      ['node_fabric', '在线 —/44'],
      ['mail', '— 排队 · 3 失败'],
      ['sse', '2 ms'],
      ['backup', '最近一份 5 小时前'],
    ])
  })
  it('数据库状态读失败而 SELECT 1 成功：postgres ok，三项指标是 null（system_components.go 照抄 database 段）', () => {
    const raw = { ...STATUS_BASE, database: { error: '读取数据库状态失败' }, components: [{ key: 'postgres', state: 'ok', latency_ms: 1, metrics: { size_bytes: null, connections: null, max_connections: null } }] }
    const parsed = systemStatusSchema.safeParse(raw)
    expect(parsed.success).toBe(true)
    expect(systemRows(parsed.data!).rows.find((r) => r.key === 'postgres')?.meta).toBe('1 ms · 连接 —/—')
  })
})
