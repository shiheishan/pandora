import { describe, expect, it } from 'vitest'
import { href } from '../../../core/router'
import { makeNaming } from '../common/purchase'
import { ONCE, STD, sub } from '../common/testing'
import { planCardView, renewOrder } from './labels'

const NOW = new Date('2026-10-07T12:00:00Z')
const one = sub({ current_period_end: '2026-10-19T12:00:00Z', renew_until: '2026-11-19T12:00:00Z' })
const onceHeld = sub({ plan_id: 'once', plan_name: '流量版', plan_kind: 'one_time', renewable: false, stackable: true })

describe('选购页套餐卡', () => {
  it('没有份：?new=，小字今天起到', () => {
    const v = planCardView(STD, [], makeNaming([], undefined), NOW)
    expect(v).toMatchObject({ label: '买这个', href: href('/checkout', { new: 'std' }), primary: true, tag: { text: '推荐', tone: 'brand' } })
    expect(v.note).toMatch(/^今天起到 11月\d+日$/)
  })

  it('持有同款订阅可续：续费 + ?plan=', () => {
    const v = planCardView(STD, [one], makeNaming([one], undefined), NOW)
    expect(v).toMatchObject({
      label: '续费',
      href: href('/checkout', { plan: 'std' }),
      primary: true,
      current: true,
      tag: { text: '你在用', tone: 'ok' },
      note: '下一步选：续在原来那份上，或另开一份',
    })
  })

  it('持有同款一次性可叠加：再买一份', () => {
    const v = planCardView(ONCE, [onceHeld], makeNaming([onceHeld], undefined), NOW)
    expect(v).toMatchObject({
      label: '再买一份',
      href: href('/checkout', { plan: 'once' }),
      note: '下一步选：叠加到原来那份，或另开一份',
    })
  })

  it('持有同款但不能续：买这个 + 原来那份不能续', () => {
    const dead = sub({ renewable: false, renew_until: null })
    const v = planCardView(STD, [dead], makeNaming([dead], undefined), NOW)
    expect(v).toMatchObject({
      label: '买这个',
      href: href('/checkout', { plan: 'std' }),
      note: '原来那份不能续，会另开一份',
      current: true,
    })
  })

  it('没持有的套餐：买这个 + ?plan= + 另开或换掉', () => {
    const v = planCardView({ ...STD, id: 'pro', name: '进阶版' }, [one], makeNaming([one], undefined), NOW)
    expect(v).toMatchObject({
      label: '买这个',
      href: href('/checkout', { plan: 'pro' }),
      note: '下一步选：另开一份，或换掉现在的一份',
      current: false,
    })
  })

  it('推荐 tag（没有在用同款时）', () => {
    expect(planCardView(STD, [one], makeNaming([one], undefined), NOW).tag).toEqual({ text: '你在用', tone: 'ok' })
    expect(planCardView(STD, [], makeNaming([], undefined), NOW).tag).toEqual({ text: '推荐', tone: 'brand' })
  })

  it('同款可续的次序与服务端一致', () => {
    const late = sub({ id: 'late', current_period_end: '2026-12-01T00:00:00Z' })
    const early = sub({ id: 'early', current_period_end: '2026-10-10T00:00:00Z' })
    const gone = sub({ id: 'gone', status: 'expired', current_period_end: '2026-10-01T00:00:00Z' })
    expect(renewOrder([gone, late, early]).map((s) => s.id)).toEqual(['early', 'late', 'gone'])
  })
})
