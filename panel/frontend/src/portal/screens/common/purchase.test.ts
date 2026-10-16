import { describe, expect, it } from 'vitest'
import { planTraffic } from './catalog'
import { day, endAfter, gb, heldSubs, isLow, linkTail, makeNaming, nameIdeas, nameRequired, periodLabel, profileName, resetsLeft } from './purchase'
import { BASIC, GIB, ONCE, STD, link, sub } from './testing'

// 叫法（原型 sn / dn / who）、流量不足、候选名、重名必填
const MINE = sub({ id: 'a', label: '我的', usedGiB: 93 })
const MOM = sub({ id: 'b', label: '妈妈的 iPad', plan_id: 'basic', plan_name: '基础版', capGiB: 50, usedGiB: 12 })
const PLAIN = sub({ id: 'c' })

describe('叫法', () => {
  it('只有一份时直接说「你的标准版」，看不到任何「哪一份」', () => {
    const n = makeNaming([PLAIN], [link('c', 'a3f9')])
    expect(n.multi).toBe(false)
    expect(n.sn(PLAIN)).toBe('标准版')
    expect(n.who(PLAIN)).toBe('你的标准版')
    expect(n.dn(PLAIN)).toBe('标准版 ····a3f9')
  })

  it('多份时用备注名；没备注用「套餐名 ····链接尾号」', () => {
    const n = makeNaming([MINE, MOM, PLAIN], [link('a', 'a3f9'), link('b', '7c21'), link('c', '9e0d')])
    expect(n.sn(MOM)).toBe('妈妈的 iPad')
    expect(n.dn(MOM)).toBe('妈妈的 iPad · 基础版')
    expect(n.who(MOM)).toBe('「妈妈的 iPad · 基础版」')
    expect(n.sn(PLAIN)).toBe('标准版 ····9e0d')
    expect(n.tail(PLAIN)).toBe('9e0d')
  })

  it('链接尾号取最后一段的末 4 位；配置名预览与服务端同一规则', () => {
    expect(linkTail('https://sub.example.com/s/abcdefa3f9?x=1')).toBe('a3f9')
    expect(linkTail(undefined)).toBe('')
    expect(profileName('Pandora', '  ', '标准版')).toBe('Pandora · 标准版')
    expect(profileName('Pandora', '妈妈的 iPad', '标准版')).toBe('Pandora · 妈妈的 iPad')
  })

  it('手上的份：生效中与过期 30 天内能救回的；彻底停用的不列', () => {
    const revivable = sub({ id: 'r', status: 'expired', changeable: true, renewable: true })
    const dead = sub({ id: 'd', status: 'expired', changeable: false, renewable: false })
    expect(heldSubs([PLAIN, revivable, dead]).map((s) => s.id)).toEqual(['c', 'r'])
  })
})

describe('流量与名字', () => {
  it('流量不足：剩余（含送的流量）低于 15%', () => {
    expect(isLow(sub({ usedGiB: 92 }))).toBe(true)
    expect(isLow(sub({ usedGiB: 92, bonus_bytes: 30 * GIB }))).toBe(false)
    expect(isLow(sub({ usedGiB: 40 }))).toBe(false)
    expect(isLow(sub({ quotas: [] }))).toBe(false)
  })

  it('候选名排除别的份已经用的，不排除自己', () => {
    expect(nameIdeas([MINE, MOM], null)).toEqual(['爸爸的手机', '工作电脑', '孩子的平板'])
    expect(nameIdeas([MINE, MOM], MOM)).toEqual(['妈妈的 iPad', '爸爸的手机', '工作电脑', '孩子的平板'])
  })

  it('另买同款且已有那份没起名：起名必填', () => {
    expect(nameRequired([PLAIN], 'std')).toBe(true)
    expect(nameRequired([MINE], 'std')).toBe(false)
    expect(nameRequired([PLAIN], 'pro')).toBe(false)
  })

  it('短写法与日期', () => {
    expect([gb(8 * GIB), gb(107 * GIB), gb(1.5 * GIB), gb(2048 * GIB), gb(300 * 1024 ** 2)]).toEqual(['8G', '107G', '1.5G', '2T', '300M'])
    expect(day('2026-11-19T12:00:00', new Date('2026-10-07T00:00:00'))).toBe('11月19日')
    expect(day('2027-01-02T12:00:00', new Date('2026-10-07T00:00:00'))).toBe('2027年1月2日')
    expect([periodLabel('month', 1), periodLabel('month', 3), periodLabel('month', 6), periodLabel('one_time', 1), periodLabel('year', 1), periodLabel('month', 12)]).toEqual([
      '1 个月',
      '3 个月',
      '半年',
      '一次',
      '1 年',
      '1 年',
    ])
    expect([planTraffic(STD), planTraffic(BASIC), planTraffic(ONCE), planTraffic({ ...STD, traffic_bytes: null })]).toEqual([
      '每期 100G',
      '每期 50G',
      '共 100G',
      '不限流量',
    ])
  })

  it('resetsLeft：没开 null、不限 Infinity、有上限扣已用', () => {
    expect(resetsLeft({ reset_price: null, reset_limit: 2, resets_used: 0 })).toBeNull()
    expect(resetsLeft({ reset_price: 500, reset_limit: null, resets_used: 0 })).toBe(Number.POSITIVE_INFINITY)
    expect(resetsLeft({ reset_price: 500, reset_limit: 2, resets_used: 1 })).toBe(1)
  })

  it('endAfter：一次性永久 null，有有效天数则顺延', () => {
    const from = new Date('2026-10-01T00:00:00Z')
    expect(endAfter(from, { billing_interval: 'one_time', interval_count: 1, validity_days: null })).toBeNull()
    const end = endAfter(from, { billing_interval: 'one_time', interval_count: 1, validity_days: 30 })
    expect(end?.getUTCDate()).toBe(31)
    expect(end?.getUTCMonth()).toBe(9)
  })
})
