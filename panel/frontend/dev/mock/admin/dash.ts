/**
 * [INPUT]: 依赖 ../types 的 MockModule / MockContext
 * [OUTPUT]: 对外提供 dash 模块的假接口 MockModule
 * [POS]: dev/mock/admin 的「仪表盘（后台-01）」假接口，归后台前端一；八个只读接口，形状、权限、参数校验与错误码照 api-contract.md 后台-01（含待补·后端字段）与 DASH-01 冻结契约。数据按日期确定性生成，概览的今日 / 昨日与收入趋势的最后两天是同一组数
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { MockContext, MockModule } from '../types.ts'

// ---------------------------------------------------------------------------
// 确定性的伪数据：同一天、同一币种得到同一组数，刷新不跳
// ---------------------------------------------------------------------------
const DAY_MS = 86_400_000

function wave(i: number, seed: number): number {
  return Math.sin(i * 0.55 + seed) * 0.5 + Math.sin(i * 0.17 + seed * 2) * 0.35
}

/** 距今 daysAgo 天那一天的收入拆分（分）；天序号按绝对日期取，跨请求一致 */
function dailyRevenue(currency: string, daysAgo: number) {
  const dayIndex = Math.floor(Date.now() / DAY_MS) - daysAgo
  const cny = currency === 'CNY'
  const base = cny ? 620_000 : 82_000
  const amp = cny ? 380_000 : 52_000
  const credit = Math.max(0, Math.round(base + amp * wave(dayIndex, cny ? 1 : 3)))
  const debit = dayIndex % 9 === 0 ? Math.round(credit * 0.04) : 0
  const adjustment = dayIndex % 13 === 0 ? (cny ? -5_000 : 1_200) : 0
  return { credit, debit, adjustment, net: credit - debit + adjustment }
}

function localDate(daysAgo: number): string {
  const d = new Date(Date.now() - daysAgo * DAY_MS)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

/** DASH-01 的时间格式：UTC、六位小数、大写 Z */
function micros(at: Date): string {
  return at.toISOString().replace(/\.(\d{3})Z$/, '.$1000Z')
}

function sumNet(currency: string, from: number, to: number, pick: 'net' | 'actual' | 'adjustment' = 'net'): number {
  let sum = 0
  for (let i = from; i < to; i++) {
    const r = dailyRevenue(currency, i)
    sum += pick === 'net' ? r.net : pick === 'actual' ? r.credit - r.debit : r.adjustment
  }
  return sum
}

// ---------------------------------------------------------------------------
// 流量排行：字节是十进制字符串（DASH-01）；用户只给脱敏邮箱
// ---------------------------------------------------------------------------
const GiB = 1024n ** 3n
const NODE_ROWS = [
  { node_id: '5b0c7f1e-2d4a-4c1b-9a3e-1f2e3d4c5b6a', name: 'hk-hkg-01', display_name: 'HK-HKG-01', gb: 1840n },
  { node_id: '6c1d8a2f-3e5b-4d2c-8b4f-2a3b4c5d6e7f', name: 'jp-tyo-03', display_name: 'JP-TYO-03', gb: 1522n },
  { node_id: '7d2e9b3a-4f6c-4e3d-9c5a-3b4c5d6e7f8a', name: 'sg-sin-02', display_name: null, gb: 1210n },
  { node_id: '8e3fac4b-5a7d-4f4e-8d6b-4c5d6e7f8a9b', name: 'us-lax-01', display_name: 'US-LAX-01', gb: 864n },
  { node_id: '9f4abd5c-6b8e-4a5f-9e7c-5d6e7f8a9b0c', name: 'tw-tpe-01', display_name: 'TW-TPE-01', gb: 540n },
  { node_id: 'a05bce6d-7c9f-4b6a-8f8d-6e7f8a9b0c1d', name: 'de-fra-01', display_name: 'DE-FRA-01', gb: 402n },
] as const
const USER_ROWS = [
  { user_id: '1a2b3c41-0000-4000-8000-000000000001', email_masked: 'z***@qq.com', gb: 312n, subs: 1 },
  { user_id: '1a2b3c42-0000-4000-8000-000000000002', email_masked: 'k***@proton.me', gb: 268n, subs: 2 },
  { user_id: '1a2b3c43-0000-4000-8000-000000000003', email_masked: 'w***@163.com', gb: 221n, subs: 1 },
  { user_id: '1a2b3c44-0000-4000-8000-000000000004', email_masked: 'm***@gmail.com', gb: 189n, subs: 1 },
  { user_id: '1a2b3c45-0000-4000-8000-000000000005', email_masked: 'y***@outlook.com', gb: 152n, subs: 3 },
  { user_id: '1a2b3c46-0000-4000-8000-000000000006', email_masked: '***', gb: 97n, subs: 1 },
] as const

const RANGE_SCALE: Record<string, bigint> = { '24h': 1n, '7d': 6n, '30d': 24n }
const RANGE_MS: Record<string, number> = { '24h': DAY_MS, '7d': 7 * DAY_MS, '30d': 30 * DAY_MS }

/** 解析 range / limit / snapshot_at；非法时已回 422 并返回 null（DASH-01：无 fields） */
function trafficWindow(ctx: MockContext) {
  const range = ctx.query.get('range') ?? '7d'
  const limitRaw = ctx.query.get('limit') ?? '10'
  const snapRaw = ctx.query.get('snapshot_at')
  if (!(range in RANGE_SCALE) || !['5', '10', '20'].includes(limitRaw)) {
    ctx.fail(422, 'validation_failed', '参数不合法：range 仅支持 24h / 7d / 30d，limit 仅支持 5 / 10 / 20')
    return null
  }
  let snapshot = new Date()
  if (snapRaw !== null) {
    const at = new Date(snapRaw)
    const age = Date.now() - at.getTime()
    if (Number.isNaN(at.getTime()) || !/Z$/.test(snapRaw) || age < 0 || age > 31 * DAY_MS) {
      ctx.fail(422, 'validation_failed', 'snapshot_at 须为 31 天内的 RFC3339 UTC 时间')
      return null
    }
    snapshot = at
  }
  const from = new Date(snapshot.getTime() - RANGE_MS[range]!)
  return { range, limit: Number(limitRaw), scale: RANGE_SCALE[range]!, snapshot, from }
}

function bytes(gb: bigint, scale: bigint, share: bigint) {
  const total = gb * GiB * scale + 123_456n
  const upload = (total * share) / 100n
  return { upload_bytes: String(upload), download_bytes: String(total - upload), total_bytes: String(total) }
}

// ---------------------------------------------------------------------------
// 需要处理：条目按各自读权限过滤。withdrawals_pending 挂 marketing.commission.read（修订 R51）
// ---------------------------------------------------------------------------
const TASK_PERMS: Record<string, string> = {
  tickets_open: 'ops.ticket.read',
  withdrawals_pending: 'marketing.commission.read',
  nodes_offline: 'node.read',
  orders_pending_stale: 'billing.order.read',
  notifications_backlog: 'ops.notification.read',
  ledger_drift: 'billing.ledger.read',
}

const BACKLOG = {
  backlog_state: 'backlogged' as const,
  counts: { ready: 186, ready_retry: 12, scheduled: 28, scheduled_retry: 9, sending_unobservable: 0, failed_total: 9, suppressed_total: 4, bounced_total: 2 },
  max_ready_lag_seconds: 1_260,
}

function isInt(raw: string | null): boolean {
  return raw !== null && /^-?\d+$/.test(raw)
}

export const dash: MockModule = {
  routes: {
    // 待补·后端：ops.dashboard.read，处理器内再按条目权限过滤
    'GET /v1/dashboard/tasks': (ctx) => {
      if (!ctx.requirePermission('ops.dashboard.read')) return
      const items = [
        { kind: 'tickets_open', count: 7, high_priority: 2, oldest_wait_seconds: 3 * 3600 + 540 },
        { kind: 'withdrawals_pending', count: 3, amounts: [{ currency: 'CNY', amount: 128_000 }] },
        { kind: 'nodes_offline', count: 3, sample: [{ id: NODE_ROWS[1].node_id, name: 'JP-TYO-03' }, { id: NODE_ROWS[3].node_id, name: 'US-LAX-01' }, { id: NODE_ROWS[5].node_id, name: 'DE-FRA-01' }], longest_offline_seconds: 26 * 60 },
        { kind: 'orders_pending_stale', count: 9, threshold_seconds: 1800 },
        { kind: 'notifications_backlog', queued: BACKLOG.counts.ready + BACKLOG.counts.scheduled, failed_total: BACKLOG.counts.failed_total, backlog_state: BACKLOG.backlog_state },
        { kind: 'ledger_drift', count: 0 },
      ].filter((item) => ctx.user.permissions.includes(TASK_PERMS[item.kind]!))
      ctx.send(200, { as_of: new Date().toISOString(), items })
    },

    // DASH-01 冻结：ops.notification.read
    'GET /v1/dashboard/backlog/notifications': (ctx) => {
      if (!ctx.requirePermission('ops.notification.read')) return
      const now = Date.now()
      ctx.send(200, {
        as_of: micros(new Date(now)),
        backlog_state: BACKLOG.backlog_state,
        processor_state: 'unobservable',
        scanner_interval_seconds: 300,
        counts: BACKLOG.counts,
        oldest_ready_at: micros(new Date(now - BACKLOG.max_ready_lag_seconds * 1000)),
        max_ready_lag_seconds: BACKLOG.max_ready_lag_seconds,
        last_sent_at: micros(new Date(now - 90_000)),
        assessment: { threshold_seconds: 600, reason: 'lag_exceeded' },
      })
    },

    // 现有 + 待补·后端（yesterday / actual_yesterday / new_7_days / nodes）：billing.ledger.read
    'GET /v1/overview': (ctx) => {
      if (!ctx.requirePermission('billing.ledger.read')) return
      const revenue = ['CNY', 'USD'].map((currency) => {
        const today = dailyRevenue(currency, 0)
        const yesterday = dailyRevenue(currency, 1)
        return {
          currency,
          today: today.net,
          last_7_days: sumNet(currency, 0, 7),
          last_30_days: sumNet(currency, 0, 30),
          total: sumNet(currency, 0, 400),
          actual_today: today.credit - today.debit,
          actual_7_days: sumNet(currency, 0, 7, 'actual'),
          actual_30_days: sumNet(currency, 0, 30, 'actual'),
          actual_total: sumNet(currency, 0, 400, 'actual'),
          adjustment_today: today.adjustment,
          adjustment_7_days: sumNet(currency, 0, 7, 'adjustment'),
          adjustment_30_days: sumNet(currency, 0, 30, 'adjustment'),
          adjustment_total: sumNet(currency, 0, 400, 'adjustment'),
          yesterday: yesterday.net,
          actual_yesterday: yesterday.credit - yesterday.debit,
        }
      })
      ctx.send(200, {
        users: { total: 18_204, active: 13_870, today: 96, last_7_days: 612 },
        subscriptions: { active: 12_408, trialing: 214, expiring_7_days: 731, expired: 5_022, new_7_days: 386 },
        revenue,
        orders: { paid_today: 142, pending: 17, failed_today: 3 },
        ledger_drift_accounts: 0,
        nodes: { total: 44, online: 41 },
      })
    },

    // 现有 + 待补 previous_total：billing.ledger.read
    'GET /v1/revenue/timeseries': (ctx) => {
      if (!ctx.requirePermission('billing.ledger.read')) return
      const currency = (ctx.query.get('currency') ?? '').toUpperCase()
      if (currency !== 'CNY' && currency !== 'USD') return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { currency: '仅支持 CNY 或 USD' })
      const daysRaw = ctx.query.get('days')
      const days = isInt(daysRaw) ? Number(daysRaw) : 30
      if (![7, 30, 90].includes(days)) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { days: '仅支持 7、30 或 90 天' })
      const points = []
      for (let i = days - 1; i >= 0; i--) {
        const r = dailyRevenue(currency, i)
        points.push({ date: localDate(i), actual_credit: r.credit, actual_debit: r.debit, adjustment: r.adjustment, displayed_net: r.net })
      }
      ctx.send(200, { currency, days, points, previous_total: sumNet(currency, days, days * 2) })
    },

    // DASH-01 冻结：metering.read + node.read
    'GET /v1/dashboard/traffic/nodes': (ctx) => {
      if (!ctx.requirePermission('metering.read') || !ctx.requirePermission('node.read')) return
      const w = trafficWindow(ctx)
      if (!w) return
      const all = NODE_ROWS.map((n, i) => ({
        node_id: n.node_id,
        name: n.name,
        display_name: n.display_name,
        ...bytes(n.gb, w.scale, BigInt(8 + i)),
        contributing_entry_count: 4_800 - i * 500,
        report_count: 1_440 - i * 60,
        last_report_at: micros(new Date(w.snapshot.getTime() - (i + 1) * 45_000)),
      }))
      const items = all.slice(0, w.limit)
      const reported = all.reduce((s, n) => s + BigInt(n.total_bytes), 0n)
      const returned = items.reduce((s, n) => s + BigInt(n.total_bytes), 0n)
      const unattributed = 37n * GiB * w.scale
      ctx.send(200, {
        range: w.range,
        snapshot_at: micros(w.snapshot),
        from: micros(w.from),
        to: micros(w.snapshot),
        basis: 'strict_raw_report_entries',
        items,
        totals: { reported_bytes: String(reported), attributed_bytes: String(reported - unattributed), unattributed_bytes: String(unattributed) },
        ranking: { returned_bytes: String(returned), other_node_bytes: String(reported - returned) },
        quality: { duplicate_report_count: 2, invalid_report_count: 0, invalid_entry_count: 1 },
      })
    },

    // DASH-01 冻结：metering.read + iam.user.read；带 identity 参数（任何值）直接 422
    'GET /v1/dashboard/traffic/users': (ctx) => {
      if (!ctx.requirePermission('metering.read') || !ctx.requirePermission('iam.user.read')) return
      if (ctx.query.has('identity')) return ctx.fail(422, 'validation_failed', '用户流量排行只提供脱敏邮箱')
      const w = trafficWindow(ctx)
      if (!w) return
      const all = USER_ROWS.map((u, i) => ({
        user_id: u.user_id,
        email_masked: u.email_masked,
        ...bytes(u.gb, w.scale, BigInt(10 + i)),
        subscription_count: u.subs,
        contributing_entry_count: 900 - i * 90,
        last_report_at: micros(new Date(w.snapshot.getTime() - (i + 2) * 60_000)),
      }))
      const items = all.slice(0, w.limit)
      const attributed = all.reduce((s, u) => s + BigInt(u.total_bytes), 0n) + 2_000n * GiB * w.scale
      const unattributed = 37n * GiB * w.scale
      const returned = items.reduce((s, u) => s + BigInt(u.total_bytes), 0n)
      ctx.send(200, {
        range: w.range,
        snapshot_at: micros(w.snapshot),
        from: micros(w.from),
        to: micros(w.snapshot),
        basis: 'strict_raw_report_entries',
        items,
        totals: { reported_bytes: String(attributed + unattributed), attributed_bytes: String(attributed), unattributed_bytes: String(unattributed) },
        ranking: { returned_bytes: String(returned), other_user_bytes: String(attributed - returned) },
        quality: { duplicate_report_count: 2, invalid_report_count: 0, invalid_entry_count: 1 },
      })
    },

    // 现有 backup / database + 待补·后端 state / components：security.audit.read
    'GET /v1/system/status': (ctx) => {
      if (!ctx.requirePermission('security.audit.read')) return
      const now = Date.now()
      const files = Array.from({ length: 10 }, (_, i) => {
        const at = new Date(now - (i * 24 + 5) * 3_600_000)
        return {
          name: `aegis-${at.toISOString().slice(0, 19).replace(/[-:]/g, '')}Z.dump.age`,
          size: 412_000_000 - i * 1_800_000,
          created_at: at.toISOString(),
          has_checksum: i !== 7,
        }
      })
      ctx.send(200, {
        backup: {
          dir: '/var/backups/aegispanel',
          readable: true,
          count: files.length,
          total_bytes: files.reduce((s, f) => s + f.size, 0),
          latest: files[0],
          latest_age_hours: 5,
          stale: false,
          missing_checksum: 1,
          recent: files.slice(0, 5),
          identity_configured: true,
          offsite_configured: false,
        },
        database: { size_bytes: 3_418_000_000, connections: 23, max_connections: 200 },
        state: 'degraded',
        components: [
          { key: 'postgres', state: 'ok', latency_ms: 3.2, metrics: { size_bytes: 3_418_000_000, connections: 23, max_connections: 200 } },
          { key: 'valkey', state: 'ok', latency_ms: 0.4, metrics: {} },
          { key: 'node_fabric', state: 'warn', metrics: { total: 44, online: 41, config_lagging: 2 }, message: '3 个节点心跳超过 90 秒' },
          { key: 'payment_callbacks', state: 'ok', metrics: { pending: 0 } },
          { key: 'mail', state: 'warn', metrics: { queued: 214, retrying: 21, failed_total: 9 }, message: 'SMTP 连接超时，正在重试' },
          { key: 'telegram', state: 'ok', metrics: { queued: 48, retrying: 0, failed_total: 0 } },
          { key: 'sse', state: 'ok', metrics: { connections: 38 } },
          { key: 'backup', state: 'warn', metrics: { latest_age_hours: 5, stale: false, identity_configured: true, offsite_configured: false } },
        ],
      })
    },

    // 现有 + 待补 active_users：security.audit.read；days 1–90，非法按 14
    'GET /v1/stats/timeseries': (ctx) => {
      if (!ctx.requirePermission('security.audit.read')) return
      const raw = ctx.query.get('days')
      const n = isInt(raw) ? Number(raw) : 14
      const days = n >= 1 && n <= 90 ? n : 14
      const points = []
      for (let i = days - 1; i >= 0; i--) {
        const idx = Math.floor(Date.now() / DAY_MS) - i
        const registered = Math.max(0, Math.round(70 + 50 * wave(idx, 5)))
        points.push({
          day: localDate(i).slice(5),
          registered,
          logins: Math.round(2_400 + 900 * wave(idx, 4)),
          orders: Math.round(140 + 60 * wave(idx, 6)),
          unique_ips: Math.round(1_900 + 700 * wave(idx, 7)),
          active_users: Math.round(5_200 + 2_600 * wave(idx, 2)),
        })
      }
      ctx.send(200, { points })
    },
  },
}
