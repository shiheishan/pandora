import { describe, expect, it } from 'vitest'
import { periodOf, periodUnit, type Price } from '../common/catalog'
import { payReturnUrl, paySuccessText } from '../common/PayFlow'
import { makeNaming } from '../common/purchase'
import { rebuyQuery } from '../common/orders'
import { balanceSplit } from '../common/quote'
import { BASIC, GIB, ONCE, PLANS as BASE_PLANS, quote, row, split, STD, sub } from '../common/testing'
import { changeFormula, confirmCopy, payCopy, tierNote } from './copy'
import { defaultTier, intentOf, landingChoice, landingHref, orderRequest, parseTarget, quoteRequest, sortTiers } from './model'
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

describe('落点页：每人份数上限', () => {
  it('只有 new 一项时自动跳', () => {
    expect(landingChoice([{ mode: 'new' }])).toEqual({ auto: { mode: 'new' }, usable: [{ mode: 'new' }], blocked: [] })
  })

  it('new 与 renew 都可选时不自动跳', () => {
    expect(landingChoice([{ mode: 'new' }, { mode: 'renew', subscription_id: 'a' }])).toEqual({
      auto: null,
      usable: [{ mode: 'new' }, { mode: 'renew', subscription_id: 'a' }],
      blocked: [],
    })
  })

  it('new 置灰时不自动跳，usable 只有 renew', () => {
    expect(
      landingChoice([
        { mode: 'new', disabled_reason: '每人最多 1 份套餐' },
        { mode: 'renew', subscription_id: 'a' },
      ]),
    ).toEqual({
      auto: null,
      usable: [{ mode: 'renew', subscription_id: 'a' }],
      blocked: [{ mode: 'new', disabled_reason: '每人最多 1 份套餐' }],
    })
  })

  it('只有置灰的 new 时 usable 为空', () => {
    expect(landingChoice([{ mode: 'new', disabled_reason: '每人最多 1 份套餐' }])).toEqual({
      auto: null,
      usable: [],
      blocked: [{ mode: 'new', disabled_reason: '每人最多 1 份套餐' }],
    })
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
  // 订阅套餐的原份只按剩余付费时间算（Go proration.go）：流量两项恒为 0
  const credit = { paid: 3000, days_left: 24, days_total: 30, traffic_left: 0, traffic_total: 0, ratio_ppm: 800000 }

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
      '标准版这期付了 ¥30.00。还剩 24/30 天（80%），¥30.00 × 80% = ¥24.00。按下单那一刻算；送的天数不算钱。',
    )
    expect(c.notes[0]).toEqual({ b: '标准版这期没用完的 24 天，已经按 ¥24.00 算给你了', rest: expect.stringMatching(/^（先抵基础版的 ¥15\.00，多的 ¥9\.00 退到钱包）/) })
    expect(c.callout[0]).toMatch(/每期流量 100G → 50G、设备 /)
    expect(c.swap?.from.name).toBe('标准版')
    expect(c.swap?.to.name).toBe('基础版')
    expect(tierNote({ kind: 'change', subId: 'x', planId: 'basic' }, r)).toEqual(['¥15 − ¥24', '= 退 ¥9'])
    expect(changeFormula(row({ subtotal: 4500, credit: 2400, total: 2100 }))).toBe('¥45.00 − 原套餐没用完的 ¥24.00 = 今天付 ¥21.00')
  })

  it('换套餐：一次性原份取天数与流量里少的；永久的只看流量；流量用得快时提醒', () => {
    const once = sub({ id: 'o1', plan_id: 'once', plan_name: '流量版', plan_kind: 'one_time', usedGiB: 60, current_period_end: '2026-10-22T12:00:00Z' })
    const r = (detail: typeof credit, c: number) => row({ plan_id: 'std', price_id: 'std1', subtotal: 3000, credit: c, credit_detail: detail, total: 3000 - c })
    const args = (s: typeof once, rr: ReturnType<typeof row>) =>
      ({ target: { kind: 'change', subId: s.id, planId: 'std' }, sub: s, plan: STD, oldPlan: ONCE, row: rr, naming: makeNaming([s], undefined), held: [s], now: NOW }) as const
    const half = { paid: 2000, days_left: 15, days_total: 30, traffic_left: 40 * GIB, traffic_total: 100 * GIB, ratio_ppm: 400000 }
    const c = confirmCopy(args(once, r(half, 800)))
    expect(c.rows.find((x) => x.k === '原套餐没用完的部分')?.calc).toBe(
      '流量版付了 ¥20.00。还剩 15/30 天（50%），流量还剩 40G/100G（40%），按少的那项算：¥20.00 × 40% = ¥8.00。按下单那一刻算；送的天数和流量不算钱。',
    )
    expect(c.notes.some((n) => typeof n === 'string' && n.startsWith('你的流量用得比天数快'))).toBe(true)
    const forever = { ...once, permanent: true, current_period_end: null }
    const perm = { paid: 2000, days_left: 0, days_total: 0, traffic_left: 40 * GIB, traffic_total: 100 * GIB, ratio_ppm: 400000 }
    const p = confirmCopy(args(forever, r(perm, 800)))
    expect(p.rows.find((x) => x.k === '原套餐没用完的部分')?.calc).toBe(
      '流量版付了 ¥20.00。流量还剩 40G/100G（40%），¥20.00 × 40% = ¥8.00。按下单那一刻算；送的流量不算钱。',
    )
    expect(p.notes.some((n) => typeof n === 'string' && n.startsWith('你的流量用得比天数快'))).toBe(false)
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

  it('重置：清零已用，送的流量保留，能用满上限 + 送的', () => {
    const one = sub({ usedGiB: 110, bonus_bytes: 20 * GIB, reset_price: 1500, reset_limit: 2, resets_used: 1 })
    const c = confirmCopy({ target: { kind: 'reset', subId: one.id }, sub: one, plan: STD, row: row({ subtotal: 1500 }), naming: makeNaming([one], undefined), held: [one], now: NOW })
    expect(c.rows.find((x) => x.k === '重置后能用')?.v).toBe('10G → 120G')
    expect(c.item).toEqual({ name: '标准版', facts: '本期已用 110G / 120G', pill: '本期还能买 1 次' })
  })

  it('续费：剩的流量到期不累加', () => {
    const one = sub({ usedGiB: 92 })
    const c = confirmCopy({ target: { kind: 'renew', subId: one.id, fromPlans: true }, sub: one, plan: STD, row: row(), naming: makeNaming([one], undefined), held: [one], now: NOW })
    expect(c.notes[0]).toMatch(/^现在剩的 8G 用到 10月\d+日，到时不累加；10月\d+日 起新的一期，每期 100G。$/)
  })
})

describe('完成页', () => {
  it('上下文经地址带到收银台回跳之后', () => {
    const ctx = { kind: 'stack' as const, subId: 'a', was: null, oldPlan: null, refund: 0, waived: 0, revive: false, stackBytes: 100 * GIB }
    expect(readDone(new URLSearchParams(doneQuery(ctx)))).toEqual(ctx)
  })

  it('花了多少：余额、渠道、免掉的差价', () => {
    const pay = (amount: number, method?: string) => ({ status: 'succeeded', amount, created_at: '', method, provider_name: 'W9 演示收银（假商户）' })
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
    expect([periodUnit('3m'), periodUnit('once')]).toEqual(['/ 季', ''])
  })
})

describe('支付', () => {
  it('回跳地址相对入口页解析、带 paid=1', () => {
    expect(payReturnUrl('o1', 'https://panel.example.com/#/checkout?plan=x')).toBe('https://panel.example.com/#/orders/o1?paid=1')
  })

  it('成功文案按订单种类', () => {
    const base = { paid_amount: 0, total_amount: 5000 }
    expect(paySuccessText({ ...base, kind: 'upgrade' })).toBe('已换好，链接不变')
    expect(paySuccessText({ ...base, kind: 'topup' })).toBe('余额 +¥50.00')
  })
})

describe('改用余额付：按原单重下的确认页参数', () => {
  const item = (plan: string | null, price: string | null) => [{ name: 'x', quantity: 1, unit_amount: 100, line_amount: 100, plan_id: plan, price_id: price }]
  it('各种类按套餐、价格档、那一份回确认页；充值与缺信息的不给', () => {
    expect(rebuyQuery({ kind: 'new', subscription_id: null, items: item('p1', 'pr1') })).toEqual({ new: 'p1', price: 'pr1' })
    expect(rebuyQuery({ kind: 'renewal', subscription_id: 's1', items: item('p1', 'pr1') })).toEqual({ renew: 's1', price: 'pr1' })
    expect(rebuyQuery({ kind: 'stack', subscription_id: 's1', items: item('p1', 'pr1') })).toEqual({ stack: 's1', price: 'pr1' })
    expect(rebuyQuery({ kind: 'upgrade', subscription_id: 's1', items: item('p2', 'pr2') })).toEqual({ change: 's1', plan: 'p2', price: 'pr2' })
    expect(rebuyQuery({ kind: 'traffic_reset', subscription_id: 's1', items: item('p1', null) })).toEqual({ reset: 's1' })
    expect(rebuyQuery({ kind: 'topup', subscription_id: null, items: item(null, null) })).toBeNull()
    expect(rebuyQuery({ kind: 'new', subscription_id: null, items: [] })).toBeNull()
  })
})
