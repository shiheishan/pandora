import { describe, expect, it } from 'vitest'
import { approveReasonError, buildCommissionConfig, commissionStats, commissionToForm, freezeLabel, voidReasonError, withdrawalState } from './commissionModel'
import { entryReviewResponse, overviewSchema, reviewEntriesResponse, type CommissionOverview } from './schemas'

describe('commission', () => {
  const overview: CommissionOverview = overviewSchema.parse({
    pending: 390400,
    available: 1,
    paid_out: 3166000,
    this_month: 0,
    entries: 0,
    need_review: 0,
    waiting_withdrawals: 0,
    rate_percent: 20,
    freeze_days: 7,
    min_withdraw: 10050,
    total_earned: 4821000,
    invited_users: 2318,
    scope: 'every_order',
    distribution_enabled: false,
    distribution_levels: [0, 0, 0],
    withdraw_enabled: true,
    withdraw_methods: ['alipay'],
  })

  it('maps withdrawal statuses; only approved can be paid', () => {
    expect(withdrawalState('requested')).toMatchObject({ label: '待审核', canReview: true, canPay: false })
    expect(withdrawalState('reviewing').canReview).toBe(true)
    expect(withdrawalState('approved')).toMatchObject({ label: '待打款', canPay: true })
    expect(withdrawalState('processing')).toMatchObject({ canReview: false, canPay: false })
    expect(withdrawalState('paid').label).toBe('已打款')
    expect(withdrawalState('failed').label).toBe('打款失败')
    expect(withdrawalState('returned').label).toBe('已退回')
  })

  it('formats the four overview stats', () => {
    expect(commissionStats(overview).map((s) => s.value)).toEqual(['¥48,210.00', '¥3,904.00', '¥31,660.00', '2,318'])
  })

  it('requires the backend-reported totals and scope', () => {
    const legacy: Partial<CommissionOverview> = { ...overview }
    delete legacy.scope
    expect(overviewSchema.safeParse(legacy).success).toBe(false)
  })

  it('round-trips the settings form into the config body with scope', () => {
    const form = commissionToForm(overview)
    expect(form).toEqual({ rate: 20, scope: 'every_order', freezeDays: '7', minWithdraw: '100.50' })
    expect(buildCommissionConfig(form)).toEqual({ ok: true, body: { rate_percent: 20, freeze_days: 7, min_withdraw: 10050, scope: 'every_order' } })
    expect(buildCommissionConfig({ ...form, scope: 'first_order' })).toEqual({ ok: true, body: { rate_percent: 20, freeze_days: 7, min_withdraw: 10050, scope: 'first_order' } })
    const bad = buildCommissionConfig({ rate: 51, scope: 'every_order', freezeDays: '91', minWithdraw: 'x' })
    expect(!bad.ok && Object.keys(bad.errors).sort()).toEqual(['freeze_days', 'min_withdraw', 'rate_percent'])
  })

  describe('review entries', () => {
    const iso = '2026-10-01T12:00:00Z'
    const row = {
      id: 'a0000000-0000-4000-8000-000000000001',
      order_no: 'ORD-1',
      referrer_email: 'ref@example.com',
      referee_email: 'buy@example.com',
      amount: 1234,
      currency: 'CNY',
      review_reason: '同源嫌疑',
      review_source: 'same_source',
      created_at: iso,
      frozen_until: iso,
    }

    it('parses review list and entry review responses', () => {
      const list = reviewEntriesResponse.parse({
        entries: [row, { ...row, id: 'a0000000-0000-4000-8000-000000000002', order_no: 'ORD-2' }],
        next_cursor: 'abc',
      })
      expect(list.entries).toHaveLength(2)
      expect(list.next_cursor).toBe('abc')
      expect(reviewEntriesResponse.safeParse({ entries: [{ ...row, amount: 12.5 }], next_cursor: '' }).success).toBe(false)
      expect(reviewEntriesResponse.safeParse({ entries: [{ ...row, referee_email: undefined }], next_cursor: '' }).success).toBe(false)
      expect(reviewEntriesResponse.safeParse({ entries: [{ ...row, review_source: 'mystery' }], next_cursor: '' }).success).toBe(false)
      expect(entryReviewResponse.parse({ status: 'pending' }).status).toBe('pending')
      expect(entryReviewResponse.parse({ status: 'voided' }).status).toBe('voided')
      expect(entryReviewResponse.safeParse({ status: 'approved' }).success).toBe(false)
    })

    it('validates void reason length by character count', () => {
      expect(voidReasonError('')).toBe('作废必须填写原因，至少 5 个字')
      expect(voidReasonError('   ')).toBe('作废必须填写原因，至少 5 个字')
      expect(voidReasonError('四个字啊')).toBe('作废必须填写原因，至少 5 个字')
      expect(voidReasonError('  abcd  ')).toBe('作废必须填写原因，至少 5 个字')
      expect(voidReasonError('同源刷单确认')).toBeNull()
      expect(voidReasonError('abcde')).toBeNull()
      expect(voidReasonError('长'.repeat(501))).toBe('原因最多 500 个字')
      expect(voidReasonError('长'.repeat(500))).toBeNull()
    })

    it('lets the approve reason be blank but caps it at 500 characters', () => {
      expect(approveReasonError('')).toBeNull()
      expect(approveReasonError('  核实无误  ')).toBeNull()
      expect(approveReasonError('长'.repeat(500))).toBeNull()
      expect(approveReasonError('长'.repeat(501))).toBe('原因最多 500 个字')
    })

    it('labels freeze period relative to now', () => {
      const before = new Date('2026-12-01T00:00:00Z')
      const after = new Date('2025-07-01T00:00:00Z')
      expect(freezeLabel({ frozen_until: '2026-12-31T12:00:00Z', review_source: 'same_source' }, before)).toContain('冻结至')
      expect(freezeLabel({ frozen_until: '2025-06-01T00:00:00Z', review_source: 'source_check' }, after)).toBe('冻结期已过，通过后下一轮自动解冻')
      // 余额不足转来的：不论冻结期，都不说「会自动解冻」
      for (const at of [before, after]) {
        const label = freezeLabel({ frozen_until: '2025-06-01T00:00:00Z', review_source: 'underfunded' }, at)
        expect(label).toContain('余额不足')
        expect(label).not.toContain('自动解冻')
      }
      // 受益人已注销：只能作废
      expect(freezeLabel({ frozen_until: '2025-06-01T00:00:00Z', review_source: 'account_deleted' }, after)).toContain('只能作废')
    })
  })
})
