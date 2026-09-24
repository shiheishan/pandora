/**
 * [INPUT]: 依赖 vitest，依赖 ./model 的全部纯函数，依赖 ./api 的 zod schema
 * [OUTPUT]: 仪表盘数据映射与 schema 的单元测试
 * [POS]: admin/screens/dash 的纯逻辑测试：字节 BigInt 安全、百分比与时长文案、按权限取舍卡片与链接、待补·后端字段缺失时的退化、系统状态行与备份摘要、流量排行与未归属告警；schema 守住契约形状（待补字段可缺、字节必须是字符串、未知 kind 判为不符）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it } from 'vitest'
import { nodeTrafficSchema, overviewSchema, systemStatusSchema, tasksSchema, type Backlog, type NodeTraffic, type SystemStatus, type TaskItem, type UserTraffic } from './api'
import {
  activitySummary,
  backupSummary,
  dashboardAccess,
  formatBytes,
  formatCount,
  formatDuration,
  formatLatency,
  formatPercent,
  kpiRevenueDelta,
  percentChange,
  revenueSummary,
  systemRows,
  taskCards,
  trafficNotes,
  trafficRows,
} from './model'

const ALL = new Set([
  'ops.dashboard.read',
  'ops.ticket.read',
  'marketing.commission.read',
  'node.read',
  'billing.order.read',
  'billing.ledger.read',
  'ops.notification.read',
  'security.audit.read',
  'metering.read',
  'iam.user.read',
])
const VIEWER = new Set(['ops.ticket.read', 'iam.user.read', 'catalog.read', 'billing.order.read', 'node.read', 'ops.notification.read'])

describe('格式化', () => {
  it('formatBytes 三位有效数字、1024 进制', () => {
    expect(formatBytes('0')).toBe('0 B')
    expect(formatBytes('1023')).toBe('1023 B')
    expect(formatBytes('1024')).toBe('1.00 KB')
    expect(formatBytes(String(540n * 1024n ** 3n))).toBe('540 GB')
    expect(formatBytes(String(1840n * 1024n ** 3n))).toBe('1.80 TB')
    expect(formatBytes(String((186n * 1024n ** 4n) / 10n))).toBe('18.6 TB')
  })

  it('formatBytes 对超过 2^53 的十进制字符串不丢精度（DASH-01）', () => {
    // 2^63-1：先转 number 会变成 9223372036854775808，这里全程 BigInt
    expect(formatBytes('9223372036854775807')).toBe('8.00 EB')
    const justUnder = String(1024n ** 5n * 10n - 1n) // 10 PB − 1 字节：四舍五入到两位小数是 10.00 PB
    expect(formatBytes(justUnder)).toBe('10.00 PB')
  })

  it('formatCount / formatPercent / percentChange', () => {
    expect(formatCount(12408)).toBe('12,408')
    expect(formatCount(-1234567)).toBe('-1,234,567')
    expect(formatPercent(0.124)).toBe('+12.4%')
    expect(formatPercent(-0.031)).toBe('−3.1%')
    expect(formatPercent(0.0001)).toBe('0.0%')
    expect(percentChange(110, 100)).toBeCloseTo(0.1)
    expect(percentChange(50, undefined)).toBeNull()
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

describe('权限', () => {
  it('只读账号只剩通知积压一块', () => {
    expect(dashboardAccess(VIEWER)).toEqual({ tasks: false, backlog: true, overview: false, nodeTraffic: false, userTraffic: false, system: false, activity: false })
  })

  it('流量两张卡都要 metering.read', () => {
    const can = dashboardAccess(new Set(['node.read', 'iam.user.read']))
    expect(can.nodeTraffic).toBe(false)
    expect(can.userTraffic).toBe(false)
  })
})

const TASKS: TaskItem[] = [
  { kind: 'tickets_open', count: 7, high_priority: 2, oldest_wait_seconds: 3 * 3600 },
  { kind: 'withdrawals_pending', count: 3, amounts: [{ currency: 'CNY', amount: 128000 }, { currency: 'USD', amount: 0 }] },
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
    const cards = taskCards(TASKS, undefined, ALL)
    expect(cards.map((c) => c.key)).toEqual(['tickets_open', 'withdrawals_pending', 'nodes_offline', 'orders_pending_stale', 'notifications_backlog'])
    const byKey = Object.fromEntries(cards.map((c) => [c.key, c]))
    expect(byKey.tickets_open!.sub).toBe('最久已等 3 小时 · 2 个高优先级')
    expect(byKey.withdrawals_pending!.sub).toBe('合计 ¥1,280.00')
    expect(byKey.nodes_offline!.sub).toBe('JP-TYO-03 等 · 最长 26 分钟')
    expect(byKey.orders_pending_stale!).toMatchObject({ sub: '目前没有', tone: 'ok', pending: false })
    expect(byKey.notifications_backlog!).toMatchObject({ count: 214, tone: 'warn', pending: true })
    expect(cards.filter((c) => c.pending)).toHaveLength(4)
  })

  it('账本漂移大于 0 时排在最前、红色', () => {
    const cards = taskCards([...TASKS.slice(0, 5), { kind: 'ledger_drift', count: 2 }], undefined, ALL)
    expect(cards[0]).toMatchObject({ key: 'ledger_drift', tone: 'danger', count: 2, target: { module: 'billing', tab: null } })
  })

  it('通知积压优先用 backlog 明细：积压数 = ready + scheduled，不写「worker 正常」', () => {
    const backlogged = taskCards(TASKS, BACKLOG, ALL).find((c) => c.key === 'notifications_backlog')!
    expect(backlogged.count).toBe(214)
    expect(backlogged.sub).toBe('最久等待 21 分钟 · 重试中 21 · 9 封失败')
    const clear = taskCards(undefined, { ...BACKLOG, backlog_state: 'clear', counts: { ...BACKLOG.counts, failed_total: 0 } }, ALL)
    expect(clear).toHaveLength(1)
    expect(clear[0]).toMatchObject({ sub: '无超阈值到期积压', tone: 'ok', pending: false })
    expect(clear[0]!.hint).toContain('不可观测')
  })

  it('目标页不可读时卡片没有链接', () => {
    const cards = taskCards(TASKS, BACKLOG, VIEWER)
    const byKey = Object.fromEntries(cards.map((c) => [c.key, c]))
    expect(byKey.tickets_open!.target).toEqual({ module: 'tickets', tab: null })
    expect(byKey.withdrawals_pending!.target).toBeNull()
    expect(byKey.notifications_backlog!.target).toBeNull()
  })
})

describe('经营 KPI', () => {
  it('较昨日：yesterday 待补缺失时不瞎算', () => {
    expect(kpiRevenueDelta(1000, undefined)).toEqual({ text: '较昨日 —', tone: 'neutral' })
    expect(kpiRevenueDelta(1000, 0)).toEqual({ text: '昨日无收入', tone: 'neutral' })
    expect(kpiRevenueDelta(0, 0).text).toBe('昨日今日均无收入')
    expect(kpiRevenueDelta(1124, 1000)).toEqual({ text: '较昨日 +12.4%', tone: 'ok' })
    expect(kpiRevenueDelta(969, 1000)).toEqual({ text: '较昨日 −3.1%', tone: 'danger' })
  })
})

describe('收入趋势', () => {
  const p = (date: string, net: number, adjustment = 0) => ({ date, actual_credit: net - adjustment, actual_debit: 0, adjustment, displayed_net: net })

  it('合计、日均、较上一区间、柱高与今日柱', () => {
    const s = revenueSummary([p('2026-09-22', 100), p('2026-09-23', -20, -20), p('2026-09-24', 200)], 'CNY', 200)
    expect(s.total).toBe(280)
    expect(s.average).toBe(93)
    expect(s.delta).toBeCloseTo(0.4)
    expect(s.bars.map((b) => b.height)).toEqual([50, 0, 100])
    expect(s.bars.map((b) => b.today)).toEqual([false, false, true])
    expect(s.bars[1]!.tip).toBe('09-23 · -¥0.20 · 实收 ¥0.00 · 调整 -¥0.20')
    expect(s.empty).toBe(false)
  })

  it('previous_total 缺失时较上一区间为 null；全零为空', () => {
    const s = revenueSummary([p('2026-09-24', 0)], 'USD', undefined)
    expect(s.delta).toBeNull()
    expect(s.empty).toBe(true)
  })
})

describe('注册与活跃', () => {
  const pt = (day: string, registered: number, active_users?: number) => ({ day, registered, logins: 3, orders: 1, unique_ips: 2, ...(active_users === undefined ? {} : { active_users }) })

  it('注册柱最高 70%、活跃柱最高 100%', () => {
    const s = activitySummary([pt('09-23', 50, 5000), pt('09-24', 100, 2500)])
    expect(s.registeredTotal).toBe(150)
    expect(s.activeAverage).toBe(3750)
    expect(s.bars.map((b) => [b.registered, b.active])).toEqual([
      [35, 100],
      [70, 50],
    ])
    expect(s.bars[0]!.tip).toBe('09-23 · 注册 50 · 活跃 5,000 · 登录 3 · 订单 1 · 独立 IP 2')
  })

  it('active_users 待补缺失时不画活跃柱', () => {
    const s = activitySummary([pt('09-24', 10)])
    expect(s.activeAverage).toBeNull()
    expect(s.bars[0]!.active).toBeNull()
  })
})

const STATUS_BASE: SystemStatus = {
  backup: {
    dir: '/var/backups/aegispanel',
    readable: true,
    count: 2,
    total_bytes: 2048,
    latest: { name: 'a.dump.age', size: 1024, created_at: '2026-09-24T03:15:00Z', has_checksum: true },
    latest_age_hours: 5,
    stale: false,
    missing_checksum: 0,
    recent: [],
    identity_configured: true,
    offsite_configured: true,
  },
  database: { size_bytes: 3 * 1024 ** 3, connections: 23, max_connections: 200 },
}

describe('系统状态', () => {
  it('备份摘要：读不到是未知、不是没有；多个问题一起列', () => {
    expect(backupSummary({ dir: '/x', readable: false, message: '读不到' })).toEqual({ state: 'unknown', meta: '读不到备份目录' })
    expect(backupSummary({ dir: '/x', readable: true, count: 0, stale: true, recent: [] })).toEqual({ state: 'warn', meta: '还没有备份' })
    expect(backupSummary(STATUS_BASE.backup)).toEqual({ state: 'ok', meta: '最近一份 5 小时前' })
    expect(backupSummary({ ...STATUS_BASE.backup, stale: true, identity_configured: false, offsite_configured: false, missing_checksum: 2 }).meta).toBe('过期 · 未配解密私钥 · 未配异地 · 2 份缺校验')
    expect(backupSummary({ ...STATUS_BASE.backup, latest_age_hours: 72 }).meta).toBe('最近一份 3 天前')
  })

  it('components 未上时只有数据库与备份两行', () => {
    const view = systemRows(STATUS_BASE, ALL)
    expect(view.rows.map((r) => [r.key, r.state, r.meta])).toEqual([
      ['postgres', 'ok', '3.00 GB · 连接 23/200'],
      ['backup', 'ok', '最近一份 5 小时前'],
    ])
    expect(view).toMatchObject({ label: '全部正常', tone: 'ok' })
    expect(systemRows({ ...STATUS_BASE, database: { error: '读取数据库状态失败' } }, ALL).rows[0]).toMatchObject({ state: 'down', meta: '读取数据库状态失败' })
  })

  it('components 按契约映射，备份永远是第 8 行，可点的行按权限给链接', () => {
    const status: SystemStatus = {
      ...STATUS_BASE,
      state: 'degraded',
      components: [
        { key: 'backup', state: 'warn', metrics: { latest_age_hours: 5, stale: false, identity_configured: true, offsite_configured: false }, message: '未配异地' },
        { key: 'postgres', state: 'ok', latency_ms: 3.2, metrics: { size_bytes: 1024 ** 3, connections: 23, max_connections: 200 } },
        { key: 'valkey', state: 'ok', latency_ms: 0.4, metrics: {} },
        { key: 'node_fabric', state: 'warn', metrics: { total: 44, online: 41, config_lagging: 2 } },
        { key: 'payment_callbacks', state: 'ok', metrics: { pending: 0 } },
        { key: 'mail', state: 'down', metrics: { queued: 214, retrying: 21, failed_total: 9 } },
        { key: 'telegram', state: 'ok', metrics: { queued: 48, retrying: 0, failed_total: 0 } },
        { key: 'sse', state: 'unknown', metrics: { connections: 38 } },
      ],
    }
    const view = systemRows(status, ALL)
    expect(view.rows.map((r) => r.meta)).toEqual([
      '3 ms · 1.00 GB · 连接 23/200',
      '0.4 ms',
      '在线 41/44 · 2 个节点配置未同步',
      '0 积压',
      '214 排队 · 21 重试 · 9 失败',
      '48 排队',
      '38 连接',
      '最近一份 5 小时前',
    ])
    expect(view.rows.at(-1)).toMatchObject({ key: 'backup', state: 'warn', hint: '未配异地', action: { kind: 'backup' } })
    expect(view).toMatchObject({ degraded: 3, label: '3 项降级', tone: 'warn' })
    // 通知与插件的通知渠道要 security.audit.read，节点要 node.read
    expect(view.rows.find((r) => r.key === 'mail')!.action).toEqual({ kind: 'go', target: { module: 'system', tab: 'notify' } })
    const limited = systemRows(status, new Set(['security.audit.read']))
    expect(limited.rows.find((r) => r.key === 'node_fabric')!.action).toBeUndefined()
  })
})

const NODE_TRAFFIC: NodeTraffic = {
  range: '24h',
  snapshot_at: '2026-09-24T08:00:00.000000Z',
  from: '2026-09-23T08:00:00.000000Z',
  to: '2026-09-24T08:00:00.000000Z',
  basis: 'strict_raw_report_entries',
  items: [
    { node_id: 'n1', name: 'hk-hkg-01', display_name: 'HK-HKG-01', upload_bytes: '100', download_bytes: '900', total_bytes: '1000', contributing_entry_count: 5, report_count: 3, last_report_at: '2026-09-24T07:59:00.000000Z' },
    { node_id: 'n2', name: 'sg-sin-02', display_name: null, upload_bytes: '0', download_bytes: '10', total_bytes: '10', contributing_entry_count: 1, report_count: 1, last_report_at: '2026-09-24T07:00:00.000000Z' },
  ],
  totals: { reported_bytes: '1010', attributed_bytes: '1000', unattributed_bytes: '10' },
  ranking: { returned_bytes: '1010', other_node_bytes: '0' },
  quality: { duplicate_report_count: 2, invalid_report_count: 0, invalid_entry_count: 1 },
}

describe('流量排行', () => {
  it('节点名 display_name ?? name，条宽以第一名为 100、最小 2', () => {
    const rows = trafficRows(NODE_TRAFFIC, new Date('2026-09-24T08:00:00Z'))
    expect(rows.map((r) => [r.name, r.width, r.value])).toEqual([
      ['HK-HKG-01', 100, '1000 B'],
      ['sg-sin-02', 2, '10 B'],
    ])
    expect(rows[0]!.tip).toBe('上行 100 B · 下行 900 B · 3 次上报 · 最近 1 分钟')
    expect(rows[0]!.userId).toBeUndefined()
  })

  it('用户行带 user_id、只显示脱敏邮箱', () => {
    const users: UserTraffic = {
      ...NODE_TRAFFIC,
      items: [{ user_id: 'u1', email_masked: 'a***@example.com', upload_bytes: '1', download_bytes: '1', total_bytes: '2', subscription_count: 2, contributing_entry_count: 1, last_report_at: '2026-09-24T07:59:00.000000Z' }],
      ranking: { returned_bytes: '2', other_user_bytes: '0' },
    }
    expect(trafficRows(users)[0]).toMatchObject({ name: 'a***@example.com', userId: 'u1' })
  })

  it('底部小字与全部未归属告警', () => {
    expect(trafficNotes(NODE_TRAFFIC)).toEqual({ notes: ['未归属 10 B', '重复上报 2', '无效条目 1'], allUnattributed: false })
    const orphan: UserTraffic = {
      ...NODE_TRAFFIC,
      items: [],
      totals: { reported_bytes: '500', attributed_bytes: '0', unattributed_bytes: '500' },
      ranking: { returned_bytes: '0', other_user_bytes: '0' },
      quality: { duplicate_report_count: 0, invalid_report_count: 0, invalid_entry_count: 0 },
    }
    expect(trafficNotes(orphan)).toEqual({ notes: ['未归属 500 B'], allUnattributed: true })
  })
})

describe('schema 守住契约形状', () => {
  it('概览缺待补·后端字段也能过', () => {
    const row = { currency: 'CNY', today: 1, last_7_days: 1, last_30_days: 1, total: 1, actual_today: 1, actual_7_days: 1, actual_30_days: 1, actual_total: 1, adjustment_today: 0, adjustment_7_days: 0, adjustment_30_days: 0, adjustment_total: 0 }
    const ok = overviewSchema.safeParse({
      users: { total: 1, active: 1, today: 0, last_7_days: 0 },
      subscriptions: { active: 1, trialing: 0, expiring_7_days: 0, expired: 0 },
      revenue: [row, { ...row, currency: 'USD' }],
      orders: { paid_today: 0, pending: 0, failed_today: 0 },
      ledger_drift_accounts: 0,
    })
    expect(ok.success).toBe(true)
  })

  it('未知 kind 与数字形态的字节都判为不符约定', () => {
    expect(tasksSchema.safeParse({ as_of: 'x', items: [{ kind: 'mystery', count: 1 }] }).success).toBe(false)
    const numeric = { ...NODE_TRAFFIC, totals: { ...NODE_TRAFFIC.totals, reported_bytes: 1010 } }
    expect(nodeTrafficSchema.safeParse(numeric).success).toBe(false)
  })

  it('系统状态：后端读不到备份目录时只有 dir / readable / message', () => {
    const res = systemStatusSchema.safeParse({ backup: { dir: '/x', readable: false, message: '读不到' }, database: { error: '读取数据库状态失败' } })
    expect(res.success).toBe(true)
  })
})
