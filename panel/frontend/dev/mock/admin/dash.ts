import type { MockContext, MockModule } from '../types.ts'
import { stalePendingCount } from './billing-store.ts'
import { certAttentionSummary, nodeTrafficRows, offlineNodes, reportNodeCounts } from './nodes.ts'

// ---------------------------------------------------------------------------
// 确定性的伪数据：同一天得到同一组数，刷新不跳
// ---------------------------------------------------------------------------
const DAY_MS = 86_400_000

function wave(i: number, seed: number): number {
  return Math.sin(i * 0.55 + seed) * 0.5 + Math.sin(i * 0.17 + seed * 2) * 0.35
}

/** 距今 daysAgo 天那一天的收入拆分（分）；天序号按绝对日期取，跨请求一致 */
function dailyRevenue(daysAgo: number) {
  const dayIndex = Math.floor(Date.now() / DAY_MS) - daysAgo
  const credit = Math.max(0, Math.round(620_000 + 380_000 * wave(dayIndex, 1)))
  const debit = dayIndex % 9 === 0 ? Math.round(credit * 0.04) : 0
  const adjustment = dayIndex % 13 === 0 ? -5_000 : 0
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

function sumNet(from: number, to: number, pick: 'net' | 'actual' | 'adjustment' = 'net'): number {
  let sum = 0
  for (let i = from; i < to; i++) {
    const r = dailyRevenue(i)
    sum += pick === 'net' ? r.net : pick === 'actual' ? r.credit - r.debit : r.adjustment
  }
  return sum
}

// ---------------------------------------------------------------------------
// 流量排行：字节是十进制字符串（DASH-01）；用户只给脱敏邮箱
// ---------------------------------------------------------------------------
const GiB = 1024n ** 3n
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

function bytesFromBase(bytes24h: number, scale: bigint, share: bigint) {
  const total = BigInt(Math.round(bytes24h)) * scale + 123_456n
  const upload = (total * share) / 100n
  return { upload_bytes: String(upload), download_bytes: String(total - upload), total_bytes: String(total) }
}

function bytes(gb: bigint, scale: bigint, share: bigint) {
  const total = gb * GiB * scale + 123_456n
  const upload = (total * share) / 100n
  return { upload_bytes: String(upload), download_bytes: String(total - upload), total_bytes: String(total) }
}

const BACKLOG = {
  backlog_state: 'backlogged' as const,
  counts: { ready: 186, ready_retry: 12, scheduled: 28, scheduled_retry: 9, sending_unobservable: 0, failed_total: 9, suppressed_total: 4, bounced_total: 2 },
  max_ready_lag_seconds: 1_260,
}

// 后端 report:integrity 的检查代号（顺序同后端）
const INTEGRITY_CODES = [
  'ledger_drift',
  'ledger_hold_mismatch',
  'payment_unsettled',
  'order_paid_unfulfilled',
  'order_reservation_mismatch',
  'commission_unpaid_order',
  'giftcard_redemption_mismatch',
  'subscription_expire_overdue',
  'subscription_close_overdue',
]

function isInt(raw: string | null): boolean {
  return raw !== null && /^-?\d+$/.test(raw)
}

export const dash: MockModule = {
  routes: {
    'GET /v1/dashboard/tasks': (ctx) => {
      const checkedAt = new Date(Date.now() - 10 * 60 * 1000).toISOString()
      const items = [
        { kind: 'tickets_open', count: 7, high_priority: 2, oldest_wait_seconds: 3 * 3600 + 540 },
        { kind: 'withdrawals_pending', count: 3, amount: 128_000 },
        { kind: 'nodes_offline', ...offlineNodes(3) },
        { kind: 'node_certs', ...certAttentionSummary(3) },
        // 与订单假后端同一份数据：待支付筛选里能看到这几张
        { kind: 'orders_pending_stale', count: stalePendingCount(1800 * 1000), threshold_seconds: 1800 },
        { kind: 'notifications_backlog', queued: BACKLOG.counts.ready + BACKLOG.counts.scheduled, failed_total: BACKLOG.counts.failed_total, backlog_state: BACKLOG.backlog_state },
        // 数据巡检：九项都是 0（有违例时总览待办才出第四格「数据巡检」）
        { kind: 'integrity', checks: INTEGRITY_CODES.map((code) => ({ code, count: 0, checked_at: checkedAt })) },
      ]
      ctx.send(200, { as_of: new Date().toISOString(), items })
    },

    'GET /v1/dashboard/backlog/notifications': (ctx) => {
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

    // yesterday / actual_yesterday / new_7_days / nodes 与 Go 一样必回
    'GET /v1/overview': (ctx) => {
      const today = dailyRevenue(0)
      const yesterday = dailyRevenue(1)
      const revenue = {
        today: today.net,
        last_7_days: sumNet(0, 7),
        last_30_days: sumNet(0, 30),
        total: sumNet(0, 400),
        actual_today: today.credit - today.debit,
        actual_7_days: sumNet(0, 7, 'actual'),
        actual_30_days: sumNet(0, 30, 'actual'),
        actual_total: sumNet(0, 400, 'actual'),
        adjustment_today: today.adjustment,
        adjustment_7_days: sumNet(0, 7, 'adjustment'),
        adjustment_30_days: sumNet(0, 30, 'adjustment'),
        adjustment_total: sumNet(0, 400, 'adjustment'),
        yesterday: yesterday.net,
        actual_yesterday: yesterday.credit - yesterday.debit,
      }
      ctx.send(200, {
        users: { total: 18_204, active: 13_870, today: 96, last_7_days: 612 },
        subscriptions: { active: 12_408, expiring_7_days: 731, expired: 5_022, new_7_days: 386 },
        revenue,
        orders: { paid_today: 142, pending: 17, failed_today: 3 },
        ledger_drift_accounts: 0,
        nodes: reportNodeCounts(),
      })
    },

    // previous_total 必回
    'GET /v1/revenue/timeseries': (ctx) => {
      const daysRaw = ctx.query.get('days')
      const days = isInt(daysRaw) ? Number(daysRaw) : 30
      if (![7, 30, 90].includes(days)) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { days: '仅支持 7、30 或 90 天' })
      const points = []
      for (let i = days - 1; i >= 0; i--) {
        const r = dailyRevenue(i)
        points.push({ date: localDate(i), actual_credit: r.credit, actual_debit: r.debit, adjustment: r.adjustment, displayed_net: r.net })
      }
      ctx.send(200, { days, points, previous_total: sumNet(days, days * 2) })
    },

    'GET /v1/dashboard/traffic/nodes': (ctx) => {
      const w = trafficWindow(ctx)
      if (!w) return
      const all = nodeTrafficRows().map((n, i) => ({
        node_id: n.node_id,
        name: n.name,
        display_name: n.display_name,
        ...bytesFromBase(n.bytes_24h, w.scale, BigInt(8 + i)),
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
        quality: { invalid_report_count: 0, invalid_entry_count: 1 },
      })
    },

    // 带 identity 参数（任何值）直接 422
    'GET /v1/dashboard/traffic/users': (ctx) => {
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
        quality: { invalid_report_count: 0, invalid_entry_count: 1 },
      })
    },

    // state / components 必回，8 个组件总在
    'GET /v1/system/status': (ctx) => {
      const now = Date.now()
      const nodeCounts = reportNodeCounts()
      const config_lagging = 0
      const nodeFabricState = nodeCounts.online < nodeCounts.total || config_lagging > 0 ? 'warn' : 'ok'
      const files = Array.from({ length: 10 }, (_, i) => {
        const at = new Date(now - (i * 24 + 5) * 3_600_000)
        return {
          name: `pandora-postgres-${at.toISOString().slice(0, 19).replace(/[-:]/g, '')}Z.dump.age`,
          size: 412_000_000 - i * 1_800_000,
          created_at: at.toISOString(),
          has_checksum: i !== 7,
          has_seal: i !== 7 && i !== 8,
        }
      })
      ctx.send(200, {
        backup: {
          dir: '/var/backups/pandora',
          readable: true,
          count: files.length,
          total_bytes: files.reduce((s, f) => s + f.size, 0),
          latest: files[0],
          latest_age_hours: 5,
          stale: false,
          missing_checksum: 1,
          missing_seal: 2,
          recent: files.slice(0, 5),
          identity_configured: true,
          offsite_configured: false,
        },
        database: { size_bytes: 3_418_000_000, connections: 23, max_connections: 200 },
        state: 'degraded',
        components: [
          { key: 'postgres', state: 'ok', latency_ms: 3.2, metrics: { size_bytes: 3_418_000_000, connections: 23, max_connections: 200 } },
          { key: 'redis', state: 'ok', latency_ms: 0.4, metrics: {} },
          { key: 'node_fabric', state: nodeFabricState, metrics: { total: nodeCounts.total, online: nodeCounts.online, config_lagging } },
          { key: 'payment_callbacks', state: 'ok', metrics: { pending: 0 } },
          { key: 'mail', state: 'warn', metrics: { queued: 214, retrying: 21, failed_total: 9 }, message: 'SMTP 连接超时，正在重试' },
          { key: 'telegram', state: 'ok', metrics: { queued: 48, retrying: 0, failed_total: 0 } },
          { key: 'sse', state: 'ok', metrics: { connections: 38 } },
          { key: 'backup', state: 'warn', metrics: { latest_age_hours: 5, stale: false, identity_configured: true, offsite_configured: false } },
        ],
      })
    },

    // active_users 必回；days 1–90，非法按 14
    'GET /v1/stats/timeseries': (ctx) => {
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
