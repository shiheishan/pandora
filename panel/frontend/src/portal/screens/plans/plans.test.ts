import { describe, expect, it } from 'vitest'
import { makeNaming } from '../common/purchase'
import { BASIC, PRO, row, STD, sub } from '../common/testing'
import { changeNote, planCardView, renewOrder } from './labels'

// 选购页套餐卡（原型 planCard）：同款只有续费；不同款单份写今天付多少；多份下一步再选；另买一份全是「买这个」
const NOW = new Date('2026-10-07T12:00:00Z')
const one = sub({ current_period_end: '2026-10-19T12:00:00Z', renew_until: '2026-11-19T12:00:00Z' })
const MINE = sub({ id: 'a', label: '我的', current_period_end: '2026-10-30T12:00:00Z', renew_until: '2026-11-30T12:00:00Z' })
const MOM = sub({ id: 'b', label: '妈妈的 iPad', plan_id: 'basic', plan_name: '基础版', current_period_end: '2026-10-12T12:00:00Z', renew_until: '2026-11-12T12:00:00Z' })
const noQuote = () => undefined

describe('选购页套餐卡', () => {
  it('同款：只有续费，小字写到期日从哪天到哪天，链接不变', () => {
    const v = planCardView(STD, 'mine', [one], makeNaming([one], undefined), noQuote, NOW)
    expect(v).toMatchObject({ label: '续费', href: '#/checkout?renew=sub1&from=plans', primary: true, current: true, tag: { text: '你在用 · 还剩 12 天', tone: 'ok' } })
    expect(v.note).toMatch(/^到期日 10月\d+日 → 11月\d+日 · 链接不变$/)
  })

  it('不同款、只有一份：「换成 X」，小字是报价里今天付多少（退的钱写到钱包）', () => {
    const quoteFor = (s: string, p: string) => (s === 'sub1' && p === 'pro' ? row({ total: 2100 }) : s === 'sub1' && p === 'basic' ? row({ total: 0, refund: 900 }) : undefined)
    const naming = makeNaming([one], undefined)
    expect(planCardView(PRO, 'mine', [one], naming, quoteFor, NOW)).toMatchObject({ label: '换成进阶版', href: '#/checkout?change=sub1&plan=pro', primary: false, note: '换过去，今天付 ¥21.00 · 链接不变' })
    expect(planCardView(BASIC, 'mine', [one], naming, quoteFor, NOW).note).toBe('换过去今天不用付，还退 ¥9.00 到钱包余额 · 链接不变')
    expect(changeNote(undefined)).toBe('链接不变')
  })

  it('不同款、多份：下一步选换掉哪一份（不预选）；同款写哪一份在用', () => {
    const naming = makeNaming([MINE, MOM], undefined)
    expect(planCardView(PRO, 'mine', [MINE, MOM], naming, noQuote, NOW)).toMatchObject({ label: '换成进阶版', href: '#/checkout?change-plan=pro', note: '下一步选换掉哪一份 · 链接不变' })
    expect(planCardView(BASIC, 'mine', [MINE, MOM], naming, noQuote, NOW)).toMatchObject({ label: '续费「妈妈的 iPad」', tag: { text: '「妈妈的 iPad」在用' } })
  })

  it('另买一份：全是「买这个」，同款标「你已有一份」，小字写新链接', () => {
    const naming = makeNaming([one], undefined)
    const v = planCardView(STD, 'new', [one], naming, noQuote, NOW)
    expect(v).toMatchObject({ label: '买这个', href: '#/checkout?new=std', tag: { text: '你已有一份' } })
    expect(v.note).toMatch(/^新链接 · 今天起到 11月\d+日$/)
    expect(planCardView(STD, 'mine', [], makeNaming([], undefined), noQuote, NOW)).toMatchObject({ label: '买这个', tag: { text: '多数人选这个', tone: 'brand' } })
  })

  it('不能续、不能换时按钮置灰并给出路', () => {
    const naming = makeNaming([one], undefined)
    expect(planCardView({ ...PRO, allow_upgrade: false }, 'mine', [one], naming, noQuote, NOW)).toMatchObject({ href: null, label: '暂不能换成进阶版' })
    expect(planCardView(STD, 'mine', [{ ...one, renew_until: null }], naming, noQuote, NOW)).toMatchObject({ href: null, label: '这个套餐暂时不能续费' })
  })

  it('同款可续的次序与服务端一致：生效中按到期从早到晚，再接过期的', () => {
    const late = sub({ id: 'late', current_period_end: '2026-12-01T00:00:00Z' })
    const early = sub({ id: 'early', current_period_end: '2026-10-10T00:00:00Z' })
    const gone = sub({ id: 'gone', status: 'expired', current_period_end: '2026-10-01T00:00:00Z' })
    expect(renewOrder([gone, late, early]).map((s) => s.id)).toEqual(['early', 'late', 'gone'])
  })
})
