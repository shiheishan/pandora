import { formatCount, formatMoney } from '../../../core/format'
import type { ActivityPoint, RevenuePoint } from './api'
import { formatPercent, percentChange } from './model'

export const REVENUE_VIEW = { width: 600, height: 240 } as const
export const ACTIVITY_VIEW = { width: 600, height: 240 } as const

/**
 * 格式化坐标轴短写。
 * yuan = minor/100
 * yuan >= 10000 -> ¥${(yuan/10000).toFixed(1)}万 (去掉末尾 .0)
 * yuan >= 1000 -> ¥${(yuan/1000).toFixed(1)}k (去掉末尾 .0)
 * 否则 -> ¥${Math.round(yuan)}
 */
export function shortYuan(minor: number): string {
  const yuan = minor / 100
  if (yuan >= 10000) {
    const s = (yuan / 10000).toFixed(1).replace(/\.0$/, '')
    return `¥${s}万`
  }
  if (yuan >= 1000) {
    const s = (yuan / 1000).toFixed(1).replace(/\.0$/, '')
    return `¥${s}k`
  }
  return `¥${Math.round(yuan)}`
}

/**
 * 返回 >= max 的最小「1、2、5 × 10^k」，且不小于 100（即 ¥1）。
 * 例：0 -> 100, 99 -> 100, 101 -> 200, 480000 -> 500000, 510000 -> 1000000, 1200000 -> 2000000
 */
export function niceTop(max: number): number {
  if (max <= 100) return 100
  const power = Math.floor(Math.log10(max))
  const base = 10 ** power
  const fraction = max / base
  if (fraction <= 1) return base
  if (fraction <= 2) return 2 * base
  if (fraction <= 5) return 5 * base
  return 10 * base
}

/** 计数轴的顶：>= max 的最小「1、2、5 × 10^k」，且不小于 10。例：0→10、7→10、11→20、48→50、412→500、5200→10000 */
export function niceCount(max: number): number {
  if (max <= 10) return 10
  const power = Math.floor(Math.log10(max))
  const base = 10 ** power
  const fraction = max / base
  if (fraction <= 1) return base
  if (fraction <= 2) return 2 * base
  if (fraction <= 5) return 5 * base
  return 10 * base
}

/**
 * '2026-10-16' 或 '10-16' -> '10月16日'（月、日去前导零：'03-05' -> '3月5日'）
 */
export function monthDay(date: string): string {
  const match = /(?:^\d{4}-)?0?(\d+)-0?(\d+)$/.exec(date.trim())
  if (!match) return date
  const [, month, day] = match
  return `${month}月${day}日`
}

/**
 * 鼠标在图上横向位置换成最近的点下标，
 * Math.round(offsetX / width * (length - 1)) 再夹在 [0, length-1]；width<=0 或 length<=1 时返回 0。
 */
export function hoverIndex(offsetX: number, width: number, length: number): number {
  if (width <= 0 || length <= 1) return 0
  const idx = Math.round((offsetX / width) * (length - 1))
  return Math.max(0, Math.min(length - 1, idx))
}

/**
 * 柱状图的悬停：offsetX 落在第几根柱（floor(offsetX / width * length)，夹在 [0, length-1]）；width<=0 或 length<=0 时 0
 */
export function barHoverIndex(offsetX: number, width: number, length: number): number {
  if (width <= 0 || length <= 0) return 0
  const idx = Math.floor((offsetX / width) * length)
  return Math.max(0, Math.min(length - 1, idx))
}

/**
 * 按给定点（已是 viewBox 坐标）画 Catmull-Rom 平滑曲线；算法与 smoothPath 相同。0 点 ''，1 点 'M x y L x y'
 */
export function pathThrough(points: readonly { x: number; y: number }[]): string {
  const n = points.length
  if (n === 0) return ''
  if (n === 1) {
    const pt = points[0]!
    const xStr = pt.x.toFixed(1)
    const yStr = pt.y.toFixed(1)
    return `M ${xStr} ${yStr} L ${xStr} ${yStr}`
  }

  let d = `M ${points[0]!.x.toFixed(1)} ${points[0]!.y.toFixed(1)}`

  for (let i = 0; i <= n - 2; i++) {
    const p0 = points[i - 1] ?? points[i]!
    const p1 = points[i]!
    const p2 = points[i + 1]!
    const p3 = points[i + 2] ?? p2

    const c1x = p1.x + (p2.x - p0.x) / 6
    const c1y = p1.y + (p2.y - p0.y) / 6

    const c2x = p2.x - (p3.x - p1.x) / 6
    const c2y = p2.y - (p3.y - p1.y) / 6

    d += ` C ${c1x.toFixed(1)} ${c1y.toFixed(1)} ${c2x.toFixed(1)} ${c2y.toFixed(1)} ${p2.x.toFixed(1)} ${p2.y.toFixed(1)}`
  }

  return d
}

/**
 * 生成平滑 SVG 曲线路径。
 * 点坐标：
 * x = values.length === 1 ? width / 2 : i / (values.length - 1) * width
 * y = height - Math.max(0, v) / max * height（max<=0 时当 1）
 */
export function smoothPath(values: readonly number[], max: number, width: number, height: number): string {
  const n = values.length
  if (n === 0) return ''

  const effectiveMax = max <= 0 ? 1 : max
  const points = values.map((v, i) => {
    const x = n === 1 ? width / 2 : (i / (n - 1)) * width
    const clampedV = Math.max(0, v)
    const y = height - (clampedV / effectiveMax) * height
    return { x, y }
  })

  return pathThrough(points)
}

// ---------------------------------------------------------------------------
// 收入图模型
// ---------------------------------------------------------------------------

export interface ChartMark {
  xPct: number
  yPct: number
  label: string
}

export interface RevenueChart {
  empty: boolean
  total: number
  delta: number | null
  deltaText: string
  up: boolean
  ticks: { topPct: number; label: string }[]
  path: string
  area: string
  marks: ChartMark[]
  axis: [string, string]
}

export function revenueChart(points: readonly RevenuePoint[], previousTotal: number): RevenueChart {
  const n = points.length
  const total = points.reduce((sum, p) => sum + p.displayed_net, 0)
  const empty = points.every(
    (p) => p.displayed_net === 0 && p.actual_credit === 0 && p.actual_debit === 0 && p.adjustment === 0,
  )
  const delta = percentChange(total, previousTotal)
  const deltaText = delta === null ? '' : formatPercent(delta)
  const up = delta === null || delta >= 0

  const maxNet = n > 0 ? Math.max(...points.map((p) => p.displayed_net)) : 0
  const top = niceTop(maxNet)

  const ticks = [
    { topPct: 0, label: shortYuan(top) },
    { topPct: 50, label: shortYuan(top / 2) },
    { topPct: 100, label: shortYuan(0) },
  ]

  const netValues = points.map((p) => p.displayed_net)
  const path = smoothPath(netValues, top, REVENUE_VIEW.width, REVENUE_VIEW.height)
  const area =
    path === ''
      ? ''
      : `${path} L ${REVENUE_VIEW.width} ${REVENUE_VIEW.height} L 0 ${REVENUE_VIEW.height} Z`

  const marks: ChartMark[] = points.map((p, i) => {
    const xPct = n === 1 ? 50 : (i / (n - 1)) * 100
    const yPct = (1 - Math.max(0, p.displayed_net) / top) * 100
    let label = `${monthDay(p.date)} · ${formatMoney(p.displayed_net)}`
    if (p.adjustment !== 0) {
      label += ` · 含调整 ${formatMoney(p.adjustment)}`
    }
    return { xPct, yPct, label }
  })

  const axis: [string, string] = n > 0
    ? [monthDay(points[0]!.date), monthDay(points[Math.floor(n / 2)]!.date)]
    : ['', '']

  return {
    empty,
    total,
    delta,
    deltaText,
    up,
    ticks,
    path,
    area,
    marks,
    axis,
  }
}

// ---------------------------------------------------------------------------
// 注册与活跃图模型
// ---------------------------------------------------------------------------

export interface ActivityBar {
  xPct: number
  wPct: number
  hPct: number
  last: boolean
}

export interface ActivityMark {
  xPct: number
  regYPct: number
  actYPct: number
  label: string
}

export interface ActivityChart {
  empty: boolean
  registeredTotal: number
  activeAverage: number
  ticks: { topPct: number; label: string }[]
  bars: ActivityBar[]
  activePath: string
  marks: ActivityMark[]
  axisStart: string
}

export function activityChart(points: readonly ActivityPoint[]): ActivityChart {
  const n = points.length
  const empty = points.every(
    (p) => p.registered === 0 && p.active_users === 0 && p.logins === 0 && p.orders === 0,
  )
  const registeredTotal = points.reduce((sum, p) => sum + p.registered, 0)
  const activeTotal = points.reduce((sum, p) => sum + p.active_users, 0)
  const activeAverage = n > 0 ? Math.round(activeTotal / n) : 0

  const maxReg = n > 0 ? Math.max(...points.map((p) => p.registered)) : 0
  const regTop = niceCount(maxReg)

  const maxAct = n > 0 ? Math.max(...points.map((p) => p.active_users)) : 0
  const actTop = niceCount(maxAct)

  const bw = n > 0 ? 100 / n : 0
  const bars: ActivityBar[] =
    n === 0
      ? []
      : points.map((p, i) => {
          const hPct = regTop > 0 ? (p.registered / regTop) * 100 : 0
          return {
            xPct: i * bw + bw * 0.22,
            wPct: bw * 0.56,
            hPct,
            last: i === n - 1,
          }
        })

  const activePoints =
    n === 0
      ? []
      : points.map((p, i) => {
          const x = ((i + 0.5) / n) * ACTIVITY_VIEW.width
          const y = ACTIVITY_VIEW.height - (p.active_users / actTop) * ACTIVITY_VIEW.height
          return { x, y }
        })
  const activePath = pathThrough(activePoints)

  const marks: ActivityMark[] =
    n === 0
      ? []
      : points.map((p, i) => {
          const xPct = ((i + 0.5) / n) * 100
          const hPct = regTop > 0 ? (p.registered / regTop) * 100 : 0
          const regYPct = 100 - hPct
          const actYPct = (1 - p.active_users / actTop) * 100
          const label = `${monthDay(p.day)} · 注册 ${formatCount(p.registered)} · 日活 ${formatCount(p.active_users)}`
          return { xPct, regYPct, actYPct, label }
        })

  const ticks = [
    { topPct: 0, label: formatCount(regTop) },
    { topPct: 50, label: formatCount(Math.round(regTop / 2)) },
    { topPct: 100, label: '0' },
  ]

  const axisStart = n > 0 ? monthDay(points[0]!.day) : ''

  return {
    empty,
    registeredTotal,
    activeAverage,
    ticks,
    bars,
    activePath,
    marks,
    axisStart,
  }
}
