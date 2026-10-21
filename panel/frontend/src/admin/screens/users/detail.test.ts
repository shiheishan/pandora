import { describe, expect, it } from 'vitest'
import { dotTone, headerTarget, listStatusView, liveTarget, NO_LIVE_REASON, planMetaLine, recentIps, subActions, type SubActionKey } from './detail'

const NOW = new Date('2026-10-16T12:00:00Z')
const days = (n: number) => new Date(NOW.getTime() + n * 86_400_000).toISOString()
const GiB = 1024 ** 3
const cycle = [{ metric: 'traffic.bytes', period: 'cycle', limit: 100 * GiB, consumed: 0, remaining: 100 * GiB }]
const total = [{ metric: 'traffic.bytes', period: 'total', limit: 500 * GiB, consumed: 0, remaining: 500 * GiB }]

type S = { id: string; status: 'active' | 'expired'; current_period_end: string | null; quotas: typeof cycle }
const sub = (id: string, status: S['status'], end: string | null, quotas = cycle): S => ({ id, status, current_period_end: end, quotas })

const reasons = (s: S, all: S[]) =>
  Object.fromEntries(subActions(s, all, NOW).map((a) => [a.key, a.reason])) as Record<SubActionKey, string | null>

describe('状态圆点与列表状态', () => {
  it('标签色映射到四种圆点', () => {
    expect(dotTone('ok')).toBe('ok')
    expect(dotTone('warn')).toBe('warn')
    expect(dotTone('danger')).toBe('bad')
    expect(dotTone('neutral')).toBe('off')
    expect(dotTone('info')).toBe('off')
  })

  it('账号正常但当前订阅已过期写「已过期」，其余照账号状态', () => {
    const cs = (status: 'active' | 'expired') => ({ id: 's', plan_name: 'p', status, current_period_end: null, traffic: { limit: null, consumed: 0 }, device_limit: 0, online_devices: 0, is_trial: false })
    expect(listStatusView({ status: 'active', current_subscription: cs('expired') })).toEqual({ label: '已过期', tone: 'warn' })
    expect(listStatusView({ status: 'active', current_subscription: cs('active') })).toEqual({ label: '正常', tone: 'ok' })
    expect(listStatusView({ status: 'active', current_subscription: null })).toEqual({ label: '正常', tone: 'ok' })
    expect(listStatusView({ status: 'banned', current_subscription: cs('expired') }).label).toBe('已禁用')
  })
})

describe('页头「续费这一份 / 加流量」的目标', () => {
  it('多份生效中取到期最晚的那份，过期的不算', () => {
    const a = sub('a', 'active', days(10))
    const b = sub('b', 'active', days(40))
    const c = sub('c', 'expired', days(-3))
    expect(liveTarget([a, c, b])?.id).toBe('b')
    expect(headerTarget([a, c, b])).toEqual({ target: b, renewLabel: '续费这一份', reason: null })
  })

  it('没有生效中的订阅：没有目标，写原因', () => {
    expect(headerTarget([sub('c', 'expired', days(-3))])).toEqual({ target: undefined, renewLabel: '续费这一份', reason: NO_LIVE_REASON })
    expect(headerTarget([]).reason).toBe(NO_LIVE_REASON)
  })

  it('一次性套餐写「给这份开单」', () => {
    expect(headerTarget([sub('o', 'active', days(3650 * 10), total)]).renewLabel).toBe('给这份开单')
  })
})

describe('订阅行的操作菜单', () => {
  it('八项一个不少，顺序固定', () => {
    const s = sub('a', 'active', days(10))
    expect(subActions(s, [s], NOW).map((a) => a.key)).toEqual(['extend', 'period', 'traffic', 'reset', 'quota', 'devices', 'order', 'rotate'])
  })

  it('生效中的订阅套餐：全部可点', () => {
    const s = sub('a', 'active', days(10))
    expect(Object.values(reasons(s, [s])).every((r) => r === null)).toBe(true)
  })

  it('过期不满 30 天：能加时长、续费、改设备，其余灰着；有生效中的另一份时不能换发', () => {
    const live = sub('l', 'active', days(10))
    const s = sub('a', 'expired', days(-5))
    const r = reasons(s, [live, s])
    expect(r.extend).toBeNull()
    expect(r.order).toBeNull()
    expect(r.devices).toBeNull()
    expect(r.period).toBe('不在生效中')
    expect(r.traffic).toBe('不在使用中')
    expect(r.reset).toBe('不在生效中')
    expect(r.quota).toBe('不在生效中')
    expect(r.rotate).toBe('先换发生效中的那份')
    // 一份生效中的都没有时，过期的也能换发
    expect(reasons(s, [s]).rotate).toBeNull()
  })

  it('过期满 30 天：不能加时长、不能续', () => {
    const s = sub('a', 'expired', days(-31))
    const r = reasons(s, [s])
    expect(r.extend).toBe('已过期满 30 天')
    expect(r.order).toBe('已过期满 30 天')
  })

  it('永久的一次性：不能加时长、改到期；续费项写「给这份开单」', () => {
    const s = sub('o', 'active', days(3650 * 10), total)
    const r = reasons(s, [s])
    expect(r.extend).toBe('永久有效')
    expect(r.period).toBe('永久有效')
    expect(subActions(s, [s], NOW).find((a) => a.key === 'order')?.label).toBe('给这份开单')
  })

  it('没有流量配额：不能改上限', () => {
    const s = { ...sub('a', 'active', days(10)), quotas: [] }
    expect(reasons(s, [s]).quota).toBe('没有流量配额')
  })
})

describe('当前套餐副行与最近登录 IP', () => {
  it('副行写类型、清零规则与到期', () => {
    expect(planMetaLine({ quotas: cycle, current_period_end: days(27) }, NOW)).toBe('订阅套餐 · 新周期清零 · 27 天后到期')
    expect(planMetaLine({ quotas: total, current_period_end: days(3650 * 10) }, NOW)).toBe('一次性套餐 · 不清零，用完即止 · 永久有效')
    expect(planMetaLine({ quotas: cycle, current_period_end: days(-3) }, NOW)).toBe('订阅套餐 · 新周期清零 · 已过期 3 天')
  })

  it('IP 按最近一次出现倒序，不改原数组', () => {
    const ips = [
      { ip: '198.51.100.1', count: 1, first: days(-9), last: days(-8), accounts: 1 },
      { ip: '198.51.100.2', count: 3, first: days(-5), last: days(-1), accounts: 2 },
    ]
    expect(recentIps(ips).map((r) => r.ip)).toEqual(['198.51.100.2', '198.51.100.1'])
    expect(ips[0]!.ip).toBe('198.51.100.1')
  })
})
