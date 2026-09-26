/**
 * [INPUT]: 依赖 vitest，依赖 ./format 的 formatMoney / formatCount / formatBytes / relativeTime / formatDateTime
 * [OUTPUT]: 对外提供 format.ts 的单元测试
 * [POS]: core/format 的单元测试：分转元、千分位、负数、币种符号；计数千分位；字节单位与 BigInt 安全；相对时间各档；绝对时间与无法解析的输入
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it } from 'vitest'
import { formatBytes, formatCount, formatDateTime, formatMoney, relativeTime } from './format'

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

describe('formatCount', () => {
  it('groups integers and truncates fractions', () => {
    expect(formatCount(0)).toBe('0')
    expect(formatCount(999)).toBe('999')
    expect(formatCount(12408)).toBe('12,408')
    expect(formatCount(-1234567)).toBe('-1,234,567')
    expect(formatCount(1234.9)).toBe('1,234')
  })
})

describe('formatBytes', () => {
  it('uses 1024 steps and three significant digits', () => {
    expect(formatBytes('0')).toBe('0 B')
    expect(formatBytes(1023)).toBe('1023 B')
    expect(formatBytes('1024')).toBe('1.00 KB')
    expect(formatBytes(String(540n * 1024n ** 3n))).toBe('540 GB')
    expect(formatBytes(String(1840n * 1024n ** 3n))).toBe('1.80 TB')
    expect(formatBytes((186n * 1024n ** 4n) / 10n)).toBe('18.6 TB')
    expect(formatBytes(-2048)).toBe('-2.00 KB')
  })

  it('stays exact above 2^53 (DASH-01 decimal strings)', () => {
    // 2^63-1：先转 number 会变成 9223372036854775808
    expect(formatBytes('9223372036854775807')).toBe('8.00 EB')
    // 10 PB − 1 字节：两位小数四舍五入进位到 10.00
    expect(formatBytes(String(1024n ** 5n * 10n - 1n))).toBe('10.00 PB')
  })

  it('rejects non-integer input like BigInt does', () => {
    expect(() => formatBytes('1.5')).toThrow()
  })
})

describe('formatDateTime', () => {
  it('formats in local time and passes through unparseable input', () => {
    expect(formatDateTime(new Date(2026, 8, 24, 8, 5))).toBe('2026-09-24 08:05')
    expect(formatDateTime('2026-09-24T08:05:00')).toBe('2026-09-24 08:05')
    expect(formatDateTime('not a date')).toBe('not a date')
  })
})
