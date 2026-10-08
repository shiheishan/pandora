import { describe, expect, it } from 'vitest'
import { makeNaming } from '../common/purchase'
import { sub } from '../common/testing'
import { giftCardSchema, type GiftCard, type PlacementOption } from './api'
import { placementBlocked, redeemViews, selectedView, simpleRedeem } from './model'

// 兑换卡（原型 S5a / S5b、redeemOptions）：选项与默认值来自服务端，这里只验写字与「没默认就置灰」
const card = (over: Partial<GiftCard>): GiftCard =>
  giftCardSchema.parse({ id: 'c', name: '卡', description: '', type: 'general', status: 'active', rewards: {}, conditions: {}, limits: {}, theme_color: '', created_at: '2026-10-01T00:00:00Z', placement: null, ...over })
const MINE = sub({ id: 'a', label: '我的', current_period_end: '2026-10-30T12:00:00Z' })
const MOM = sub({ id: 'b', label: '妈妈的 iPad', plan_id: 'basic', plan_name: '基础版', current_period_end: '2026-10-12T12:00:00Z' })
const naming2 = makeNaming([MINE, MOM], undefined)
const month = (id: string | undefined) => ({ std: 3000, basic: 1500, pro: 4500 })[id ?? ''] as number | undefined
const NOW = new Date('2026-10-07T12:00:00')

describe('兑换卡', () => {
  it('preview 的 placement 照 Go：选项字段 omitempty、placement 本身必回（纯余额卡为 null）', () => {
    expect(card({}).placement).toBeNull()
    const raw = { id: 'c', name: '卡', description: '', type: 'general', status: 'active', rewards: {}, conditions: {}, limits: {}, theme_color: '', created_at: '' }
    expect(giftCardSchema.safeParse(raw).success).toBe(false)
    expect(giftCardSchema.safeParse({ ...raw, placement: { question: 'q', options: [{ key: 'new', kind: 'new' }], default_key: '' } }).success).toBe(true)
  })

  it('同款套餐卡、两份：预选同款那份（标「同款，最常见」），每项写会发生什么', () => {
    const std = card({ type: 'plan', plan_name: '标准版', interval: 'month', interval_count: 1, rewards: { plan_id: 'std' } })
    const opts: PlacementOption[] = [
      { key: 'renew:a', kind: 'renew', subscription_id: 'a', badge: 'same_plan', state: 'live', period_end: '2026-10-30T12:00:00', new_period_end: '2026-11-30T12:00:00' },
      { key: 'new', kind: 'new', new_period_end: '2026-11-07T12:00:00' },
      { key: 'change:b', kind: 'change', subscription_id: 'b', plan_name: '基础版', plan_id: 'basic', credit: 249, new_period_end: '2026-11-07T12:00:00' },
    ]
    const views = redeemViews({ card: std, held: [MINE, MOM], naming: naming2, monthPrice: month, now: NOW }, opts, 'renew:a')
    expect(views.map((v) => [v.label, v.desc, v.badge])).toEqual([
      ['续到「我的」', '到期日 10月30日 → 11月30日 · 链接不变', '同款，最常见'],
      ['再开一份标准版', '到 11月7日 · 会得到一个新链接，要另外添加到 App', undefined],
      ['换掉「妈妈的 iPad」', '到 11月7日 · 链接不变 · 基础版没用完的 ¥2.49 退到钱包余额', undefined],
    ])
    expect(selectedView(views, null, 'renew:a')?.verb).toBe('兑换，续到 11月30日')
    expect(views[0]!.sentence).toBe('标准版 · 1 个月加到「我的 · 标准版」：到期日 10月30日 → 11月30日。链接不变，设备不用重新添加。')
  })

  it('不同款套餐卡：一律不预选，白话写升级还是另开，按钮在选之前置灰', () => {
    const one = sub()
    const pro = card({ type: 'plan', plan_name: '进阶版', rewards: { plan_id: 'pro' } })
    const opts: PlacementOption[] = [
      { key: 'new', kind: 'new' },
      { key: 'change:sub1', kind: 'change', subscription_id: 'sub1', plan_id: 'std', plan_name: '标准版', credit: 1200 },
    ]
    const views = redeemViews({ card: pro, held: [one], naming: makeNaming([one], undefined), monthPrice: month, now: NOW }, opts, '')
    expect(views.map((v) => [v.label, v.desc])).toEqual([
      ['另开一份进阶版', '会得到新链接，要另外添加到 App'],
      ['把现在的标准版升级成进阶版', '链接不变 · 标准版没用完的 ¥12.00 退到钱包余额'],
    ])
    expect(selectedView(views, null, '')).toBeNull()
    expect(selectedView(views, 'new', '')?.verb).toBe('兑换，开一份新的')
  })

  it('加时长卡预选最快到期；流量重置卡写用量清零；纯余额卡直接进钱包', () => {
    const days = card({ rewards: { expire_days: 30 } })
    const v = redeemViews({ card: days, held: [MINE, MOM], naming: naming2, monthPrice: month, now: NOW }, [{ key: 'extend_days:b', kind: 'extend_days', subscription_id: 'b', badge: 'soonest_expiry', period_end: '2026-10-12T12:00:00', new_period_end: '2026-11-11T12:00:00' }], 'extend_days:b')[0]!
    expect([v.label, v.desc, v.badge, v.verb]).toEqual(['「妈妈的 iPad · 基础版」', '到期日 10月12日 → 11月11日', '最快到期', '兑换，加 30 天'])
    const reset = redeemViews({ card: card({ rewards: { reset_quota: true } }), held: [MINE, MOM], naming: naming2, monthPrice: month, now: NOW }, [{ key: 'reset_traffic:a', kind: 'reset_traffic', subscription_id: 'a', traffic_used: 93 * 1024 ** 3, traffic_cap: 100 * 1024 ** 3, badge: 'most_used' }], 'reset_traffic:a')[0]!
    expect([reset.desc, reset.badge, reset.verb]).toEqual(['这个月已用 93G（93%） → 0', '用得最多', '兑换，流量清零重算'])
    expect(simpleRedeem(card({ rewards: { balance: 1000 } }))).toEqual({ sentence: '¥10.00 直接进钱包余额，结账时自动先用。', verb: '兑换，余额 +¥10.00' })
    expect(placementBlocked(card({ rewards: { expire_days: 7 } }))).toBe(true)
    expect(placementBlocked(card({ rewards: { traffic_bytes: 1 } }))).toBe(false)
  })
})
