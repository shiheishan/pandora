import { formatCount, formatMoney } from '../../../core/format'
import type { RouteQuery } from '../../../core/router'
import type { AdminMe } from '../../me'
import { modulePath, type ModuleKey } from '../../modules'
import type { TaskItem } from '../../tasks'
import type { Overview } from './api'

// ===========================================================================
// 格式化
// ===========================================================================

/** 百分比变化：+12.4% / −3.1%（设计稿用数学减号）/ 0.0% */
export function formatPercent(ratio: number): string {
  const pct = Math.round(ratio * 1000) / 10
  if (pct === 0) return '0.0%'
  return `${pct > 0 ? '+' : '−'}${Math.abs(pct).toFixed(1)}%`
}

/** 相对变化；上一期为 0 时无意义，返回 null */
export function percentChange(current: number, previous: number): number | null {
  if (previous === 0) return null
  return (current - previous) / Math.abs(previous)
}

/** 等待时长：26 分钟 / 3 小时 / 2 天 */
export function formatDuration(seconds: number): string {
  if (seconds < 60) return '不到 1 分钟'
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes} 分钟`
  const hours = Math.floor(minutes / 60)
  if (hours < 48) return `${hours} 小时`
  return `${Math.floor(hours / 24)} 天`
}

export interface Target {
  module: ModuleKey
  tab: string | null
  /** 「打开某一条」：拼在 modulePath(module, tab) 后面（编码后），如节点详情 /nodes/nodes/<id>；只用于 admin/modules.ts 的 DETAIL_ROUTES 登记过的「模块:页签」 */
  id?: string
  /** 目标页的地址筛选（如订单页 s=pending） */
  query?: RouteQuery
}

/** 去处的 hash 路径（不含查询串）：modulePath，有 id 时再接一段编码后的 id */
export function targetPath(target: Target): string {
  const base = modulePath(target.module, target.tab)
  return target.id ? `${base}/${encodeURIComponent(target.id)}` : base
}

// ===========================================================================
// 问候行
// ===========================================================================

function greetingWord(hour: number): string {
  if (hour >= 5 && hour <= 10) return '早上好'
  if (hour >= 11 && hour <= 12) return '中午好'
  if (hour >= 13 && hour <= 17) return '下午好'
  return '晚上好'
}

function displayName(me: Pick<AdminMe, 'display_name' | 'email'> | undefined): string {
  if (!me) return ''
  const name = me.display_name?.trim()
  if (name) return name
  const local = me.email.split('@')[0]?.trim()
  return local ?? ''
}

/** 问候行：日期「10 月 16 日 周五」+ 问候「下午好，林舟」 */
export function greeting(now: Date, me: Pick<AdminMe, 'display_name' | 'email'> | undefined): { date: string; hello: string } {
  const weekdays = '日一二三四五六'
  const date = `${now.getMonth() + 1} 月 ${now.getDate()} 日 周${weekdays[now.getDay()]}`
  const word = greetingWord(now.getHours())
  const name = displayName(me)
  const hello = name ? `${word}，${name}` : word
  return { date, hello }
}

// ===========================================================================
// 「要处理」
// ===========================================================================

export type TodoKey = 'tickets_open' | 'withdrawals_pending' | 'orders_pending_stale' | 'node_certs' | 'integrity'

export interface TodoCard {
  key: TodoKey
  title: string
  count: number
  meta: string
  action: string
  target: Target
  hot: boolean
}

export interface TodoBoard {
  cards: TodoCard[]
  summary: string
}

function findItem<K extends TaskItem['kind']>(items: readonly TaskItem[] | undefined, kind: K): Extract<TaskItem, { kind: K }> | undefined {
  return items?.find((i): i is Extract<TaskItem, { kind: K }> => i.kind === kind)
}

function ticketMeta(item: Extract<TaskItem, { kind: 'tickets_open' }> | undefined): string {
  if (!item || item.count === 0) return '没有待回复的工单'
  const parts = [
    item.high_priority > 0 ? `高优先级 ${item.high_priority} 个` : '',
    item.oldest_wait_seconds !== null ? `最久等了 ${formatDuration(item.oldest_wait_seconds)}` : '',
  ].filter(Boolean)
  return parts.length > 0 ? parts.join(' · ') : '有新工单待回复'
}

function withdrawalMeta(item: Extract<TaskItem, { kind: 'withdrawals_pending' }> | undefined): string {
  if (!item || item.count === 0) return '没有待审核的提现'
  return `合计 ${formatMoney(item.amount)}`
}

function staleOrderMeta(item: Extract<TaskItem, { kind: 'orders_pending_stale' }> | undefined): string {
  if (!item || item.count === 0) return '没有超时未支付的订单'
  return `超过 ${Math.round(item.threshold_seconds / 60)} 分钟没到账，可能是回调丢失`
}

function certMeta(item: Extract<TaskItem, { kind: 'node_certs' }>): string {
  const parts: string[] = []
  if (item.failing > 0) parts.push(`申请失败 ${item.failing} 个`)
  if (item.expiring > 0) parts.push(`快到期 ${item.expiring} 个`)
  if (item.count === 1 && item.sample[0]) {
    parts.push(item.sample[0].name)
  } else {
    const names = item.sample.slice(0, 2).map((s) => s.name)
    if (names.length > 0) parts.push(`如 ${names.join('、')}`)
  }
  return parts.join(' · ')
}

function certTarget(item: Extract<TaskItem, { kind: 'node_certs' }>): Target {
  if (item.count === 1 && item.sample[0]) {
    return { module: 'nodes', tab: 'nodes', id: item.sample[0].id }
  }
  return { module: 'nodes', tab: 'nodes', query: { f: 'cert' } }
}

// ---------------------------------------------------------------------------
// 数据巡检：后端每 30 分钟跑一轮（report:integrity），每项给最近一次的违例数。
// 代号与后端 report/service/integrity.go 的常量一一对应；中文名与告警里的检查名一致，处理办法见运维手册「数据巡检告警」。
// 每项的去处是最接近的后台页（按 INTEGRITY_CHECKS 顺序取第一项有违例的）。
// ---------------------------------------------------------------------------
const INTEGRITY_CHECKS: ReadonlyArray<{ code: string; label: string; target: Target }> = [
  { code: 'ledger_drift', label: '账本漂移', target: { module: 'billing', tab: null } },
  { code: 'ledger_hold_mismatch', label: '余额冻结对不上', target: { module: 'billing', tab: 'orders' } },
  { code: 'payment_unsettled', label: '收款了订单却没付', target: { module: 'billing', tab: 'arrears' } },
  { code: 'order_paid_unfulfilled', label: '已付订单没落地', target: { module: 'billing', tab: 'orders' } },
  { code: 'order_reservation_mismatch', label: '订单预留没释放', target: { module: 'billing', tab: 'orders' } },
  { code: 'commission_unpaid_order', label: '佣金挂在没付的订单上', target: { module: 'marketing', tab: 'commission' } },
  { code: 'giftcard_redemption_mismatch', label: '礼品卡兑换对不上', target: { module: 'marketing', tab: 'gifts' } },
  { code: 'subscription_expire_overdue', label: '订阅到期没停', target: { module: 'users', tab: 'list' } },
  { code: 'subscription_close_overdue', label: '过期订阅没关续费窗口', target: { module: 'users', tab: 'list' } },
]

/** 巡检代号的中文名；前端还不认识的新检查原样显示代号 */
export function integrityLabel(code: string): string {
  return INTEGRITY_CHECKS.find((c) => c.code === code)?.label ?? code
}

type IntegrityCheckRow = Extract<TaskItem, { kind: 'integrity' }>['checks'][number]

/** 有违例的巡检项（按 INTEGRITY_CHECKS 顺序，前端不认识的排最后） */
function failingChecks(item: Extract<TaskItem, { kind: 'integrity' }> | undefined): IntegrityCheckRow[] {
  const order = (code: string) => {
    const i = INTEGRITY_CHECKS.findIndex((c) => c.code === code)
    return i === -1 ? INTEGRITY_CHECKS.length : i
  }
  return (item?.checks ?? []).filter((c) => c.count > 0).sort((a, b) => order(a.code) - order(b.code))
}

/** 巡检每 30 分钟一轮；某项超过这么久没更新，说明巡检没在跑或这一项每轮都失败（失败的项保留上一次的结果） */
export const INTEGRITY_STALE_MS = 2 * 60 * 60 * 1000

/** 超过 INTEGRITY_STALE_MS 没更新的巡检项；asOf 是接口返回的服务器时间，不用本机时钟 */
function outdatedChecks(item: Extract<TaskItem, { kind: 'integrity' }> | undefined, asOf: string | undefined): IntegrityCheckRow[] {
  const now = asOf ? Date.parse(asOf) : NaN
  if (Number.isNaN(now)) return []
  return (item?.checks ?? []).filter((c) => {
    const at = Date.parse(c.checked_at)
    return !Number.isNaN(at) && now - at > INTEGRITY_STALE_MS
  })
}

/** 最多列两项「中文名 违例数」，多的写「等 N 项」 */
function integrityMeta(failing: readonly IntegrityCheckRow[]): string {
  const shown = failing.slice(0, 2).map((c) => `${integrityLabel(c.code)} ${formatCount(c.count)}`)
  const rest = failing.length - shown.length
  return shown.join(' · ') + (rest > 0 ? ` 等 ${failing.length} 项` : '')
}

function todoSummary(cards: readonly TodoCard[]): string {
  const pending = cards.filter((c) => c.count > 0).length
  if (pending === 0) return '没有待处理的事'
  const urgent = cards.filter((c) => c.count > 0 && c.hot).length
  return `${pending} 项` + (urgent > 0 ? ` · ${urgent} 项紧急` : '')
}

export function todoBoard(items: readonly TaskItem[] | undefined, asOf?: string): TodoBoard {
  const tickets = findItem(items, 'tickets_open')
  const withdrawals = findItem(items, 'withdrawals_pending')
  const stale = findItem(items, 'orders_pending_stale')
  const nodeCerts = findItem(items, 'node_certs')
  const integrity = findItem(items, 'integrity')
  const failing = failingChecks(integrity)
  const outdated = outdatedChecks(integrity, asOf)

  const cards: TodoCard[] = [
    {
      key: 'tickets_open',
      title: '待处理工单',
      count: tickets?.count ?? 0,
      meta: ticketMeta(tickets),
      action: '去回复',
      target: { module: 'tickets', tab: null },
      hot: (tickets?.count ?? 0) > 0,
    },
    {
      key: 'withdrawals_pending',
      title: '提现待审核',
      count: withdrawals?.count ?? 0,
      meta: withdrawalMeta(withdrawals),
      action: '去审核',
      target: { module: 'marketing', tab: 'commission' },
      hot: false,
    },
    {
      key: 'orders_pending_stale',
      title: '超时未支付订单',
      count: stale?.count ?? 0,
      meta: staleOrderMeta(stale),
      action: '去核对',
      target: { module: 'billing', tab: 'orders', query: { s: 'pending' } },
      hot: false,
    },
  ]

  if (nodeCerts && nodeCerts.count > 0) {
    cards.push({
      key: 'node_certs',
      title: '证书异常',
      count: nodeCerts.count,
      meta: certMeta(nodeCerts),
      action: '去看看',
      target: certTarget(nodeCerts),
      hot: nodeCerts.failing > 0,
    })
  }

  // 巡检有违例或有项太久没更新才多出一格，标红；数字是出问题的检查项数（各项违例的单位不同，不相加），
  // 去处是第一项有违例的检查对应的页
  const problems = new Set([...failing, ...outdated].map((c) => c.code))
  if (problems.size > 0) {
    const firstFailing = failing[0]
    const meta = [
      failing.length > 0 ? integrityMeta(failing) : '',
      outdated.length > 0 ? `巡检没在跑：${outdated.length} 项超过 2 小时没更新` : '',
    ]
    cards.push({
      key: 'integrity',
      title: '数据巡检',
      count: problems.size,
      meta: meta.filter(Boolean).join(' · '),
      action: '去核对',
      target: (firstFailing && INTEGRITY_CHECKS.find((c) => c.code === firstFailing.code)?.target) ?? { module: 'billing', tab: null },
      hot: true,
    })
  }

  return { cards, summary: todoSummary(cards) }
}

// ===========================================================================
// 「今天」KPI
// ===========================================================================

export interface KpiTile {
  key: 'revenue' | 'orders' | 'signups' | 'subs'
  label: string
  value: string
  cents?: string
  compare: { label: string; value: string }
}

function splitMoney(minor: number): { value: string; cents?: string } {
  const full = formatMoney(minor)
  const dot = full.lastIndexOf('.')
  if (dot === -1) return { value: full }
  return { value: full.slice(0, dot), cents: full.slice(dot) }
}

export function kpiTiles(o: Overview): KpiTile[] {
  const rev = splitMoney(o.revenue.today)
  return [
    {
      key: 'revenue',
      label: '收入',
      value: rev.value,
      cents: rev.cents,
      compare: { label: '昨天全天', value: formatMoney(o.revenue.yesterday) },
    },
    {
      key: 'orders',
      label: '付费订单',
      value: formatCount(o.orders.paid_today),
      compare: { label: '待支付', value: formatCount(o.orders.pending) },
    },
    {
      key: 'signups',
      label: '新注册',
      value: formatCount(o.users.today),
      compare: { label: '近 7 天', value: formatCount(o.users.last_7_days) },
    },
    {
      key: 'subs',
      label: '有效订阅',
      value: formatCount(o.subscriptions.active),
      compare: { label: '7 天内到期', value: formatCount(o.subscriptions.expiring_7_days) },
    },
  ]
}

/** 「截至 14:32」：两位小时与分钟 */
export function asOfText(at: Date): string {
  const p = (n: number) => String(n).padStart(2, '0')
  return `截至 ${p(at.getHours())}:${p(at.getMinutes())}`
}
