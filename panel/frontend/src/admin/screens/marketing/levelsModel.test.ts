import { describe, expect, it } from 'vitest'
import {
  buildOverrideBody,
  buildSettingsConfig,
  levelsSum,
  overrideSummary,
  settingsToForm,
  type SettingsForm,
} from './levelsModel'
import type { CommissionOverview } from './schemas'

const baseOverview = (): CommissionOverview => ({
  pending: 0,
  available: 0,
  paid_out: 0,
  this_month: 0,
  entries: 0,
  need_review: 0,
  waiting_withdrawals: 0,
  rate_percent: 10,
  freeze_days: 7,
  min_withdraw: 10050,
  total_earned: 0,
  invited_users: 0,
  scope: 'every_order',
  distribution_enabled: false,
  distribution_levels: [0, 0, 0],
  withdraw_enabled: true,
  withdraw_methods: ['alipay'],
})

const validForm = (): SettingsForm => ({
  rate: '20',
  freezeDays: '7',
  scope: 'every_order',
  distributionOn: true,
  l1: '50',
  l2: '30',
  l3: '20',
  withdrawOn: true,
  minWithdraw: '100.50',
  methods: ['wechat', 'alipay'],
})

describe('settingsToForm', () => {
  it('正确从 overview 映射到表单字段', () => {
    const f = settingsToForm(baseOverview())
    expect(f).toEqual({
      rate: '10',
      freezeDays: '7',
      scope: 'every_order',
      distributionOn: false,
      l1: '0',
      l2: '0',
      l3: '0',
      withdrawOn: true,
      minWithdraw: '100.50',
      methods: ['alipay'],
    })
  })
})

describe('buildSettingsConfig', () => {
  it('合法时输出全部 10 个字段且 methods 按目录排序', () => {
    const r = buildSettingsConfig(validForm())
    expect(r).toEqual({
      ok: true,
      body: {
        rate_percent: 20,
        freeze_days: 7,
        min_withdraw: 10050,
        scope: 'every_order',
        distribution_enabled: true,
        distribution_l1: 50,
        distribution_l2: 30,
        distribution_l3: 20,
        withdraw_enabled: true,
        withdraw_methods: ['alipay', 'wechat'],
      },
    })
  })

  it('rate_percent 校验', () => {
    expect(buildSettingsConfig({ ...validForm(), rate: '51' })).toEqual({
      ok: false,
      errors: { rate_percent: '佣金比例需在 0 到 50 之间' },
    })
    expect(buildSettingsConfig({ ...validForm(), rate: '-1' })).toEqual({
      ok: false,
      errors: { rate_percent: '佣金比例需在 0 到 50 之间' },
    })
    expect(buildSettingsConfig({ ...validForm(), rate: 'abc' })).toEqual({
      ok: false,
      errors: { rate_percent: '佣金比例需在 0 到 50 之间' },
    })
    expect(buildSettingsConfig({ ...validForm(), rate: '0' }).ok).toBe(true)
  })

  it('freeze_days 校验', () => {
    expect(buildSettingsConfig({ ...validForm(), freezeDays: '91' })).toEqual({
      ok: false,
      errors: { freeze_days: '冻结天数需在 0 到 90 之间' },
    })
    expect(buildSettingsConfig({ ...validForm(), freezeDays: '-1' })).toEqual({
      ok: false,
      errors: { freeze_days: '冻结天数需在 0 到 90 之间' },
    })
    expect(buildSettingsConfig({ ...validForm(), freezeDays: '0' }).ok).toBe(true)
  })

  it('min_withdraw 校验', () => {
    expect(buildSettingsConfig({ ...validForm(), minWithdraw: '10.555' })).toEqual({
      ok: false,
      errors: { min_withdraw: '填金额，最多两位小数' },
    })
    expect(buildSettingsConfig({ ...validForm(), minWithdraw: 'invalid' })).toEqual({
      ok: false,
      errors: { min_withdraw: '填金额，最多两位小数' },
    })
  })

  it('三级之和超 100', () => {
    const r = buildSettingsConfig({ ...validForm(), l1: '60', l2: '30', l3: '20' })
    expect(r.ok).toBe(false)
    if (!r.ok) expect(r.errors.distribution_l1).toBe('三级比例之和不能超过 100')
  })

  it('开启分销且三级全 0', () => {
    const r = buildSettingsConfig({ ...validForm(), distributionOn: true, l1: '0', l2: '0', l3: '0' })
    expect(r.ok).toBe(false)
    if (!r.ok) expect(r.errors.distribution_l1).toBe('开启多级分销时至少要给一级设比例')
  })

  it('关闭分销且三级全 0 合法', () => {
    const r = buildSettingsConfig({ ...validForm(), distributionOn: false, l1: '0', l2: '0', l3: '0', methods: ['alipay'] })
    expect(r.ok).toBe(true)
  })

  it('非整数比例', () => {
    const r = buildSettingsConfig({ ...validForm(), l2: '10.5' })
    expect(r.ok).toBe(false)
    if (!r.ok) expect(r.errors.distribution_l2).toBe('比例需在 0 到 100 之间')
  })

  it('开着提现且无方式', () => {
    const r = buildSettingsConfig({ ...validForm(), withdrawOn: true, methods: [] })
    expect(r.ok).toBe(false)
    if (!r.ok) expect(r.errors.withdraw_methods).toBe('开着提现时至少要选一种提现方式')
  })

  it('关闭提现也至少要选一种提现方式', () => {
    const r = buildSettingsConfig({ ...validForm(), withdrawOn: false, methods: [] })
    expect(r.ok).toBe(false)
    if (!r.ok) expect(r.errors.withdraw_methods).toBe('关着提现时也至少要选一种提现方式，重新打开提现时直接生效')
    expect(buildSettingsConfig({ ...validForm(), withdrawOn: false, methods: ['alipay'] }).ok).toBe(true)
  })
})

describe('levelsSum', () => {
  it('非法项当 0', () => {
    expect(levelsSum({ ...validForm(), l1: '10', l2: 'x', l3: '5' })).toBe(15)
  })
})

describe('buildOverrideBody', () => {
  it('只给 rate', () => {
    const r = buildOverrideBody({ email: 'a@example.test', rate: '15', scope: '' })
    expect(r).toEqual({ ok: true, body: { email: 'a@example.test', rate_percent: 15, scope: null } })
  })

  it('只给 scope', () => {
    const r = buildOverrideBody({ email: 'b@example.test', rate: '', scope: 'first_order' })
    expect(r).toEqual({ ok: true, body: { email: 'b@example.test', rate_percent: null, scope: 'first_order' } })
  })

  it('rate 为 0 发 0', () => {
    const r = buildOverrideBody({ email: 'c@example.test', rate: '0', scope: 'every_order' })
    expect(r).toEqual({ ok: true, body: { email: 'c@example.test', rate_percent: 0, scope: 'every_order' } })
  })

  it('都空', () => {
    const r = buildOverrideBody({ email: 'd@example.test', rate: '', scope: '' })
    expect(r.ok).toBe(false)
    if (!r.ok) expect(r.errors.rate_percent).toBe('比例与计佣范围至少设一项')
  })

  it('rate 51', () => {
    const r = buildOverrideBody({ email: 'e@example.test', rate: '51', scope: 'first_order' })
    expect(r.ok).toBe(false)
    if (!r.ok) expect(r.errors.rate_percent).toBe('佣金比例需在 0 到 50 之间')
  })

  it('邮箱空', () => {
    const r = buildOverrideBody({ email: '  ', rate: '10', scope: '' })
    expect(r.ok).toBe(false)
    if (!r.ok) expect(r.errors.email).toBe('请填写用户邮箱')
  })
})

describe('overrideSummary', () => {
  it('四种组合', () => {
    expect(overrideSummary({ rate_percent: 15, scope: 'first_order' })).toBe('比例 15% · 仅首单')
    expect(overrideSummary({ rate_percent: null, scope: 'every_order' })).toBe('比例跟全局 · 每笔订单')
    expect(overrideSummary({ rate_percent: 0, scope: null })).toBe('不拿佣金 · 范围跟全局')
    expect(overrideSummary({ rate_percent: 10, scope: null })).toBe('比例 10% · 范围跟全局')
  })
})
