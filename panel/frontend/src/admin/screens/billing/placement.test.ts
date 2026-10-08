import { describe, expect, it } from 'vitest'
import { badgeLabel, choiceOf, placementResult, placementTitle, priceNote, selectedKey, stateLine, submitGate, subjectOf } from './placement'
import { manualPreviewSchema, type Placement } from './schemas'

const sub = (over: Partial<Placement>): Placement => ({
  key: 'renew:s1',
  kind: 'renew',
  subscription_id: 's1',
  plan_id: 'p1',
  plan_name: '标准版',
  state: 'live',
  period_end: '2026-10-30T12:00:00Z',
  new_period_end: '2026-11-30T12:00:00Z',
  currency: 'CNY',
  ...over,
})
const NEW: Placement = { key: 'new', kind: 'new', new_period_end: '2026-11-07T12:00:00Z' }

describe('落点选项的称呼与结果', () => {
  it('有备注名写「备注名」套餐名，没有只写套餐名', () => {
    expect(subjectOf({ plan_name: '标准版' })).toBe('标准版')
    expect(subjectOf({ plan_name: '标准版', label: '妈妈的 iPad' })).toBe('「妈妈的 iPad」标准版')
    expect(subjectOf({})).toBe('订阅')
  })

  it('续一期：写清这一份的状态与到期日变化，链接不变', () => {
    const p = sub({})
    expect(placementTitle(p, '标准版')).toBe('续一期：标准版')
    expect(placementResult(p, 'pending')).toBe('生效中，2026-10-30 到期；到期 2026-10-30 → 2026-11-30；订阅链接不变')
    expect(stateLine({ state: 'live' })).toBe('生效中，长期有效')
  })

  it('过期 30 天内的同款：恢复并续一期，从现在起算', () => {
    const p = sub({ state: 'revivable' })
    expect(placementTitle(p, '标准版')).toBe('恢复并续一期：标准版')
    expect(placementResult(p, 'grant')).toContain('已过期（2026-10-30 到期），还在 30 天续费窗口内')
    expect(placementResult(p, 'grant')).toContain('从现在起算，到期 2026-11-30')
  })

  it('换套餐：写明剩余价值的去向，赠送全额退回、别的先抵新价', () => {
    const p = sub({ key: 'change:s1', kind: 'change', plan_name: '基础版', label: '妈妈的 iPad', credit: 1200 })
    expect(placementTitle(p, '进阶版')).toBe('把「妈妈的 iPad」基础版换成进阶版')
    expect(placementResult(p, 'grant')).toContain('没用完的 ¥12.00 全额退到用户余额')
    expect(placementResult(p, 'offline')).toContain('没用完的 ¥12.00 先抵新价，抵不完的退到用户余额')
    expect(placementResult(p, 'grant')).toContain('换后从现在起算，到期 2026-11-30')
    expect(placementResult(p, 'grant')).toContain('订阅链接不变')
    expect(placementResult({ ...p, credit: undefined }, 'pending')).toContain('原套餐没有可抵的剩余价值')
  })

  it('恢复并改成：目标是过期那份', () => {
    const p = sub({ key: 'change:s1', kind: 'change', expired: true, state: 'revivable', plan_name: '基础版' })
    expect(placementTitle(p, '进阶版')).toBe('恢复基础版并改成进阶版')
  })

  it('另开一份：会生成新链接，现有订阅都不动', () => {
    expect(placementTitle(NEW, '进阶版')).toBe('另开一份进阶版')
    expect(placementResult(NEW, 'grant')).toBe('会生成新的订阅链接，用户现有的订阅都不动；到期 2026-11-07')
  })

  it('徽标与 target', () => {
    expect(badgeLabel('same_plan')).toBe('同款')
    expect(badgeLabel(undefined)).toBeNull()
    expect(choiceOf(sub({}))).toEqual({ kind: 'renew', subscription_id: 's1' })
    expect(choiceOf(NEW)).toEqual({ kind: 'new' })
  })

  it('价目说明：换套餐有剩余价值时不替服务端算应付', () => {
    expect(priceNote(4500, 'CNY', undefined)).toBe('应付 ¥45.00')
    expect(priceNote(4500, 'CNY', { kind: 'renew' })).toBe('应付 ¥45.00')
    expect(priceNote(4500, 'CNY', { kind: 'change', credit: 0 })).toBe('应付 ¥45.00')
    expect(priceNote(4500, 'CNY', { kind: 'change', credit: 1200 })).toContain('先抵扣原套餐的剩余价值')
  })
})

describe('默认值与提交按钮', () => {
  const two = manualPreviewSchema.parse({ options: [sub({}), NEW], default_key: 'renew:s1' })
  const noDefault = manualPreviewSchema.parse({ options: [NEW, sub({ key: 'change:s1', kind: 'change' })], default_key: '' })

  it('按服务端默认预选；点过且仍在列表里的用点的', () => {
    expect(selectedKey(two, '')).toBe('renew:s1')
    expect(selectedKey(two, 'new')).toBe('new')
    // 点过的选项已经不在列表里（订阅变了）：回到服务端默认
    expect(selectedKey(two, 'change:gone')).toBe('renew:s1')
  })

  it('没有默认值时不预选，只有点了才有', () => {
    expect(selectedKey(noDefault, '')).toBe('')
    expect(selectedKey(noDefault, 'new')).toBe('new')
  })

  it('服务端给的默认键不在列表里当作没有默认', () => {
    expect(selectedKey({ options: [NEW, sub({})], default_key: 'renew:other' }, '')).toBe('')
  })

  it('只有一个选项时不让选，直接用它', () => {
    expect(selectedKey({ options: [NEW], default_key: '' }, '')).toBe('new')
  })

  it('还没取回来时没有选择', () => {
    expect(selectedKey(undefined, 'new')).toBe('')
  })

  it('提交按钮：没默认值灰着写「先选落点」，选了才能点', () => {
    const base = { hasInputs: true, loading: false, failed: false }
    expect(submitGate({ ...base, selected: '' })).toEqual({ ok: false, label: '先选落点' })
    expect(submitGate({ ...base, selected: 'new' })).toEqual({ ok: true })
    expect(submitGate({ ...base, loading: true, selected: '' })).toEqual({ ok: false, label: '读取落点中…' })
    expect(submitGate({ ...base, failed: true, selected: '' })).toEqual({ ok: false, label: '落点读取失败' })
    // 用户、套餐、价格没选全：落点无从谈起，按钮照常可点，点了由表单预检指出缺哪项
    expect(submitGate({ hasInputs: false, loading: false, failed: false, selected: '' })).toEqual({ ok: true })
  })
})

describe('preview 的 schema', () => {
  it('omitempty 的键缺席、default_key 缺席都按空处理', () => {
    const r = manualPreviewSchema.parse({ options: [{ key: 'new', kind: 'new' }] })
    expect(r.default_key).toBe('')
    expect(r.options[0]!.new_period_end).toBeUndefined()
  })

  it('后台开单只会有 renew / change / new，别的判为不符', () => {
    expect(manualPreviewSchema.safeParse({ options: [{ key: 'reset_traffic:s1', kind: 'reset_traffic' }], default_key: '' }).success).toBe(false)
    expect(manualPreviewSchema.safeParse({ options: [{ key: 'new', kind: 'new', badge: 'cheapest' }], default_key: '' }).success).toBe(false)
  })
})
