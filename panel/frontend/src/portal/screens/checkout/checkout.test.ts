import { describe, expect, it } from 'vitest'
import { monthlyNote, periodName, periodOf, periodUnit, perGbNote, quotaPeriodNote, resetNote, savingAmount, savingPercent, type Pack, type Plan, type Price } from '../common/catalog'
import { payReturnUrl, paySuccessText } from '../common/PayFlow'
import { makeNaming } from '../common/purchase'
import { balanceSplit } from '../common/quote'
import { BASIC, GIB, PACK100, PLANS, quote, row, split, STD, sub } from '../common/testing'
import { changeFormula, confirmCopy, payCopy, tierNote } from './copy'
import { defaultTier, intentOf, orderRequest, parseTarget, quoteRequest, sortTiers } from './model'
import { doneLines, doneQuery, findNewSub, paidText, readDone } from './result-copy'

const price = (id: string, unit_amount: number, billing_interval: Price['billing_interval'], interval_count = 1, currency = 'CNY'): Price => ({ id, currency, unit_amount, billing_interval, interval_count, trial_days: 0 })
const OLD_STD: Plan = { ...STD, prices: [price('s1', 2900, 'month'), price('s3', 7900, 'quarter'), price('s12', 29900, 'year'), price('su', 499, 'month', 1, 'USD')], quota_reset_strategy: 'natural_month', quotas: [{ metric: 'traffic.bytes', limit: 200 * GIB, unit: 'bytes', period: 'cycle' }] }
const OLD_PRO: Plan = { ...OLD_STD, id: 'pro', name: '专业版', prices: [price('p1', 5900, 'month'), price('p3', 15900, 'month', 3), price('p12', 59900, 'month', 12)] }
const PACK: Pack = { id: 'k', name: '500 GB', traffic_bytes: 500 * GIB, currency: 'CNY', unit_amount: 9900, recommended: true }
const q = (s: string) => new URLSearchParams(s)
const NOW = new Date('2026-10-07T12:00:00Z')

// 小王两份：「我的」标准版（流量快用完）、「妈妈的 iPad」基础版
const MINE = sub({ id: 'a', label: '我的', usedGiB: 93, current_period_end: '2026-10-30T12:00:00Z' })
const MOM = sub({ id: 'b', label: '妈妈的 iPad', plan_id: 'basic', plan_name: '基础版', usedGiB: 12, capGiB: 50, current_period_end: '2026-10-12T12:00:00Z' })

describe('确认页地址（约定地址）', () => {
  const one = [sub()]
  const two = [MINE, MOM]
  const parse = (s: string, held = one) => parseTarget(q(s), held, PLANS, [PACK100])

  it('续费、换套餐、新买、加流量各自的地址', () => {
    expect(parse('renew=sub1')).toEqual({ target: { kind: 'renew', subId: 'sub1', fromPlans: false } })
    expect(parse('renew=sub1&from=plans')).toEqual({ target: { kind: 'renew', subId: 'sub1', fromPlans: true } })
    expect(parse('change=sub1&plan=pro')).toEqual({ target: { kind: 'change', subId: 'sub1', planId: 'pro', chooser: false } })
    expect(parse('new=pro')).toEqual({ target: { kind: 'new', planId: 'pro' } })
    expect(parse('pack=k100&sub=sub1')).toEqual({ target: { kind: 'pack', packId: 'k100', subId: 'sub1', chooser: false } })
  })

  it('换掉哪一份：能换的只有一份时直接定；多份不预选，选过带 sub', () => {
    expect(parse('change-plan=pro')).toEqual({ target: { kind: 'change', subId: 'sub1', planId: 'pro', chooser: false } })
    expect(parse('change-plan=pro', two)).toEqual({ target: { kind: 'change', subId: null, planId: 'pro', chooser: true } })
    expect(parse('change-plan=pro&sub=b', two)).toEqual({ target: { kind: 'change', subId: 'b', planId: 'pro', chooser: true } })
    // 两份里只有基础版能换成标准版（另一份就是标准版）：直接定
    expect(parse('change-plan=std', two)).toEqual({ target: { kind: 'change', subId: 'b', planId: 'std', chooser: false } })
  })

  it('老地址 ?plan=：没有套餐新买，有同款续费，否则换套餐', () => {
    expect(parse('plan=pro', [])).toEqual({ target: { kind: 'new', planId: 'pro' } })
    expect(parse('plan=std')).toEqual({ target: { kind: 'renew', subId: 'sub1', fromPlans: true } })
    expect(parse('plan=pro')).toEqual({ target: { kind: 'change', subId: 'sub1', planId: 'pro', chooser: false } })
    expect(parse('plan=std', two)).toEqual({ target: { kind: 'renew', subId: 'a', fromPlans: true } })
  })

  it('加流量没带是哪一份：多份时预选剩得最少的并显示选择', () => {
    expect(parse('pack=k100', two)).toEqual({ target: { kind: 'pack', packId: 'k100', subId: 'a', chooser: true } })
    expect(parse('pack=k100&sub=b&pick=1', two)).toEqual({ target: { kind: 'pack', packId: 'k100', subId: 'b', chooser: true } })
    expect(parse('pack=k100')).toEqual({ target: { kind: 'pack', packId: 'k100', subId: 'sub1', chooser: false } })
  })

  it('异常地址', () => {
    expect(parse('')).toEqual({ problem: 'missing' })
    expect(parse('renew=nope')).toEqual({ problem: 'sub_gone' })
    expect(parse('change=sub1&plan=std')).toEqual({ problem: 'sub_gone' })
    expect(parse('new=gone')).toEqual({ problem: 'plan_gone' })
    expect(parse('pack=gone')).toEqual({ problem: 'pack_gone' })
    expect(parse('pack=k100', [sub({ status: 'expired' })])).toEqual({ problem: 'sub_gone' })
    expect(parse('pack=k100&sub=nope', [MINE, MOM])).toEqual({ problem: 'sub_gone' })
  })
})

describe('报价与建单', () => {
  it('报价请求：换套餐没选是哪一份时按套餐展开；新买在已有套餐时是另买一份', () => {
    expect(quoteRequest({ kind: 'renew', subId: 'a', fromPlans: false }, [MINE], 'X')).toEqual({ action: 'renew', subscription_id: 'a', coupon_code: 'X' })
    expect(quoteRequest({ kind: 'change', subId: null, planId: 'pro', chooser: true }, [MINE], null)).toEqual({ action: 'change', plan_id: 'pro' })
    expect(quoteRequest({ kind: 'new', planId: 'std' }, [MINE], null)).toEqual({ action: 'new', plan_id: 'std', new_copy: true })
    expect(quoteRequest({ kind: 'new', planId: 'std' }, [], null)).toEqual({ action: 'new', plan_id: 'std', new_copy: false })
    expect(quoteRequest({ kind: 'pack', packId: 'k', subId: 'a', chooser: false }, [MINE], null)).toEqual({ action: 'pack', pack_id: 'k', subscription_id: 'a' })
  })

  it('买多久：按时长排；默认点名的 → 续费原价格 → 同样长 → 第一档', () => {
    const tiers = [row({ price_id: 'std12', interval: 'year' }), row({ price_id: 'std3', interval_count: 3 }), row({ price_id: 'std1' })]
    expect(sortTiers(tiers).map((r) => r.price_id)).toEqual(['std1', 'std3', 'std12'])
    expect(defaultTier(tiers, sub(), 'std12')?.price_id).toBe('std12')
    expect(defaultTier(tiers, sub(), null)?.price_id).toBe('std1')
    const quarterly = sub({ renewal_price: { id: 'old', currency: 'CNY', unit_amount: 1, billing_interval: 'month', interval_count: 3, available: false } })
    expect(defaultTier(tiers, quarterly, null)?.price_id).toBe('std3')
  })

  it('建单带 as_of 与 expect；余额只在用了时传；可选字段不用时不传', () => {
    const r = row({ with_balance: split({ applied: 1200, payable: 1800 }) })
    const qt = quote([r], { balance: 1200 })
    const renew = orderRequest({ target: { kind: 'renew', subId: 'a', fromPlans: false }, quote: qt, row: r, split: r.with_balance, coupon: null })
    expect(renew).toEqual({ path: 'v1/me/subscriptions/a/renew', body: { price_id: 'std1', as_of: qt.as_of, expect: { total: 3000, balance_applied: 1200, payable: 1800 }, use_balance: 1200 } })
    const off = orderRequest({ target: { kind: 'renew', subId: 'a', fromPlans: false }, quote: qt, row: r, split: r.without_balance, coupon: 'X' })
    expect(off.body).toEqual({ price_id: 'std1', as_of: qt.as_of, expect: { total: 3000, balance_applied: 0, payable: 3000 }, coupon_code: 'X' })
    const pack = orderRequest({ target: { kind: 'pack', packId: 'k', subId: 'a', chooser: false }, quote: qt, row: r, split: r.without_balance, coupon: null })
    expect(pack.body).toMatchObject({ pack_id: 'k', subscription_id: 'a' })
    const fresh = orderRequest({ target: { kind: 'new', planId: 'std' }, quote: qt, row: r, split: r.without_balance, coupon: null, label: ' 妈妈的 iPad ', newCopy: true })
    expect(fresh.body).toMatchObject({ plan_id: 'std', price_id: 'std1', new_copy: true, label: '妈妈的 iPad' })
    expect(orderRequest({ target: { kind: 'new', planId: 'std' }, quote: qt, row: r, split: r.without_balance, coupon: null, label: '  ' }).body).not.toHaveProperty('label')
    // 重新报价后再点仍是同一个意图
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
    const off = payCopy(qt, r, r.without_balance, false, '续费', methods)
    expect(off.balanceLine?.text).toBe('用余额（有 ¥12.00，现在没用）')
    expect(off.button).toBe('续费，付 ¥30.00')
  })

  it('S7b：余额够付，不出付款方式', () => {
    const r = row({ with_balance: split({ applied: 3000 }) })
    const c = payCopy(quote([r], { balance: 5000 }), r, r.with_balance, true, '续费', methods)
    expect(c.sum).toEqual({ text: '余额够付，', strong: '¥30.00', after: ' 全部用余额' })
    expect(c.needsMethod).toBe(false)
    expect(c.button).toBe('续费，用余额付 ¥30.00')
  })

  it('S7c：剩下的低于最低额时少用一点余额，说明紧挨金额', () => {
    const r = row({ with_balance: split({ applied: 2900, payable: 100, kept: 50 }) })
    const c = payCopy(quote([r], { balance: 2950 }), r, r.with_balance, true, '续费', methods)
    expect(c.keptNote).toBe('支付宝、微信支付最低要付 ¥1.00，所以这次余额只用 ¥29.00，剩下 ¥0.50 还在余额里。')
    expect(c.button).toBe('续费，付 ¥1.00')
  })

  it('应付低于最低额：余额够就锁成打开；不够就免掉差价（用户 10-07 拍板）', () => {
    const forced = row({ total: 30, with_balance: split({ applied: 30, forced: true }), without_balance: split({ payable: 30 }) })
    const f = payCopy(quote([forced], { balance: 850 }), forced, forced.with_balance, false, '换成进阶版', methods)
    expect(f.balanceLine).toEqual({ on: true, locked: true, text: '这单只要 ¥0.30，低于支付最低额，只能用余额付' })
    expect(f.button).toBe('换成进阶版，用余额付 ¥0.30')
    // A 路：SmallDue 时 payable 已归零、免掉的钱在 waived
    const small = row({ total: 30, with_balance: split({ small_due: true, waived: 30 }), without_balance: split({ small_due: true, waived: 30 }) })
    const s = payCopy(quote([small]), small, small.with_balance, true, '换成进阶版', methods)
    expect(s.sum.text).toBe('差价 ¥0.30 不到支付最低额，这次免了')
    expect(s.needsMethod).toBe(false)
    expect(s.button).toBe('换成进阶版')
  })

  it('不用付钱、还退余额', () => {
    const r = row({ total: 0, refund: 900, with_balance: split(), without_balance: split() })
    expect(payCopy(quote([r], { balance: 0 }), r, r.with_balance, true, '换成基础版', methods)).toMatchObject({ balanceLine: null, sum: { text: '这次不用付钱', after: '，多出的 ¥9.00 退到钱包余额' }, button: '换成基础版' })
  })
})

describe('确认页的文字（原型 S4 / S8）', () => {
  const s4 = sub({ usedGiB: 20, current_period_end: '2026-11-01T12:00:00Z' })
  const naming = makeNaming([s4], undefined)
  const credit = { paid: 3000, days_left: 25, days_total: 30, traffic_left: 80 * GIB, traffic_total: 100 * GIB, ratio_ppm: 800000 }

  it('换便宜：先说钱再说流量，三档都写算式', () => {
    const r = row({ plan_id: 'basic', price_id: 'basic1', subtotal: 1500, credit: 2400, credit_detail: credit, total: 0, refund: 900, period_end: '2026-11-07T12:00:00Z', previous_end: '2026-11-01T12:00:00Z' })
    const c = confirmCopy({ target: { kind: 'change', subId: s4.id, planId: 'basic', chooser: false }, sub: s4, plan: BASIC, oldPlan: STD, row: r, naming, held: [s4], now: NOW })
    expect(c.title).toBe('换成基础版')
    expect(c.callout[0]).toBe('现在就换成基础版：链接不变。每月流量 100G → 50G、设备 3 台 → 2 台，今天生效。多出的 ¥9.00 退到钱包余额。')
    expect(c.callout[1]).toMatch(/^从今天起重新算一期，到期 11月\d+日 → 11月\d+日。$/)
    expect(c.formula).toBe('¥15.00 − 原套餐没用完的 ¥24.00 = 今天不用付，退 ¥9.00 到钱包余额')
    expect(c.rows.find((x) => x.k === '原套餐没用完的部分')?.calc).toBe('标准版这期付了 ¥30.00。还剩 25/30 天（83%），流量还剩 80G/100G（80%），按少的那项算：¥30.00 × 80% = ¥24.00。按下单那一刻算；送的天数和流量包不算钱。')
    expect(c.notes[0]).toEqual({ b: '标准版这期没用完的 80G 和 25 天，已经按 ¥24.00 算给你了', rest: '（先抵基础版的 ¥15.00，多的 ¥9.00 退到钱包）。从今天起按基础版（每月 50G）重新算。' })
    expect(tierNote({ kind: 'change', subId: 'x', planId: 'basic', chooser: false }, r)).toEqual(['¥15 − ¥24', '= 退 ¥9'])
    expect(changeFormula(row({ subtotal: 4500, credit: 2400, total: 2100 }))).toBe('¥45.00 − 原套餐没用完的 ¥24.00 = 今天付 ¥21.00')
  })

  it('过期 3 天：恢复使用，过期那几天不补', () => {
    const s8 = sub({ status: 'expired', current_period_end: '2026-10-04T12:00:00Z' })
    const c = confirmCopy({ target: { kind: 'renew', subId: s8.id, fromPlans: false }, sub: s8, plan: STD, row: row({ period_end: '2026-11-07T12:00:00Z' }), naming: makeNaming([s8], undefined), held: [s8], now: NOW })
    expect(c.title).toBe('恢复使用')
    expect(c.callout[0]).toMatch(/^付款后马上恢复使用。从今天起算，到 11月\d+日；过期那 3 天不补。链接不变，设备上不用重新添加。$/)
  })

  it('续费：剩的流量到期不累加；从选购页来的给「另买一份」的退路', () => {
    const one = sub({ usedGiB: 92 })
    const c = confirmCopy({ target: { kind: 'renew', subId: one.id, fromPlans: true }, sub: one, plan: STD, row: row(), naming: makeNaming([one], undefined), held: [one], now: NOW })
    expect(c.notes[0]).toMatch(/^现在剩的 8G 用到 10月\d+日，到时不累加；10月\d+日 起新的一期，每月 100G。$/)
    expect(c.alt).toEqual({ text: '要给别人另买一份标准版？', href: '#/checkout?new=std' })
  })

  it('加流量：多份时写只给这份用，加错了也不怕', () => {
    const naming2 = makeNaming([MINE, MOM], undefined)
    const c = confirmCopy({ target: { kind: 'pack', packId: 'k100', subId: 'a', chooser: false }, sub: MINE, pack: PACK100, row: row({ subtotal: 1800 }), naming: naming2, held: [MINE, MOM], now: NOW })
    expect(c.callout[0]).toBe('100G 马上加到「我的 · 标准版」，只给这份用，用完为止。链接不变，不用重新添加。')
    expect(c.rows.find((x) => x.k === '加完能用')?.v).toBe('7G → 107G')
    expect(c.change).toBe('加错了也不怕：这份以后彻底停用时，没用完的流量包可以转到另一份。')
  })
})

describe('完成页', () => {
  it('上下文经地址带到收银台回跳之后', () => {
    const ctx = { kind: 'change' as const, subId: 'a', was: '2026-11-01T12:00:00Z', oldPlan: '标准版', refund: 900, packBytes: 0, waived: 0, revive: false }
    expect(readDone(new URLSearchParams(doneQuery(ctx)))).toEqual(ctx)
  })

  it('花了多少：余额、渠道、免掉的差价', () => {
    const o = { balance_applied: 850, paid_amount: 950, payments: [{ status: 'succeeded', amount: 950, currency: 'CNY', created_at: '', method: 'alipay', provider_name: '易支付' }] }
    expect(paidText(o)).toBe('余额付了 ¥8.50，支付宝付了 ¥9.50')
    expect(paidText({ balance_applied: 0, paid_amount: 0, payments: [] })).toBe('这次没有花钱')
    expect(paidText({ balance_applied: 0, paid_amount: 0, payments: [] }, 30)).toBe('差价 ¥0.30 不到支付最低额，这次免了')
  })

  it('新买的那一份：按套餐名与有效期至认', () => {
    const fresh = sub({ id: 'n', current_period_start: '2026-10-07T12:00:00Z', current_period_end: '2026-11-07T12:00:00Z' })
    expect(findNewSub({ plan_name: '标准版', subscription_period_end: '2026-11-07T12:00:00Z' }, [sub(), fresh])?.id).toBe('n')
    expect(findNewSub({ plan_name: '标准版', subscription_period_end: undefined }, [sub(), fresh])?.id).toBe('n')
  })

  it('续费好了：用到哪天、原来哪天；你要做的什么都不用做', () => {
    const o = { balance_applied: 0, paid_amount: 3000, payments: [], plan_name: '标准版' } as never
    const s = sub({ current_period_end: '2026-11-19T12:00:00Z' })
    const lines = doneLines({ kind: 'renew', subId: s.id, was: '2026-10-19T12:00:00Z', oldPlan: null, refund: 0, packBytes: 0, waived: 0, revive: false }, o, s, STD, [s], makeNaming([s], undefined), null)
    expect(lines.title).toBe('续费好了')
    expect(lines.happened[0]).toMatch(/^你的标准版用到 11月\d+日（原来 10月\d+日）$/)
    expect(lines.showNewLink).toBe(false)
  })
})

describe('目录文案', () => {
  it('周期归档与文案', () => {
    expect([periodOf(price('a', 1, 'quarter')), periodOf(price('b', 1, 'month', 12)), periodOf(price('c', 1, 'week', 2))]).toEqual(['3m', '12m', 'week:2'])
    expect([periodName('12m'), periodName('week:2'), periodName('one_time:1')]).toEqual(['年付', '每 2 周', '一次性'])
    expect([periodUnit('3m'), periodUnit('month:2'), periodUnit('day:1')]).toEqual(['/ 季', '/ 2 个月', '/ 天'])
  })

  it('折合月价、省额与最小省幅', () => {
    expect(monthlyNote(price('m', 2900, 'month'))).toBe('按月付费')
    expect(monthlyNote(price('y', 29900, 'year'))).toBe('折合 ¥24.92 / 月')
    expect(savingAmount(OLD_STD, OLD_STD.prices[2]!)).toBe(2900 * 12 - 29900)
    // 标准版年付省 14%，专业版省 15% → 取 14
    expect(savingPercent([OLD_STD, OLD_PRO], '12m')).toBe(14)
    expect(savingPercent([OLD_STD], '1m')).toBe(0)
    expect(perGbNote(PACK)).toBe('约 ¥0.20 / GB')
  })

  it('重置与额度周期文案（billing_cycle 按到期日重置）', () => {
    expect(resetNote({ quota_reset_strategy: 'fixed_day', quota_reset_day: 15 })).toBe('每月 15 日重置')
    expect(resetNote({ quota_reset_strategy: 'billing_cycle', quota_reset_day: null })).toBe('到期日自动重置')
    expect(quotaPeriodNote({ quota_reset_strategy: 'billing_cycle' }, 'cycle', '12m')).toBe('/ 年')
    expect(quotaPeriodNote({ quota_reset_strategy: 'natural_month' }, 'cycle', '12m')).toBe('/ 月')
    expect(quotaPeriodNote({ quota_reset_strategy: 'billing_cycle' }, 'total', '1m')).toBe('总量')
  })
})

describe('支付', () => {
  it('回跳地址相对入口页解析、带 paid=1', () => {
    expect(payReturnUrl('o1', 'https://panel.example.com/#/checkout?plan=x')).toBe('https://panel.example.com/#/orders/o1?paid=1')
    expect(payReturnUrl('o 1', 'https://h.example/app/index.html#/x')).toBe('https://h.example/app/#/orders/o%201?paid=1')
  })

  it('成功文案按订单种类', () => {
    const base = { paid_amount: 0, total_amount: 5000, currency: 'CNY' }
    expect(paySuccessText({ ...base, kind: 'addon' })).toBe('流量包已到账')
    expect(paySuccessText({ ...base, kind: 'upgrade' })).toBe('已换好，链接不变')
    expect(paySuccessText({ ...base, kind: 'topup' })).toBe('余额 +¥50.00')
    expect(paySuccessText({ ...base, kind: 'new', subscription_period_end: '2027-09-19T10:00:00Z' })).toBe('已开通，有效期至 2027-09-19')
    expect(paySuccessText({ ...base, kind: 'renewal' })).toBe('已开通')
  })
})
