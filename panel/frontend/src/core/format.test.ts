/**
 * [INPUT]: 依赖 vitest，依赖 ./format 的 formatMoney / relativeTime
 * [OUTPUT]: 对外提供 format.ts 的单元测试
 * [POS]: core/format 的单元测试：分转元、千分位、负数、币种符号；相对时间各档
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it } from 'vitest'
import { formatMoney, relativeTime } from './format'

describe('formatMoney', () => {
  it('formats minor units with grouping, sign and currency symbol', () => {
    expect(formatMoney(2650)).toBe('¥26.50')
    expect(formatMoney(0)).toBe('¥0.00')
    expect(formatMoney(128000)).toBe('¥1,280.00')
    expect(formatMoney(-5)).toBe('-¥0.05')
    expect(formatMoney(123456789, 'USD')).toBe('$1,234,567.89')
    expect(formatMoney(100, 'EUR')).toBe('EUR 1.00')
  })
})

describe('relativeTime', () => {
  const now = new Date('2026-09-24T12:00:00')
  it('buckets like the design', () => {
    expect(relativeTime(new Date('2026-09-24T11:59:30'), now)).toBe('刚刚')
    expect(relativeTime(new Date('2026-09-24T11:58:00'), now)).toBe('2 分钟')
    expect(relativeTime(new Date('2026-09-24T09:00:00'), now)).toBe('3 小时')
    expect(relativeTime(new Date('2026-09-20T09:00:00'), now)).toBe('09-20')
    expect(relativeTime(new Date('2026-09-24T12:00:05'), now)).toBe('刚刚')
  })
})
