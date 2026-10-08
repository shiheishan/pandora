import { describe, expect, it } from 'vitest'
import { extendedSchema, rotatedSchema, userDetailSchema, usersSchema } from './api'
import {
  currentSubscription,
  deviceLimitLabel,
  deviceView,
  expiryView,
  extendableSubscriptions,
  extendedEnd,
  expiredDays,
  isRescue,
  extendReasonProblem,
  initial,
  listParams,
  liveSubscriptions,
  orderWhat,
  paidTotalsLabel,
  parseExtendDays,
  parseYuan,
  passwordProblem,
  shortId,
  trafficView,
  unattachedNext,
} from './model'
import { sharingHint } from './RiskTab'

const NOW = new Date('2026-09-24T12:00:00Z')
const days = (n: number) => new Date(NOW.getTime() + n * 86_400_000).toISOString()
const GiB = 1024 ** 3

describe('状态分段 → 后端 query', () => {
  it('正常 / 已禁用走账号状态，已过期走订阅态；未分组传 none', () => {
    expect(listParams('all', '', '', 0)).toEqual({ offset: 0 })
    expect(listParams('active', '', ' a@b ', 25)).toEqual({ offset: 25, q: 'a@b', status: 'active' })
    expect(listParams('disabled', 'none', '', 0)).toEqual({ offset: 0, group_id: 'none', status: 'suspended,banned' })
    expect(listParams('expired', 'g1', '', 0)).toEqual({ offset: 0, group_id: 'g1', sub_state: 'expired' })
  })
})

describe('列表三列', () => {
  it('到期：过期标红，7 天内标黄，无到期时间是长期有效', () => {
    expect(expiryView(days(-3), NOW)).toEqual({ text: '已过期 3 天', tone: 'danger' })
    expect(expiryView(days(0.2), NOW)).toEqual({ text: '1 天后到期', tone: 'warn' })
    expect(expiryView(days(30), NOW)).toEqual({ text: '30 天后到期', tone: 'neutral' })
    expect(expiryView(null, NOW).text).toBe('长期有效')
  })

  it('流量：limit 为 null 显示不限；70% 以上黄、90% 以上红', () => {
    expect(trafficView(null, 5 * GiB)).toEqual({ text: '5.00 GB / 不限', percent: 0, tone: 'neutral' })
    expect(trafficView(100 * GiB, 75 * GiB)).toEqual({ text: '75.0 GB / 100 GB', percent: 75, tone: 'warn' })
    expect(trafficView(100 * GiB, 120 * GiB).percent).toBe(100)
  })

  it('设备：0 为不限；满额黄、超额红', () => {
    expect(deviceView(3, 0)).toEqual({ text: '3/不限', tone: 'neutral' })
    expect(deviceView(3, 3).tone).toBe('warn')
    expect(deviceView(4, 3).tone).toBe('danger')
  })

  it('抽屉设备上限：覆盖 ?? 套餐 ?? 不限', () => {
    expect(deviceLimitLabel({ device_limit_override: 10, plan_max_devices: 3 })).toEqual({ text: '10', source: 'override' })
    expect(deviceLimitLabel({ device_limit_override: null, plan_max_devices: 3 })).toEqual({ text: '3', source: 'plan' })
    expect(deviceLimitLabel({ device_limit_override: 0, plan_max_devices: 3 })).toEqual({ text: '不限', source: 'override' })
    expect(deviceLimitLabel({ device_limit_override: null, plan_max_devices: null })).toEqual({ text: '不限', source: 'none' })
  })

  it('头像字与短 id', () => {
    expect(initial('zhang@qq.com', null)).toBe('Z')
    expect(initial('zhang@qq.com', ' 张伟 ')).toBe('张')
    expect(shortId('1a2b3c4d-0000-4000-8000-000000000001')).toBe('1a2b3c4d')
  })
})

describe('当前订阅（与后端 currentSubscriptionSQL 同口径）', () => {
  const s = (status: 'active' | 'expired' | 'grace' | 'cancelled', end: number | null, id: string) => ({ id, status, current_period_end: end === null ? null : days(end) })

  it('还在用的优先，其次到期最晚', () => {
    expect(currentSubscription([s('expired', 30, 'a'), s('active', 5, 'b'), s('grace', 10, 'c')])?.id).toBe('c')
    expect(currentSubscription([s('expired', -1, 'a'), s('cancelled', -5, 'b')])?.id).toBe('a')
    expect(currentSubscription([])).toBeUndefined()
  })

  it('换发只列还在用的订阅，一个都没有时退回全部', () => {
    expect(liveSubscriptions([s('expired', 1, 'a'), s('active', 5, 'b')]).map((x) => x.id)).toEqual(['b'])
    expect(liveSubscriptions([s('expired', 1, 'a')]).map((x) => x.id)).toEqual(['a'])
  })
})

describe('加时长（与后端 subscriptionExtendable / validateAdminExtend 同口径）', () => {
  const s = (status: 'active' | 'trialing' | 'expired', end: number | null, id: string) => ({ id, status, current_period_end: end === null ? null : days(end) })

  it('生效中、试用中、过期不满 30 天且有到期时间的订阅能加时长（w5expiry）', () => {
    expect(extendableSubscriptions([s('active', 5, 'a'), s('trialing', 5, 'b'), s('expired', -1, 'c'), s('active', null, 'd'), s('expired', -31, 'e')], NOW).map((x) => x.id)).toEqual(['a', 'b', 'c'])
    expect(extendableSubscriptions([s('expired', -31, 'e')], NOW)).toEqual([])
    expect(expiredDays(days(-3), NOW)).toBe(3)
    expect(expiredDays(days(2), NOW)).toBeNull()
    expect(isRescue(s('expired', -3, 'c'), NOW)).toBe(true)
    expect(isRescue(s('active', -1, 'x'), NOW)).toBe(true)
    expect(isRescue(s('trialing', 5, 'b'), NOW)).toBe(true)
    expect(isRescue(s('active', 5, 'a'), NOW)).toBe(false)
  })

  it('天数 1–3650 的整数，原因 5–500 字', () => {
    expect(parseExtendDays(' 30 ')).toBe(30)
    expect(parseExtendDays('3650')).toBe(3650)
    for (const bad of ['', '0', '3651', '7.5', '-1', '1e2', 'abc']) expect(parseExtendDays(bad)).toBeNull()
    expect(extendReasonProblem('补偿线路故障')).toBeNull()
    expect(extendReasonProblem(' 补偿 ')).toContain('至少 5 个字')
    expect(extendReasonProblem('长'.repeat(501))).toContain('500')
  })

  it('预计新到期从「原到期」与「现在」里更晚的那个起算', () => {
    expect(extendedEnd(days(10), 7, NOW).toISOString()).toBe(new Date(Date.parse(days(17))).toISOString())
    expect(extendedEnd(days(-3), 7, NOW).toISOString()).toBe(new Date(NOW.getTime() + 7 * 86_400_000).toISOString())
  })
})

describe('订单与输入', () => {
  it('订单「买了什么」', () => {
    expect(orderWhat({ kind: 'new', plan_name: '标准版', interval: 'month', interval_count: 1, item_count: 1 })).toBe('标准版 · 月付')
    expect(orderWhat({ kind: 'renewal', plan_name: '专业版', interval: 'month', interval_count: 3, item_count: 2 })).toBe('专业版 · 3 × 月付 等 2 项')
    expect(orderWhat({ kind: 'topup', plan_name: '', interval: '', interval_count: 0, item_count: 1 })).toBe('余额充值')
  })

  it('累计消费按币种各写一笔，没有实收时按用户币种写 0（R114）', () => {
    expect(paidTotalsLabel([], 'CNY')).toBe(paidTotalsLabel([{ currency: 'CNY', amount: 0 }], 'USD'))
    const both = paidTotalsLabel([{ currency: 'CNY', amount: 12000 }, { currency: 'USD', amount: 500 }], 'CNY')
    expect(both.split(' · ')).toHaveLength(2)
    expect(both).toContain('120')
  })

  it('调账：元转分，正负号、两位小数；0 与非法返回 null', () => {
    expect(parseYuan('+50')).toBe(5000)
    expect(parseYuan('-20.5')).toBe(-2050)
    expect(parseYuan('¥0.01')).toBe(1)
    expect(parseYuan('0')).toBeNull()
    expect(parseYuan('1.234')).toBeNull()
    expect(parseYuan('abc')).toBeNull()
  })

  it('密码策略与 platform/crypto 一致：8 位、字母 + 数字、≤ 256 字节', () => {
    expect(passwordProblem('abc123')).toBe('密码至少需要 8 个字符')
    expect(passwordProblem('abcdefgh')).toBe('密码必须同时包含字母和数字')
    expect(passwordProblem('密码密码密码1234')).toBeNull()
    expect(passwordProblem(`a1${'x'.repeat(300)}`)).toBe('密码过长')
    expect(passwordProblem('pandora2026')).toBeNull()
  })

  it('未分配的流量包：没有在用的订阅时开通第一份会自动挂上，有在用的要用户自己挑', () => {
    expect(unattachedNext(0)).toContain('开通第一份订阅时会自动加到那一份上')
    expect(unattachedNext(1)).toContain('在门户里选一份')
    expect(unattachedNext(3)).toContain('在门户里选一份')
  })

  it('分享提示阈值', () => {
    expect(sharingHint(1).tone).toBe('ok')
    expect(sharingHint(3).tone).toBe('warn')
    expect(sharingHint(6).tone).toBe('danger')
  })
})

describe('schema', () => {
  it('保留规则 2：换发响应带令牌就判为不符约定', () => {
    expect(rotatedSchema.safeParse({ user_email: 'a@b.c', old_revoked: true }).success).toBe(true)
    expect(rotatedSchema.safeParse({ user_email: 'a@b.c', old_revoked: true, token: 'secret' }).success).toBe(false)
  })

  it('加时长响应：五个键都必填，天数为正', () => {
    const ok = { subscription_id: 's', user_email: 'a@b.c', days: 7, previous_end: days(1), period_end: days(8) }
    expect(extendedSchema.safeParse(ok).success).toBe(true)
    expect(extendedSchema.safeParse({ ...ok, days: 0 }).success).toBe(false)
    expect(extendedSchema.safeParse({ ...ok, period_end: undefined }).success).toBe(false)
  })

  it('用户详情的订阅行（按份）：备注名可为 null，这一份的流量包余量必填', () => {
    const subscription = {
      id: 's1',
      label: '妈妈的 iPad',
      plan_name: '标准版',
      plan_version: 1,
      status: 'active',
      current_period_start: days(-20),
      current_period_end: days(10),
      amount: 2500,
      currency: 'CNY',
      auto_renew: true,
      quotas: [],
      device_limit_override: null,
      plan_max_devices: 3,
      online_devices: 0,
      pack_remaining_bytes: 10 * 1024 ** 3,
    }
    const detail = {
      id: 'u',
      email: 'a@b.c',
      display_name: null,
      status: 'active',
      risk_level: 'normal',
      group_name: '',
      group_id: null,
      created_at: days(-30),
      last_login_at: null,
      subscription_count: 0,
      active_plan: null,
      balance: 0,
      currency: 'CNY',
      current_subscription: null,
      email_verified: true,
      subscriptions: [subscription],
      recent_orders: [],
      roles: [],
      stats: { paid_totals: [], order_count: 0, referral_count: 0 },
      referrer: null,
      telegram: null,
      unattached_pack_bytes: 5 * 1024 ** 3,
    }
    expect(userDetailSchema.safeParse(detail).success).toBe(true)
    // 顶层未分配的流量包余量（B 路 UserDetail.UnattachedPackBytes，非 omitempty）：必填、非负整数
    expect(userDetailSchema.safeParse({ ...detail, unattached_pack_bytes: undefined }).success).toBe(false)
    expect(userDetailSchema.safeParse({ ...detail, unattached_pack_bytes: -1 }).success).toBe(false)
    expect(userDetailSchema.safeParse({ ...detail, subscriptions: [{ ...subscription, label: null }] }).success).toBe(true)
    expect(userDetailSchema.safeParse({ ...detail, subscriptions: [{ ...subscription, pack_remaining_bytes: undefined }] }).success).toBe(false)
    expect(userDetailSchema.safeParse({ ...detail, subscriptions: [{ ...subscription, label: undefined }] }).success).toBe(false)
  })

  it('列表行：未知账号状态判为不符；current_subscription 可为 null', () => {
    const row = {
      id: 'u',
      email: 'a@b.c',
      display_name: null,
      status: 'active',
      risk_level: 'normal',
      group_name: '',
      group_id: null,
      created_at: days(-1),
      last_login_at: null,
      subscription_count: 0,
      active_plan: null,
      balance: 0,
      currency: 'CNY',
      current_subscription: null,
    }
    expect(usersSchema.safeParse({ users: [row], total: 1 }).success).toBe(true)
    expect(usersSchema.safeParse({ users: [{ ...row, status: 'disabled' }], total: 1 }).success).toBe(false)
  })
})
