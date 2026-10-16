import { describe, expect, it } from 'vitest'
import type { ActivityPoint, RevenuePoint } from './api'
import {
  ACTIVITY_VIEW,
  activityChart,
  hoverIndex,
  monthDay,
  niceTop,
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
    // 宽 600, 高 180, 值 50, max 100 -> x = 300.0, y = 180 - (50/100)*180 = 90.0
    expect(smoothPath([50], 100, 600, 180)).toBe('M 300.0 90.0 L 300.0 90.0')
  })

  it('3 点路径以 M0.0 开头并包含 2 个 C', () => {
    const path = smoothPath([10, 20, 30], 50, 600, 180)
    expect(path.startsWith('M 0.0 ')).toBe(true)
    const curveCount = (path.match(/ C /g) || []).length
    expect(curveCount).toBe(2)
  })

  it('负值按 0 算贴底（y = height）', () => {
    // 宽 600, 高 180, 值 -20 -> Math.max(0, -20) = 0 -> y = 180.0
    const path = smoothPath([-20], 100, 600, 180)
    expect(path).toBe('M 300.0 180.0 L 300.0 180.0')
  })

  it('max <= 0 时当作 1 处理，避免除以零', () => {
    const path = smoothPath([0], 0, 600, 180)
    expect(path).toBe('M 300.0 180.0 L 300.0 180.0')
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
    // 超过边界夹在 [0, length-1]
    expect(hoverIndex(-50, 600, 7)).toBe(0)
    expect(hoverIndex(700, 600, 7)).toBe(6)
    // 异常尺寸 / 长度
    expect(hoverIndex(100, 0, 7)).toBe(0)
    expect(hoverIndex(100, -100, 7)).toBe(0)
    expect(hoverIndex(100, 600, 1)).toBe(0)
    expect(hoverIndex(100, 600, 0)).toBe(0)
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
    expect(chart.previousTotal).toBe(0)
    expect(chart.delta).toBeNull()
    expect(chart.deltaText).toBe('—')
    expect(chart.up).toBe(true)
    expect(chart.average).toBe(0)
    expect(chart.peak).toEqual({ value: 0, label: '—' })
    expect(chart.path).toBe('')
    expect(chart.marks).toEqual([])
    expect(chart.axis).toEqual(['', ''])
    expect(chart.ticks).toEqual([
      { topPct: 0, label: '¥1' },
      { topPct: 50, label: '¥1' }, // 100 / 2 = 50 分 = ¥0.5 -> Math.round -> 1
      { topPct: 100, label: '¥0' },
    ])
  })

  it('所有点均为 0 时 empty 为 true', () => {
    const chart = revenueChart([p('2026-10-14', 0), p('2026-10-15', 0)], 0)
    expect(chart.empty).toBe(true)
    expect(chart.total).toBe(0)
  })

  it('正常数据：total、average、peak、delta、ticks、marks、axis', () => {
    const points = [
      p('2026-10-14', 10000), // 100 元
      p('2026-10-15', 30000, 5000), // 300 元，含调整 50 元
      p('2026-10-16', 20000), // 200 元
    ]
    const chart = revenueChart(points, 50000)

    expect(chart.empty).toBe(false)
    expect(chart.total).toBe(60000)
    expect(chart.previousTotal).toBe(50000)
    // delta = (60000 - 50000) / 50000 = +0.2 -> +20.0%
    expect(chart.delta).toBeCloseTo(0.2)
    expect(chart.deltaText).toBe('+20.0%')
    expect(chart.up).toBe(true)
    expect(chart.average).toBe(20000)
    expect(chart.peak).toEqual({ value: 30000, label: '10月15日' })

    // 最大 30000 分（¥300）-> niceTop = 50000 分（¥500）
    expect(chart.ticks).toEqual([
      { topPct: 0, label: '¥500' },
      { topPct: 50, label: '¥250' },
      { topPct: 100, label: '¥0' },
    ])

    // marks 长度与百分比定位
    expect(chart.marks).toHaveLength(3)
    expect(chart.marks[0]!.xPct).toBe(0)
    expect(chart.marks[1]!.xPct).toBe(50)
    expect(chart.marks[2]!.xPct).toBe(100)

    // yPct: (1 - 30000 / 50000) * 100 = 40
    expect(chart.marks[1]!.yPct).toBeCloseTo(40)

    // label 检查（含调整）
    expect(chart.marks[0]!.label).toBe('10月14日 · ¥100.00')
    expect(chart.marks[1]!.label).toBe('10月15日 · ¥300.00 · 含调整 ¥50.00')

    // axis: [第一个点, 中间点 Math.floor(3/2) = 1]
    expect(chart.axis).toEqual(['10月14日', '10月15日'])
  })

  it('单点 marks.xPct 为 50', () => {
    const chart = revenueChart([p('2026-10-16', 5000)], 0)
    expect(chart.marks).toHaveLength(1)
    expect(chart.marks[0]!.xPct).toBe(50)
    expect(chart.axis).toEqual(['10月16日', '10月16日'])
  })

  it('常数 REVENUE_VIEW 宽 600 高 180', () => {
    expect(REVENUE_VIEW).toEqual({ width: 600, height: 180 })
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
    expect(chart.registeredPath).toBe('')
    expect(chart.activePath).toBe('')
    expect(chart.marks).toEqual([])
    expect(chart.axisStart).toBe('')
  })

  it('所有点均为 0 时 empty 为 true', () => {
    const chart = activityChart([
      { day: '10-15', registered: 0, logins: 0, orders: 0, unique_ips: 0, active_users: 0 },
    ])
    expect(chart.empty).toBe(true)
  })

  it('正常数据：registeredTotal、activeAverage、marks、registeredPath、activePath、axisStart', () => {
    const points = [
      pt('10-14', 10, 100),
      pt('10-15', 30, 200),
      pt('10-16', 20, 150),
    ]
    const chart = activityChart(points)

    expect(chart.empty).toBe(false)
    expect(chart.registeredTotal).toBe(60)
    expect(chart.activeAverage).toBe(150) // (100 + 200 + 150) / 3 = 150
    expect(chart.axisStart).toBe('10月14日')

    // marks
    expect(chart.marks).toHaveLength(3)
    expect(chart.marks[0]!.xPct).toBe(0)
    expect(chart.marks[1]!.xPct).toBe(50)
    expect(chart.marks[2]!.xPct).toBe(100)

    // regMax = 30, actMax = 200
    // 点 1 (10-15): registered = 30 -> regYPct = (1 - 30 * 0.7 / 30) * 100 = 30%
    // 点 1 (10-15): active_users = 200 -> actYPct = (1 - 200 / 200) * 100 = 0%
    expect(chart.marks[1]!.regYPct).toBeCloseTo(30)
    expect(chart.marks[1]!.actYPct).toBeCloseTo(0)

    expect(chart.marks[1]!.label).toBe('10月15日 · 注册 30 · 活跃 200')

    // 路径非空
    expect(chart.registeredPath.startsWith('M ')).toBe(true)
    expect(chart.activePath.startsWith('M ')).toBe(true)
  })

  it('常数 ACTIVITY_VIEW 宽 600 高 150', () => {
    expect(ACTIVITY_VIEW).toEqual({ width: 600, height: 150 })
  })
})
