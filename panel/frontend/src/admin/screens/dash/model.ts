import { formatCount, formatMoney } from '../../../core/format'
import type { RouteQuery } from '../../../core/router'
import type { AdminMe } from '../../me'
import type { ModuleKey } from '../../modules'
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
  /** 目标页的地址筛选（如订单页 s=pending） */
  query?: RouteQuery
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

export type TodoKey = 'tickets_open' | 'withdrawals_pending' | 'orders_pending_stale' | 'ledger_drift'

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

function todoSummary(cards: readonly TodoCard[]): string {
  const pending = cards.filter((c) => c.count > 0).length
  if (pending === 0) return '没有待处理的事'
  const urgent = cards.filter((c) => c.count > 0 && (c.key === 'tickets_open' || c.key === 'ledger_drift')).length
  return `${pending} 项` + (urgent > 0 ? ` · ${urgent} 项紧急` : '')
}

export function todoBoard(items: readonly TaskItem[] | undefined): TodoBoard {
  const tickets = findItem(items, 'tickets_open')
  const withdrawals = findItem(items, 'withdrawals_pending')
  const stale = findItem(items, 'orders_pending_stale')
  const drift = findItem(items, 'ledger_drift')

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

  if (drift && drift.count > 0) {
    cards.push({
      key: 'ledger_drift',
      title: '账本漂移',
      count: drift.count,
      meta: '余额与账本流水对不上的账户',
      action: '去核对',
      target: { module: 'billing', tab: null },
      hot: false,
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
