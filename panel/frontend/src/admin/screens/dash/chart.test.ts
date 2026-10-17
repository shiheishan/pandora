import { describe, expect, it } from 'vitest'
import type { ActivityPoint, RevenuePoint } from './api'
import {
  ACTIVITY_VIEW,
  activityChart,
  barHoverIndex,
  hoverIndex,
  monthDay,
  niceCount,
  niceTop,
  pathThrough,
  REVENUE_VIEW,
  revenueChart,
  shortYuan,
  smoothPath,
} from './chart'

describe('smoothPath', () => {
  it('0 点返回空串', () => {
    expect(smoothPath([], 100, 600, 180)).toBe('')
  })

  it('1 点返回 M x y L x y 且居中', () => {
    expect(smoothPath([50], 100, 600, 180)).toBe('M 300.0 90.0 L 300.0 90.0')
  })

  it('3 点路径以 M0.0 开头并包含 2 个 C', () => {
    const path = smoothPath([10, 20, 30], 50, 600, 180)
    expect(path.startsWith('M 0.0 ')).toBe(true)
    const curveCount = (path.match(/ C /g) || []).length
    expect(curveCount).toBe(2)
  })

  it('负值按 0 算贴底（y = height）', () => {
    const path = smoothPath([-20], 100, 600, 180)
    expect(path).toBe('M 300.0 180.0 L 300.0 180.0')
  })

  it('max <= 0 时当作 1 处理，避免除以零', () => {
    const path = smoothPath([0], 0, 600, 180)
    expect(path).toBe('M 300.0 180.0 L 300.0 180.0')
  })

  it('与 pathThrough 输出一致', () => {
    const values = [10, 20, 30, 15]
    const max = 50
    const width = 600
    const height = 240
    const n = values.length
    const effectiveMax = max
    const points = values.map((v, i) => {
      const x = n === 1 ? width / 2 : (i / (n - 1)) * width
      const y = height - (Math.max(0, v) / effectiveMax) * height
      return { x, y }
    })
    expect(smoothPath(values, max, width, height)).toBe(pathThrough(points))
  })
})

describe('pathThrough', () => {
  it('0 点返回空串', () => {
    expect(pathThrough([])).toBe('')
  })

  it('1 点返回 M x y L x y', () => {
    expect(pathThrough([{ x: 100, y: 50 }])).toBe('M 100.0 50.0 L 100.0 50.0')
  })
})

describe('niceTop', () => {
  it('返回 >= max 的最小「1、2、5 × 10^k」，且不小于 100（即 ¥1）', () => {
    expect(niceTop(0)).toBe(100)
    expect(niceTop(99)).toBe(100)
    expect(niceTop(100)).toBe(100)
    expect(niceTop(101)).toBe(200)
    expect(niceTop(200)).toBe(200)
    expect(niceTop(201)).toBe(500)
    expect(niceTop(500)).toBe(500)
    expect(niceTop(501)).toBe(1000)
    expect(niceTop(480000)).toBe(500000)
    expect(niceTop(510000)).toBe(1000000)
    expect(niceTop(1200000)).toBe(2000000)
  })
})

describe('niceCount', () => {
  it('返回 >= max 的最小「1、2、5 × 10^k」，且不小于 10', () => {
    expect(niceCount(0)).toBe(10)
    expect(niceCount(7)).toBe(10)
    expect(niceCount(10)).toBe(10)
    expect(niceCount(11)).toBe(20)
    expect(niceCount(48)).toBe(50)
    expect(niceCount(412)).toBe(500)
    expect(niceCount(5200)).toBe(10000)
  })
})

describe('shortYuan', () => {
  it('格式化坐标轴短写', () => {
    expect(shortYuan(0)).toBe('¥0')
    expect(shortYuan(5000)).toBe('¥50')
    expect(shortYuan(250000)).toBe('¥2.5k')
    expect(shortYuan(500000)).toBe('¥5k')
    expect(shortYuan(2000000)).toBe('¥2万')
    expect(shortYuan(2500000)).toBe('¥2.5万')
    expect(shortYuan(15000000)).toBe('¥15万')
  })
})

describe('monthDay', () => {
  it('YYYY-MM-DD 或 MM-DD 格式转为 M月D日', () => {
    expect(monthDay('2026-10-16')).toBe('10月16日')
    expect(monthDay('10-16')).toBe('10月16日')
    expect(monthDay('03-05')).toBe('3月5日')
    expect(monthDay('2026-03-05')).toBe('3月5日')
    expect(monthDay('2026-01-01')).toBe('1月1日')
  })

  it('不符合日期格式时原样返回', () => {
    expect(monthDay('')).toBe('')
    expect(monthDay('unknown')).toBe('unknown')
  })
})

describe('hoverIndex', () => {
  it('边界与正常计算', () => {
    expect(hoverIndex(0, 600, 7)).toBe(0)
    expect(hoverIndex(600, 600, 7)).toBe(6)
    expect(hoverIndex(300, 600, 7)).toBe(3)
    expect(hoverIndex(-50, 600, 7)).toBe(0)
    expect(hoverIndex(700, 600, 7)).toBe(6)
    expect(hoverIndex(100, 0, 7)).toBe(0)
    expect(hoverIndex(100, -100, 7)).toBe(0)
    expect(hoverIndex(100, 600, 1)).toBe(0)
    expect(hoverIndex(100, 600, 0)).toBe(0)
  })
})

describe('barHoverIndex', () => {
  it('按柱宽分段', () => {
    expect(barHoverIndex(0, 300, 3)).toBe(0)
    expect(barHoverIndex(150, 300, 3)).toBe(1)
    expect(barHoverIndex(299, 300, 3)).toBe(2)
    expect(barHoverIndex(400, 300, 3)).toBe(2)
    expect(barHoverIndex(-5, 300, 3)).toBe(0)
    expect(barHoverIndex(100, 0, 3)).toBe(0)
    expect(barHoverIndex(100, 300, 0)).toBe(0)
  })
})

describe('revenueChart', () => {
  const p = (date: string, net: number, adjustment = 0): RevenuePoint => ({
    date,
    actual_credit: net - adjustment,
    actual_debit: 0,
    adjustment,
    displayed_net: net,
  })

  it('空点数组处理', () => {
    const chart = revenueChart([], 0)
    expect(chart.empty).toBe(true)
    expect(chart.total).toBe(0)
    expect(chart.delta).toBeNull()
    expect(chart.deltaText).toBe('')
    expect(chart.up).toBe(true)
    expect(chart.path).toBe('')
    expect(chart.area).toBe('')
    expect(chart.marks).toEqual([])
    expect(chart.axis).toEqual(['', ''])
    expect(chart.ticks).toEqual([
      { topPct: 0, label: '¥1' },
      { topPct: 50, label: '¥1' },
      { topPct: 100, label: '¥0' },
    ])
  })

  it('所有点均为 0 时 empty 为 true', () => {
    const chart = revenueChart([p('2026-10-14', 0), p('2026-10-15', 0)], 0)
    expect(chart.empty).toBe(true)
    expect(chart.total).toBe(0)
  })

  it('previousTotal 为 0 时 deltaText 为空', () => {
    const chart = revenueChart([p('2026-10-16', 5000)], 0)
    expect(chart.delta).toBeNull()
    expect(chart.deltaText).toBe('')
  })

  it('正常数据：total、delta、ticks、marks、axis、area', () => {
    const points = [
      p('2026-10-14', 10000),
      p('2026-10-15', 30000, 5000),
      p('2026-10-16', 20000),
    ]
    const chart = revenueChart(points, 50000)

    expect(chart.empty).toBe(false)
    expect(chart.total).toBe(60000)
    expect(chart.delta).toBeCloseTo(0.2)
    expect(chart.deltaText).toBe('+20.0%')
    expect(chart.up).toBe(true)

    expect(chart.ticks).toEqual([
      { topPct: 0, label: '¥500' },
      { topPct: 50, label: '¥250' },
      { topPct: 100, label: '¥0' },
    ])

    expect(chart.marks).toHaveLength(3)
    expect(chart.marks[0]!.xPct).toBe(0)
    expect(chart.marks[1]!.xPct).toBe(50)
    expect(chart.marks[2]!.xPct).toBe(100)
    expect(chart.marks[1]!.yPct).toBeCloseTo(40)
    expect(chart.marks[0]!.label).toBe('10月14日 · ¥100.00')
    expect(chart.marks[1]!.label).toBe('10月15日 · ¥300.00 · 含调整 ¥50.00')
    expect(chart.axis).toEqual(['10月14日', '10月15日'])

    expect(chart.path.startsWith('M ')).toBe(true)
    expect(chart.area.startsWith(chart.path)).toBe(true)
    expect(chart.area.endsWith(' Z')).toBe(true)
  })

  it('单点 marks.xPct 为 50', () => {
    const chart = revenueChart([p('2026-10-16', 5000)], 0)
    expect(chart.marks).toHaveLength(1)
    expect(chart.marks[0]!.xPct).toBe(50)
    expect(chart.axis).toEqual(['10月16日', '10月16日'])
  })

  it('常数 REVENUE_VIEW 宽 600 高 240', () => {
    expect(REVENUE_VIEW).toEqual({ width: 600, height: 240 })
  })
})

describe('activityChart', () => {
  const pt = (day: string, registered: number, active_users: number): ActivityPoint => ({
    day,
    registered,
    logins: 10,
    orders: 5,
    unique_ips: 8,
    active_users,
  })

  it('空点数组处理', () => {
    const chart = activityChart([])
    expect(chart.empty).toBe(true)
    expect(chart.registeredTotal).toBe(0)
    expect(chart.activeAverage).toBe(0)
    expect(chart.bars).toEqual([])
    expect(chart.activePath).toBe('')
    expect(chart.marks).toEqual([])
    expect(chart.axisStart).toBe('')
    expect(chart.ticks).toEqual([
      { topPct: 0, label: '10' },
      { topPct: 50, label: '5' },
      { topPct: 100, label: '0' },
    ])
  })

  it('所有点均为 0 时 empty 为 true', () => {
    const chart = activityChart([
      { day: '10-15', registered: 0, logins: 0, orders: 0, unique_ips: 0, active_users: 0 },
    ])
    expect(chart.empty).toBe(true)
  })

  it('正常数据：bars、ticks、marks、activePath、axisStart', () => {
    const points = [
      pt('10-14', 10, 100),
      pt('10-15', 30, 200),
      pt('10-16', 20, 150),
      pt('10-17', 5, 80),
    ]
    const chart = activityChart(points)

    expect(chart.empty).toBe(false)
    expect(chart.registeredTotal).toBe(65)
    expect(chart.activeAverage).toBe(133)
    expect(chart.axisStart).toBe('10月14日')

    expect(chart.bars).toHaveLength(4)
    expect(chart.bars[0]!.xPct).toBeCloseTo(5.5)
    expect(chart.bars[0]!.wPct).toBeCloseTo(14)
    expect(chart.bars[0]!.last).toBe(false)
    expect(chart.bars[3]!.last).toBe(true)

    expect(chart.ticks).toEqual([
      { topPct: 0, label: '50' },
      { topPct: 50, label: '25' },
      { topPct: 100, label: '0' },
    ])

    expect(chart.marks).toHaveLength(4)
    expect(chart.marks[1]!.label).toBe('10月15日 · 注册 30 · 日活 200')
    expect(chart.marks[1]!.regYPct).toBeCloseTo(40)
    expect(chart.marks[1]!.actYPct).toBeCloseTo(0)

    expect(chart.activePath.startsWith('M ')).toBe(true)
  })

  it('常数 ACTIVITY_VIEW 宽 600 高 240', () => {
    expect(ACTIVITY_VIEW).toEqual({ width: 600, height: 240 })
  })
})
