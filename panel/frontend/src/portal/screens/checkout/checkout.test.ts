import { describe, expect, it } from 'vitest'
import { monthlyNote, periodName, periodOf, periodUnit, savingAmount, type Price } from '../common/catalog'
import { payReturnUrl, paySuccessText } from '../common/PayFlow'
import { makeNaming } from '../common/purchase'
import { balanceSplit } from '../common/quote'
import { BASIC, GIB, ONCE, PLANS as BASE_PLANS, quote, row, split, STD, sub } from '../common/testing'
import { changeFormula, confirmCopy, payCopy, tierNote } from './copy'
import { defaultTier, intentOf, landingHref, orderRequest, parseTarget, quoteRequest, sortTiers } from './model'
import { doneLines, doneQuery, doneSub, paidText, readDone } from './result-copy'

const price = (id: string, unit_amount: number, billing_interval: Price['billing_interval'], interval_count = 1, validity_days: number | null = null): Price => ({
  id,
  unit_amount,
  billing_interval,
  interval_count,
  validity_days,
})
const PLANS = [...BASE_PLANS, ONCE]
const q = (s: string) => new URLSearchParams(s)
const NOW = new Date('2026-10-07T12:00:00Z')

const MINE = sub({ id: 'a', label: '我的', usedGiB: 93, current_period_end: '2026-10-30T12:00:00Z' })
const MOM = sub({ id: 'b', label: '妈妈的 iPad', plan_id: 'basic', plan_name: '基础版', usedGiB: 12, capGiB: 50, current_period_end: '2026-10-12T12:00:00Z' })
const STACKABLE = sub({ id: 's1', stackable: true, plan_id: 'once', plan_name: '流量版', plan_kind: 'one_time', usedGiB: 10, capGiB: 100 })

describe('确认页地址（约定地址）', () => {
  const one = [sub()]
  const two = [MINE, MOM]
  const parse = (s: string, held = one) => parseTarget(q(s), held, PLANS)

  it('续费、换套餐、新买、叠加、重置', () => {
    expect(parse('renew=sub1')).toEqual({ target: { kind: 'renew', subId: 'sub1', fromPlans: false } })
    expect(parse('renew=sub1&from=plans')).toEqual({ target: { kind: 'renew', subId: 'sub1', fromPlans: true } })
    expect(parse('change=sub1&plan=pro')).toEqual({ target: { kind: 'change', subId: 'sub1', planId: 'pro' } })
    expect(parse('reset=sub1')).toEqual({ target: { kind: 'reset', subId: 'sub1' } })
    expect(parse('new=pro')).toEqual({ target: { kind: 'new', planId: 'pro' } })
    expect(parse('stack=s1', [STACKABLE])).toEqual({ target: { kind: 'stack', subId: 's1' } })
  })

  it('?plan= 与 ?change-plan=：无份新买，有份先选放到哪里', () => {
    expect(parse('plan=pro', [])).toEqual({ target: { kind: 'new', planId: 'pro' } })
    expect(parse('plan=std')).toEqual({ target: { kind: 'choose', planId: 'std' } })
    expect(parse('change-plan=pro', two)).toEqual({ target: { kind: 'choose', planId: 'pro' } })
  })

  it('landingHref 四种模式', () => {
    expect(landingHref('std', { mode: 'new' }, null)).toBe('#/checkout?new=std')
    expect(landingHref('std', { mode: 'renew', subscription_id: 'a' }, 'std1')).toBe('#/checkout?renew=a&price=std1')
    expect(landingHref('once', { mode: 'stack', subscription_id: 's1' }, null)).toBe('#/checkout?stack=s1')
    expect(landingHref('pro', { mode: 'change', subscription_id: 'b' }, null)).toBe('#/checkout?change=b&plan=pro')
  })

  it('异常地址', () => {
    expect(parse('')).toEqual({ problem: 'missing' })
    expect(parse('renew=nope')).toEqual({ problem: 'sub_gone' })
    expect(parse('change=sub1&plan=std')).toEqual({ problem: 'sub_gone' })
    expect(parse('new=gone')).toEqual({ problem: 'plan_gone' })
    expect(parse('stack=sub1')).toEqual({ problem: 'sub_gone' })
    expect(parse('stack=s1', [sub({ stackable: false })])).toEqual({ problem: 'sub_gone' })
  })
})

describe('报价与建单', () => {
  it('报价请求与 choose', () => {
    expect(quoteRequest({ kind: 'renew', subId: 'a', fromPlans: false }, [MINE], 'X')).toEqual({ action: 'renew', subscription_id: 'a', coupon_code: 'X' })
    expect(quoteRequest({ kind: 'change', subId: 'a', planId: 'pro' }, [MINE], null)).toEqual({ action: 'change', subscription_id: 'a', plan_id: 'pro' })
    expect(quoteRequest({ kind: 'new', planId: 'std' }, [MINE], null)).toEqual({ action: 'new', plan_id: 'std', new_copy: true })
    expect(quoteRequest({ kind: 'stack', subId: 's1' }, [STACKABLE], null)).toEqual({ action: 'stack', subscription_id: 's1' })
    expect(quoteRequest({ kind: 'choose', planId: 'std' }, [], null)).toBeNull()
    expect(quoteRequest({ kind: 'reset', subId: 'a' }, [MINE], null)).toEqual({ action: 'reset', subscription_id: 'a' })
  })

  it('买多久：按时长排；默认点名的 → 续费原价格 → 同样长 → 第一档', () => {
    const tiers = [row({ price_id: 'std12', interval: 'year' }), row({ price_id: 'std3', interval_count: 3 }), row({ price_id: 'std1' })]
    expect(sortTiers(tiers).map((r) => r.price_id)).toEqual(['std1', 'std3', 'std12'])
    expect(defaultTier(tiers, sub(), 'std12')?.price_id).toBe('std12')
    expect(defaultTier(tiers, sub(), null)?.price_id).toBe('std1')
    const quarterly = sub({ renewal_price: { id: 'old', unit_amount: 1, billing_interval: 'month', interval_count: 3, available: false } })
    expect(defaultTier(tiers, quarterly, null)?.price_id).toBe('std3')
  })

  it('建单带 as_of 与 expect；stack 路径', () => {
    const r = row({ with_balance: split({ applied: 1200, payable: 1800 }) })
    const qt = quote([r], { balance: 1200 })
    const renew = orderRequest({ target: { kind: 'renew', subId: 'a', fromPlans: false }, quote: qt, row: r, split: r.with_balance, coupon: null })
    expect(renew).toEqual({ path: 'v1/me/subscriptions/a/renew', body: { price_id: 'std1', as_of: qt.as_of, expect: { total: 3000, balance_applied: 1200, payable: 1800 }, use_balance: 1200 } })
    const stack = orderRequest({ target: { kind: 'stack', subId: 's1' }, quote: qt, row: r, split: r.without_balance, coupon: null })
    expect(stack).toEqual({ path: 'v1/me/subscriptions/s1/stack', body: { price_id: 'std1', as_of: qt.as_of, expect: { total: 3000, balance_applied: 0, payable: 3000 } } })
    expect(intentOf(renew)).toEqual({ path: renew.path, body: { price_id: 'std1', use_balance: 1200 } })
  })

  it('余额开关只在两组数之间切换；Forced 时只能用余额', () => {
    const r = row({ with_balance: split({ applied: 1200, payable: 1800 }), without_balance: split({ payable: 3000 }) })
    expect(balanceSplit(r, true).applied).toBe(1200)
    expect(balanceSplit(r, false).payable).toBe(3000)
    const forced = row({ total: 30, with_balance: split({ applied: 30, forced: true }), without_balance: split({ payable: 30 }) })
    expect(balanceSplit(forced, false)).toEqual(forced.with_balance)
  })
})

describe('确认页的钱（原型 S7 / S7b / S7c 与最低额）', () => {
  const methods = '支付宝、微信支付'
  it('S7：余额抵一部分；关掉后写有多少、现在没用', () => {
    const r = row({ with_balance: split({ applied: 1200, payable: 1800 }), without_balance: split({ payable: 3000 }) })
    const qt = quote([r], { balance: 1200 })
    const on = payCopy(qt, r, r.with_balance, true, '续费', methods)
    expect(on.balanceLine).toEqual({ on: true, locked: false, strong: '已用余额 ¥12.00', text: '（可关掉）' })
    expect(on.sum).toEqual({ text: '余额抵 ¥12.00，还需支付 ', strong: '¥18.00' })
    expect(on.button).toBe('续费，付 ¥18.00')
  })

  it('不用付钱、还退余额', () => {
    const r = row({ total: 0, refund: 900, with_balance: split(), without_balance: split() })
    expect(payCopy(quote([r], { balance: 0 }), r, r.with_balance, true, '换成基础版', methods)).toMatchObject({
      balanceLine: null,
      sum: { text: '这次不用付钱', after: '，多出的 ¥9.00 退到钱包余额' },
      button: '换成基础版',
    })
  })
})

describe('确认页的文字', () => {
  const s4 = sub({ usedGiB: 20, current_period_end: '2026-11-01T12:00:00Z' })
  const naming = makeNaming([s4], undefined)
  const credit = { paid: 3000, days_left: 25, days_total: 30, traffic_left: 80 * GIB, traffic_total: 100 * GIB, ratio_ppm: 800000 }

  it('换便宜：算式与明细', () => {
    const r = row({
      plan_id: 'basic',
      price_id: 'basic1',
      subtotal: 1500,
      credit: 2400,
      credit_detail: credit,
      total: 0,
      refund: 900,
      period_end: '2026-11-07T12:00:00Z',
      previous_end: '2026-11-01T12:00:00Z',
    })
    const c = confirmCopy({ target: { kind: 'change', subId: s4.id, planId: 'basic' }, sub: s4, plan: BASIC, oldPlan: STD, row: r, naming, held: [s4], now: NOW })
    expect(c.formula).toBe('¥15.00 − 原套餐没用完的 ¥24.00 = 今天不用付，退 ¥9.00 到钱包余额')
    expect(c.rows.find((x) => x.k === '原套餐没用完的部分')?.calc).toBe(
      '标准版这期付了 ¥30.00。还剩 25/30 天（83%），流量还剩 80G/100G（80%），按少的那项算：¥30.00 × 80% = ¥24.00。按下单那一刻算；送的天数不算钱。',
    )
    expect(tierNote({ kind: 'change', subId: 'x', planId: 'basic' }, r)).toEqual(['¥15 − ¥24', '= 退 ¥9'])
    expect(changeFormula(row({ subtotal: 4500, credit: 2400, total: 2100 }))).toBe('¥45.00 − 原套餐没用完的 ¥24.00 = 今天付 ¥21.00')
  })

  it('叠加：总流量与有效期', () => {
    const r = row({ validity_days: 30, period_end: '2026-11-19T12:00:00Z', subtotal: 2000 })
    const c = confirmCopy({
      target: { kind: 'stack', subId: STACKABLE.id },
      sub: STACKABLE,
      plan: ONCE,
      row: r,
      naming: makeNaming([STACKABLE], undefined),
      held: [STACKABLE],
      now: NOW,
    })
    expect(c.rows.find((x) => x.k === '总流量')?.v).toBe('剩 90G → 加完 190G')
    expect(c.rows.find((x) => x.k === '有效期')?.v).toMatch(/→/)
  })

  it('续费：剩的流量到期不累加', () => {
    const one = sub({ usedGiB: 92 })
    const c = confirmCopy({ target: { kind: 'renew', subId: one.id, fromPlans: true }, sub: one, plan: STD, row: row(), naming: makeNaming([one], undefined), held: [one], now: NOW })
    expect(c.notes[0]).toMatch(/^现在剩的 8G 用到 10月\d+日，到时不累加；10月\d+日 起新的一期，每月 100G。$/)
  })
})

describe('完成页', () => {
  it('上下文经地址带到收银台回跳之后', () => {
    const ctx = { kind: 'stack' as const, subId: 'a', was: null, oldPlan: null, refund: 0, waived: 0, revive: false, stackBytes: 100 * GIB }
    expect(readDone(new URLSearchParams(doneQuery(ctx)))).toEqual(ctx)
  })

  it('花了多少：余额、渠道、免掉的差价', () => {
    const pay = (amount: number, method?: string) => ({ status: 'succeeded', amount, currency: 'CNY', created_at: '', method, provider_name: 'W9 演示收银（假商户）' })
    expect(paidText({ balance_applied: 850, payments: [pay(950, 'alipay')] })).toBe('余额付了 ¥8.50，支付宝付了 ¥9.50')
    expect(paidText({ balance_applied: 0, payments: [] }, 30)).toBe('零头 ¥0.30 已免')
  })

  it('落到的那一份按订单详情的 subscription_id 认', () => {
    const old = sub({ id: 'o', current_period_end: '2026-11-07T12:00:00Z' })
    const fresh = sub({ id: 'n', current_period_end: '2026-11-07T12:00:00Z' })
    expect(doneSub({ subscription_id: 'n' }, { kind: 'new', subId: null }, [old, fresh])?.id).toBe('n')
    expect(doneSub({ subscription_id: null }, { kind: 'stack', subId: 'o' }, [old, fresh])?.id).toBe('o')
  })

  it('续费好了', () => {
    const o = { balance_applied: 0, paid_amount: 3000, payments: [], plan_name: '标准版' } as never
    const s = sub({ current_period_end: '2026-11-19T12:00:00Z' })
    const lines = doneLines(
      { kind: 'renew', subId: s.id, was: '2026-10-19T12:00:00Z', oldPlan: null, refund: 0, waived: 0, revive: false, stackBytes: 0 },
      o,
      s,
      STD,
      [s],
      makeNaming([s], undefined),
      null,
    )
    expect(lines.title).toBe('续费好了')
    expect(lines.showNewLink).toBe(false)
  })
})

describe('目录文案', () => {
  it('价格档归档与文案', () => {
    expect([periodOf(price('a', 1, 'month', 3)), periodOf(price('b', 1, 'year')), periodOf(price('c', 1, 'one_time'))]).toEqual(['3m', '12m', 'once'])
    expect([periodName('12m'), periodName('once'), periodName('6m')]).toEqual(['年付', '一次性', '半年付'])
    expect([periodUnit('3m'), periodUnit('once')]).toEqual(['/ 季', ''])
  })

  it('折合月价与省额', () => {
    expect(monthlyNote(price('m', 2900, 'month'))).toBe('按月付费')
    expect(monthlyNote(price('y', 29900, 'year'))).toBe('折合 ¥24.92 / 月')
    expect(savingAmount(STD, STD.prices[2]!)).toBe(3000 * 12 - 30000)
  })
})

describe('支付', () => {
  it('回跳地址相对入口页解析、带 paid=1', () => {
    expect(payReturnUrl('o1', 'https://panel.example.com/#/checkout?plan=x')).toBe('https://panel.example.com/#/orders/o1?paid=1')
  })

  it('成功文案按订单种类', () => {
    const base = { paid_amount: 0, total_amount: 5000, currency: 'CNY' }
    expect(paySuccessText({ ...base, kind: 'upgrade' })).toBe('已换好，链接不变')
    expect(paySuccessText({ ...base, kind: 'topup' })).toBe('余额 +¥50.00')
  })
})
