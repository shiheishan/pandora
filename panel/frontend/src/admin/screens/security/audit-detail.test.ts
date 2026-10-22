import { describe, expect, it } from 'vitest'
import { auditDetail, detailKeyLabel, detailValue } from './audit-detail'

describe('auditDetail', () => {
  it('pairs before / after objects key by key and marks unchanged keys', () => {
    const d = auditDetail({ reason: '渠道维护', before: { enabled: true, accepting_new: true }, after: { enabled: true, accepting_new: false } })
    expect(d.changes).toEqual([
      { key: 'enabled', before: '是', after: '是', changed: false },
      { key: 'accepting_new', before: '是', after: '否', changed: true },
    ])
    expect(d.facts).toEqual([{ key: 'reason', value: '渠道维护' }])
  })

  it('compares scalar before / after as one value', () => {
    expect(auditDetail({ before: false, after: true }).changes).toEqual([{ key: '值', before: '否', after: '是', changed: true }])
  })

  it('pairs X_before / X_after, before_X / after_X and before_X with X', () => {
    const d = auditDetail({ subject_before: '旧标题', subject_after: '新标题', before_hmac: 'a', after_hmac: 'b', before_row_version: 3, row_version: 4, name: '香港 02' })
    expect(d.changes.map((c) => [c.key, c.before, c.after])).toEqual([
      ['subject', '旧标题', '新标题'],
      ['hmac', 'a', 'b'],
      ['row_version', '3', '4'],
    ])
    expect(d.facts).toEqual([{ key: 'name', value: '香港 02' }])
  })

  it('reads before + status as an account status change in Chinese', () => {
    const d = auditDetail({ before: 'active', status: 'suspended', sessions_revoked: true })
    expect(d.changes).toEqual([{ key: 'status', before: '正常', after: '已停用', changed: true }])
    expect(d.facts).toEqual([{ key: 'sessions_revoked', value: '是' }])
  })

  it('pairs a scalar before with enabled (feature switch)', () => {
    expect(auditDetail({ before: true, enabled: false, reason: '维护' }).changes).toEqual([{ key: 'enabled', before: '是', after: '否', changed: true }])
  })

  it('lists a one-sided before / after object key by key', () => {
    const d = auditDetail({ after: { name: '标准版', enabled: true } })
    expect(d.changes).toEqual([])
    expect(d.facts).toEqual([
      { key: 'after.name', value: '标准版' },
      { key: 'after.enabled', value: '是' },
    ])
    expect(detailKeyLabel('after.name')).toEqual({ text: '改后 · 名称', raw: false })
    expect(detailKeyLabel('before.site.name')).toEqual({ text: '改前 · site.name', raw: true })
  })

  it('pairs previous_end with period_end', () => {
    const d = auditDetail({ days: 7, previous_end: '2026-10-01T00:00:00Z', period_end: '2026-10-08T00:00:00Z' })
    expect(d.changes).toHaveLength(1)
    expect(d.changes[0]).toMatchObject({ key: 'period_end', changed: true })
    expect(d.facts).toEqual([{ key: 'days', value: '7' }])
  })

  it('leaves a lone before_X without its counterpart as a plain fact', () => {
    expect(auditDetail({ before_status: 'draft' })).toEqual({ changes: [], facts: [{ key: 'before_status', value: 'draft' }] })
  })

  it('is empty for an empty object', () => {
    expect(auditDetail({})).toEqual({ changes: [], facts: [] })
  })
})

describe('detailValue', () => {
  it('formats scalars, lists and objects', () => {
    expect(detailValue(null)).toBe('空')
    expect(detailValue('')).toBe('空')
    expect(detailValue(true)).toBe('是')
    expect(detailValue(12)).toBe('12')
    expect(detailValue(['IPLC', '游戏'])).toBe('IPLC、游戏')
    expect(detailValue([])).toBe('无')
    expect(detailValue({ a: 1 })).toBe('{"a":1}')
    expect(detailValue([{ a: 1 }])).toBe('[{"a":1}]')
  })

  it('translates login failure reasons and account statuses by key', () => {
    expect(detailValue('invalid_credentials', 'reason')).toBe('口令错误或账号不存在')
    // 账号状态只在 before + status 那一对里翻译，单独的 status 键（节点、订单等）原样
    expect(detailValue('active', 'status')).toBe('active')
  })

  it('shows ISO times in local time', () => {
    expect(detailValue('2026-10-21T08:30:00Z')).toMatch(/^2026-10-2\d \d{2}:\d{2}$/)
  })
})

describe('detailKeyLabel', () => {
  it('uses Chinese names for known keys and the raw key otherwise', () => {
    expect(detailKeyLabel('amount')).toEqual({ text: '金额', raw: false })
    expect(detailKeyLabel('site.name')).toEqual({ text: 'site.name', raw: true })
  })
})
