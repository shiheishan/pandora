import { describe, expect, it } from 'vitest'
import { changeHint, copyTabText, eyebrowText, expiryText, goneText, heroTraffic, isExpiredNow, leakSources, lowTip, metaLine, nextUsageText, pillText, renewText, resetRow } from './card-text'
import { APP_UPDATE, guideApps, parseGuideDevice } from './app-update'
import { appLink, CLIENTS, DEVICES, detectDevice, guessDeviceFromName, protocolLabel, rateLabel, shareMessage } from './clients'
import { throttleNote } from './catalog'
import { createPlacedOrder, recallPayable } from './intent'
import { archivedPaidText, isPaymentLapsed, lapsedCancelLabel, payHereNote } from './PayFlow'
import { refusalOf } from './quote'
import { ApiError } from '../../../core/api'
import { orderQuerySchema, queryFailure, queryOutcome } from './order-query'
import { archivedRefundText, expiryNote, intervalLabel, isPayable, isPlanArchivedRefund, orderRowSchema, orderTitle } from './orders'
import { pickPrimary } from '../../queries'
import { subscriptionSchema, usageReportSchema, type Subscription } from './subscriptions'
import { sub as subFixture } from './testing'
import { buildUsageBars, bytesParts, pickTrafficQuota, projectUsage, resetAtOf, trafficSummary } from './traffic'

const NOW = new Date('2026-09-24T12:00:00Z')
const GIB = 1024 ** 3

// 字段与 Go 的 mySubscriptionView 一一对应；renewable 同口径：生效状态即可续费
function sub(over: Partial<Subscription> = {}): Subscription {
  const status = over.status ?? 'active'
  const live = status === 'active'
  return subscriptionSchema.parse({
    id: 's1',
    plan_id: 'p1',
    price_id: '',
    plan_name: '专业版',
    plan_kind: 'subscription',
    plan_version: 3,
    status,
    state: live ? 'live' : 'revivable',
    current_period_start: '2026-09-01T00:00:00Z',
    current_period_end: '2026-11-05T10:00:00Z',
    amount: 5900,
    validity_days: null,
    quotas: [{ metric: 'traffic.bytes', limit: 500 * GIB, bonus_bytes: 0, consumed: 312 * GIB, remaining: 188 * GIB, period: 'cycle', period_start: '2026-09-01T00:00:00Z', period_end: null }],
    device_limit: null,
    online_devices: 0,
    traffic_limit: 500 * GIB,
    bonus_bytes: 0,
    traffic_used: 312 * GIB,
    permanent: false,
    resets_used: 0,
    reset_limit: null,
    reset_price: null,
    next_reset_at: null,
    renewable: live,
    stackable: false,
    renewal_price: null,
    label: null,
    client_name: 'Pandora · 专业版',
    changeable: live,
    renew_until: null,
    usage: { traffic_bytes: 500 * GIB, device_limit: null, throttle_kbps: null },
    next_usage: null,
    is_trial: false,
    ...over,
  })
}

describe('traffic', () => {
  it('bytesParts：core formatBytes 拆成数字与单位，负数与小数按 0 与取整处理', () => {
    expect(bytesParts(218 * GIB)).toEqual(['218', 'GB'])
    expect(bytesParts(4.7 * GIB)).toEqual(['4.70', 'GB'])
    expect(bytesParts(-5)).toEqual(['0', 'B'])
    expect(bytesParts(1.5)).toEqual(['2', 'B'])
  })

  it('摘要：total = limit + bonus，remaining = max(0, total - consumed)；limit=null 为不限', () => {
    expect(trafficSummary({ metric: 'traffic.bytes', limit: 470, bonus_bytes: 30, consumed: 400, remaining: 0 })).toEqual({ used: 400, total: 500, remaining: 100, bonus: 30, ratio: 0.8 })
    expect(trafficSummary({ metric: 'traffic.bytes', limit: null, consumed: 7, remaining: null })).toMatchObject({ total: null, remaining: null, bonus: 0, ratio: 0 })
    expect(trafficSummary(null)).toBeNull()
  })

  it('额度行：只看 traffic.bytes，多行时取周期覆盖当前时刻的那行', () => {
    const rows = [
      { metric: 'devices', limit: 5, consumed: 1, remaining: 4 },
      { metric: 'traffic.bytes', limit: 1, consumed: 1, remaining: 0, period_start: '2026-08-01T00:00:00Z', period_end: '2026-09-01T00:00:00Z' },
      { metric: 'traffic.bytes', limit: 2, consumed: 1, remaining: 1, period_start: '2026-09-01T00:00:00Z', period_end: '2026-10-01T00:00:00Z' },
    ]
    expect(pickTrafficQuota(rows, NOW)?.limit).toBe(2)
    expect(pickTrafficQuota([{ metric: 'devices', limit: 5, consumed: 1, remaining: 4 }])).toBeNull()
  })

  it('重置日：一次性为 null；否则 next_reset_at → 额度周期末 → 用量窗口末', () => {
    expect(resetAtOf({ plan_kind: 'one_time', next_reset_at: 'x' }, null, undefined)).toBeNull()
    expect(resetAtOf({ plan_kind: 'subscription', next_reset_at: 'a' }, { period_end: 'b' }, { period_end: 'c' })).toBe('a')
    expect(resetAtOf({ plan_kind: 'subscription' }, { period_end: null }, { period_end: 'c' })).toBe('c')
    expect(resetAtOf({ plan_kind: 'subscription' }, null, undefined)).toBeNull()
  })

  it('预测：够用 / 比重置早用完 / 不限 / 无用量', () => {
    const s = trafficSummary({ metric: 'traffic.bytes', limit: 500 * GIB, bonus_bytes: 0, consumed: 312 * GIB, remaining: 188 * GIB })!
    const reset = '2026-09-27T12:00:00Z' // 3 天后
    expect(projectUsage(s, 11 * GIB, reset, NOW)).toEqual({ text: '按目前的速度，本期预计用到 345 GB，够用。', short: false })
    const tight = trafficSummary({ metric: 'traffic.bytes', limit: 100 * GIB, bonus_bytes: 0, consumed: 90 * GIB, remaining: 10 * GIB })!
    expect(projectUsage(tight, 5 * GIB, reset, NOW)).toEqual({ text: '按目前的速度，约 2 天后用完，比重置早。', short: true })
    expect(projectUsage({ ...s, total: null, remaining: null }, GIB, reset, NOW).short).toBe(false)
    expect(projectUsage(s, 0, reset, NOW).text).toBe('本期还没有产生用量。')
  })

  it('用量柱：今天高亮、周期末前补「未到」、按时区取周期末日期', () => {
    const chart = buildUsageBars({
      timezone: 'Asia/Shanghai',
      period_end: '2026-09-26T16:00:00Z', // 上海 09-27 零点
      days: [
        { date: '2026-09-22', bytes: 10 },
        { date: '2026-09-23', bytes: 20 },
        { date: '2026-09-24', bytes: 5 },
      ],
    })
    expect(chart.bars.map((b) => [b.date, b.bytes, b.today])).toEqual([
      ['2026-09-22', 10, false],
      ['2026-09-23', 20, false],
      ['2026-09-24', 5, true],
      ['2026-09-25', null, false],
      ['2026-09-26', null, false],
    ])
    expect(chart.bars[1]!.height).toBe(100)
    expect(chart).toMatchObject({ start: '09-22', endLabel: '09-27 重置', range: '09-22 至 09-27' })
    const open = buildUsageBars({ timezone: 'Nope/Zone', period_end: null, days: [{ date: '2026-09-24', bytes: 0 }] })
    expect(open).toMatchObject({ endLabel: '09-24', bars: [{ today: true, height: 0 }] })
  })
})

describe('catalog', () => {
  it('限速文案（R99）：kbps → Mbps，不限速为 null', () => {
    expect(throttleNote(null)).toBeNull()
    expect(throttleNote(100_000)).toBe('限速 100 Mbps')
    expect(throttleNote(2500)).toBe('限速 2.5 Mbps')
  })
})

describe('orders', () => {
  it('周期文案', () => {
    expect(intervalLabel('month', 1)).toBe('月付')
    expect(intervalLabel('month', 3)).toBe('季付')
    expect(intervalLabel('year', 1)).toBe('年付')
    expect(intervalLabel('month', 2)).toBe('2 个月')
    expect(intervalLabel(undefined)).toBe('')
  })

  it('标题按 kind 映射，周期缺席（omitempty）时只写套餐名', () => {
    expect(orderTitle({ kind: 'topup', item_name: '' })).toBe('余额充值')
    expect(orderTitle({ kind: 'stack', plan_name: '流量版', item_name: '流量版' })).toBe('叠加 · 流量版')
    expect(orderTitle({ kind: 'new', plan_name: '专业版', item_name: '专业版', interval: 'month', interval_count: 1 })).toBe('专业版 · 月付')
    expect(orderTitle({ kind: 'new', plan_name: '专业版', item_name: '专业版' })).toBe('专业版')
    expect(orderTitle({ kind: 'renewal', plan_name: '专业版', item_name: '专业版', interval: 'year', interval_count: 1 })).toBe('专业版 · 年付续费')
    expect(orderTitle({ kind: 'upgrade', plan_name: '家庭版', item_name: '家庭版' })).toBe('家庭版 · 变更套餐')
  })

  it('待支付期限按 expires_at 倒数', () => {
    expect(expiryNote('2026-09-24T12:17:30Z', NOW)).toBe('18 分钟后自动取消')
    expect(expiryNote('2026-09-24T11:59:00Z', NOW)).toBe('即将自动取消')
    expect(expiryNote(undefined, NOW)).toBe('请尽快完成支付')
  })

  it('订单行 schema：无 omitempty 的字段必填，omitempty 的可缺席，未知 kind 拒收', () => {
    const row = { id: 'o', order_no: 'PD-1', kind: 'stack', status: 'pending_payment', total_amount: 1, discount_amount: 0, balance_applied: 0, payable_amount: 1, paid_amount: 0, item_name: '', cancellable: true, has_payment_intent: false, created_at: 'x' }
    expect(orderRowSchema.safeParse(row).success).toBe(true)
    expect(orderRowSchema.safeParse({ ...row, kind: 'gift' }).success).toBe(false)
    const missing: Partial<typeof row> = { ...row }
    delete missing.cancellable
    expect(orderRowSchema.safeParse(missing).success).toBe(false)
    // has_payment_intent 在 Go 无 omitempty：缺席即形状不符
    const noIntent: Partial<typeof row> = { ...row }
    delete noIntent.has_payment_intent
    expect(orderRowSchema.safeParse(noIntent).success).toBe(false)
    const noItem: Partial<typeof row> = { ...row }
    delete noItem.item_name
    expect(orderRowSchema.safeParse(noItem).success).toBe(false)
  })
})

describe('subscriptions', () => {
  it('全字段可解析，扩展字段缺席即拒收（Go 无 omitempty）；状态枚举外的值拒收', () => {
    const missing: Partial<Subscription> = { ...sub() }
    delete missing.bonus_bytes
    expect(subscriptionSchema.safeParse(missing).success).toBe(false)
    expect(subscriptionSchema.safeParse({ ...sub(), status: 'weird' }).success).toBe(false)
    expect(subscriptionSchema.safeParse({ ...sub(), quotas: null }).success).toBe(false)
  })

  it('主订阅：生效状态中 current_period_end 最晚的一条', () => {
    const list = [
      sub({ id: 'old', status: 'expired', current_period_end: '2027-01-01T00:00:00Z' }),
      sub({ id: 'a', current_period_end: '2026-10-01T00:00:00Z' }),
      sub({ id: 'b', current_period_end: '2026-12-01T00:00:00Z' }),
    ]
    expect(pickPrimary(list)?.id).toBe('b')
    expect(pickPrimary([sub({ status: 'expired', renewable: false })])).toBeNull()
  })

  it('主订阅：没有生效订阅时回落到能原地续费的已过期订阅', () => {
    const expired = [
      sub({ id: 'closed', status: 'expired', renewable: false, current_period_end: '2026-09-01T00:00:00Z' }),
      sub({ id: 'older', status: 'expired', renewable: true, current_period_end: '2026-09-10T00:00:00Z' }),
      sub({ id: 'newer', status: 'expired', renewable: true, current_period_end: '2026-09-20T00:00:00Z' }),
    ]
    expect(pickPrimary(expired)?.id).toBe('newer')
    expect(pickPrimary([expired[0]!])).toBeNull()
    expect(pickPrimary([...expired, sub({ id: 'live' })])?.id).toBe('live')
  })

  it('按日用量 schema 与契约一致', () => {
    const ok = { timezone: 'UTC', period_start: 'x', period_end: null, days: [{ date: '2026-09-24', bytes: 1 }], today_bytes: 1, avg_daily_bytes: 1 }
    expect(usageReportSchema.safeParse(ok).success).toBe(true)
    expect(usageReportSchema.safeParse({ ...ok, avg_daily_bytes: 1.5 }).success).toBe(false)
  })

  it('设计稿 2.9 的新字段必回：备注名、配置名、能否换套餐、续到哪天（可空的为 null）', () => {
    for (const k of ['label', 'client_name', 'changeable', 'renew_until'] as const) {
      const missing: Partial<Subscription> = { ...sub() }
      delete missing[k]
      expect(subscriptionSchema.safeParse(missing).success, k).toBe(false)
    }
    expect(subscriptionSchema.safeParse({ ...sub(), label: '妈妈的 iPad', renew_until: '2026-12-05T10:00:00Z' }).success).toBe(true)
  })

  it('卡片到期：7 天以上「还剩 N 天」、7 天内警示、最后 24 小时按小时、过期按天', () => {
    expect(expiryText(sub(), NOW)).toMatchObject({ main: '还剩 42 天', tone: 'ok' })
    expect(expiryText(sub({ current_period_end: '2026-09-29T12:00:00Z' }), NOW)).toMatchObject({ main: '5 天后到期', tone: 'warn' })
    expect(expiryText(sub({ current_period_end: '2026-09-24T18:00:00Z' }), NOW)).toMatchObject({ main: '还剩 6 小时', tone: 'warn' })
    expect(expiryText(sub({ status: 'expired', current_period_end: '2026-09-21T11:00:00Z' }), NOW)).toMatchObject({ main: '已过期 3 天', tone: 'danger' })
    expect(expiryText(sub({ current_period_end: null }), NOW).main).toBe('长期有效')
    expect(expiryText(sub({ permanent: true, current_period_end: '2099-01-01T00:00:00Z' }), NOW)).toMatchObject({ main: '永久有效', sub: '', tone: 'ok' })
    expect(isExpiredNow(sub({ permanent: true, status: 'active', current_period_end: '2000-01-01T00:00:00Z' }), NOW)).toBe(false)
    expect(isExpiredNow(sub({ status: 'expired' }), NOW)).toBe(true)
    expect(isExpiredNow(sub({ current_period_end: '2026-09-20T08:30:00Z' }), NOW)).toBe(true)
    expect(isExpiredNow(sub(), NOW)).toBe(false)
  })

  // 主卡用的一份：100G、用了 40G、3 台、11 月 5 日到期、续费价 ¥30/月还在卖
  const card = (over: Partial<Subscription> = {}) =>
    sub({
      traffic_limit: 100 * GIB,
      traffic_used: 40 * GIB,
      device_limit: 3,
      online_devices: 1,
      current_period_end: '2026-11-05T12:00:00Z',
      renew_until: '2026-12-05T12:00:00Z',
      renewal_price: { id: 'p1m', unit_amount: 3000, billing_interval: 'month', interval_count: 1, available: true },
      ...over,
    })

  it('主卡流量：剩余含送的，订阅套餐的送流量本期有效；一次性不清零', () => {
    expect(heroTraffic(card())).toMatchObject({ big: { value: '60', unit: 'GB' }, ratio: 0.6, usedPct: 40, scope: '本期剩余', total: '100G', bonus: null, tail: '' })
    expect(heroTraffic(card({ bonus_bytes: 30 * GIB, traffic_used: 92 * GIB }))).toMatchObject({ big: { value: '38', unit: 'GB' }, total: '130G', bonus: '30G', bonusNote: '本期有效', usedPct: 70 })
    const once = card({ plan_kind: 'one_time', bonus_bytes: 10 * GIB, traffic_used: 50 * GIB })
    expect(heroTraffic(once)).toMatchObject({ scope: '剩余', bonus: '10G', bonusNote: '', tail: '不清零，用完为止' })
    expect(heroTraffic(card({ traffic_limit: null }))).toMatchObject({ big: null, ratio: null, total: null })
  })

  it('流量快用完的提示：过 80% 才有；下一期清零早于到期才说到下一期恢复', () => {
    const next = { next_reset_at: '2026-10-05T12:00:00Z' }
    expect(lowTip(card({ traffic_used: 79 * GIB, ...next }), NOW)).toBeNull()
    expect(lowTip(card({ traffic_used: 85 * GIB, ...next }), NOW)).toEqual({ title: '流量快用完了', text: '本期已用 85%，用完会停，到下一期恢复' })
    expect(lowTip(card({ traffic_used: 85 * GIB, next_reset_at: '2026-11-05T12:00:00Z' }), NOW)?.text).toBe('本期已用 85%，用完会停，续上后恢复')
    expect(lowTip(card({ traffic_used: 100 * GIB, ...next }), NOW)).toEqual({ title: '流量用完了', text: '节点已经停了，到下一期恢复' })
    expect(lowTip(card({ traffic_used: 90 * GIB, plan_kind: 'one_time', ...next }), NOW)?.text).toBe('已用 90%，用完会停')
  })

  it('主卡文字：眉标、灰字行、续费按钮带价只在那档还在卖时', () => {
    expect(eyebrowText('Pandora', card())).toBe('Pandora')
    expect(eyebrowText('Pandora', card({ label: '家里用' }))).toBe('Pandora · 专业版')
    expect(eyebrowText('Pandora', card({ plan_kind: 'one_time' }))).toBe('Pandora · 一次性')
    expect(metaLine(card({ online_devices: 2, current_period_end: '2026-11-05T12:03:00Z' }), NOW)).toMatch(/^11月5日 \d\d:03 到期 · 2 \/ 3 台设备在线$/)
    expect(metaLine(card({ permanent: true, device_limit: null }), NOW)).toBe('没有到期日 · 1 台设备在线 · 不限台数')
    expect(renewText(card(), NOW)).toBe('续费 · 到 12月5日')
    const gone = card({ status: 'expired', current_period_end: '2026-09-21T11:00:00Z', renew_until: '2026-10-24T12:00:00Z' })
    expect(renewText(gone, NOW)).toBe('续费，恢复使用 · ¥30/月')
    expect(renewText({ ...gone, renewal_price: { ...gone.renewal_price!, available: false } }, NOW)).toBe('续费，恢复使用 · 续到 10月24日')
    expect(renewText(card({ renew_until: null }), NOW)).toBeNull()
    expect(pillText(gone, NOW)).toBe('已到期')
    expect(goneText(gone, NOW)).toBe('9月21日到期了')
  })

  it('操作行：换套餐的算法说明、重置次数与能用满多少（含送的）、切换条小字', () => {
    expect(changeHint(card())).toBe('差价按剩余时间算，换之前先看报价')
    expect(changeHint(card({ plan_kind: 'one_time' }))).toBe('差价按剩余流量和天数算（取少的），换之前先看报价')
    expect(changeHint(card({ plan_kind: 'one_time', permanent: true }))).toBe('差价按剩余流量算，换之前先看报价')
    expect(resetRow(card(), NOW)).toBeNull()
    const r = card({ reset_price: 1500, reset_limit: 2, resets_used: 0, bonus_bytes: 20 * GIB })
    expect(resetRow(r, NOW)).toEqual({ title: '重置本期流量', sub: '已用的清零，马上能用满 120G · 本期还能买 2 次', price: '¥15', open: true })
    expect(resetRow({ ...r, resets_used: 2 }, NOW)).toMatchObject({ sub: '本期的 2 次已用完', open: false })
    expect(resetRow({ ...r, reset_limit: null }, NOW)?.sub).toBe('已用的清零，马上能用满 120G')
    expect(copyTabText(r, NOW)).toBe('80G · 42 天')
    expect(copyTabText(card({ permanent: true }), NOW)).toBe('60G · 永久')
  })

  it('疑似泄露：近 24 小时来源超过设备上限，过期的不提示', () => {
    const l = { subscription_id: 's1', url: 'u', fetch_count: 12, last_fetched_at: null, distinct_sources_24h: 7, expired: false }
    expect(leakSources(sub({ device_limit: 2 }), l)).toBe(7)
    expect(leakSources(sub({ device_limit: 8 }), l)).toBeNull()
    expect(leakSources(sub({ device_limit: null }), l)).toBeNull()
    expect(leakSources(sub({ device_limit: 2, status: 'expired' }), l)).toBeNull()
  })
})

describe('nextUsageText', () => {
  it('next_usage 为 null → null', () => {
    expect(nextUsageText(subFixture({ next_usage: null }))).toBeNull()
  })

  it('只流量不同（100G → 200G）→ 「下期起每期 200G」', () => {
    const s = subFixture({
      usage: { traffic_bytes: 100 * GIB, device_limit: 3, throttle_kbps: null },
      next_usage: { traffic_bytes: 200 * GIB, device_limit: 3, throttle_kbps: null },
    })
    expect(nextUsageText(s)).toBe('下期起每期 200G')
  })

  it('新版本不限流量（traffic_bytes null）→ 「下期起每期不限流量」', () => {
    const s = subFixture({
      usage: { traffic_bytes: 100 * GIB, device_limit: 3, throttle_kbps: null },
      next_usage: { traffic_bytes: null, device_limit: 3, throttle_kbps: null },
    })
    expect(nextUsageText(s)).toBe('下期起每期不限流量')
  })

  it('流量、设备（3 → 5）、限速（null → 50000）都不同 → 「下期起每期 200G，5 台设备同时用，限速 50 Mbps」', () => {
    const s = subFixture({
      usage: { traffic_bytes: 100 * GIB, device_limit: 3, throttle_kbps: null },
      next_usage: { traffic_bytes: 200 * GIB, device_limit: 5, throttle_kbps: 50_000 },
    })
    expect(nextUsageText(s)).toBe('下期起每期 200G，5 台设备同时用，限速 50 Mbps')
  })

  it('限速 50000 → null → 「下期起不限速」（usage.throttle_kbps 50000，next 的 null，其余相同）', () => {
    const s = subFixture({
      usage: { traffic_bytes: 100 * GIB, device_limit: 3, throttle_kbps: 50_000 },
      next_usage: { traffic_bytes: 100 * GIB, device_limit: 3, throttle_kbps: null },
    })
    expect(nextUsageText(s)).toBe('下期起不限速')
  })

  it('plan_kind: "one_time" 即使给了 next_usage 也返回 null', () => {
    const s = subFixture({
      plan_kind: 'one_time',
      usage: { traffic_bytes: 100 * GIB, device_limit: 3, throttle_kbps: null },
      next_usage: { traffic_bytes: 200 * GIB, device_limit: 3, throttle_kbps: null },
    })
    expect(nextUsageText(s)).toBeNull()
  })

  it('三项都相同 → null', () => {
    const s = subFixture({
      usage: { traffic_bytes: 100 * GIB, device_limit: 3, throttle_kbps: null },
      next_usage: { traffic_bytes: 100 * GIB, device_limit: 3, throttle_kbps: null },
    })
    expect(nextUsageText(s)).toBeNull()
  })
})

describe('clients', () => {
  it('深链带原链接与配置名；没有可靠 scheme 的 App 复制链接', () => {
    const url = 'https://sub.example.com/s/abc?x=1'
    const name = 'Pandora · 妈妈的 iPad'
    expect(appLink('Clash Verge', url, name)).toBe(`clash://install-config?url=${encodeURIComponent(url)}&name=${encodeURIComponent(name)}`)
    expect(appLink('Shadowrocket', url, name)).toBe(`shadowrocket://add/sub://${btoa(url)}?remark=${encodeURIComponent(name)}`)
    expect(appLink('sing-box', url, name)).toBe(`sing-box://import-remote-profile?url=${encodeURIComponent(url)}#${encodeURIComponent(name)}`)
    expect(appLink('v2rayN', url, name)).toBeNull()
    expect(appLink('Quantumult X', url, name)).toBeNull()
  })

  it('按设备推荐：UA 猜这台；给别人添加时按备注名猜对方', () => {
    expect(detectDevice('Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)')).toBe('ios')
    expect(detectDevice('Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)', 5)).toBe('ios')
    expect(detectDevice('Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)', 0)).toBe('mac')
    expect(detectDevice('Mozilla/5.0 (Linux; Android 14; Pixel 8)')).toBe('android')
    expect(detectDevice('Mozilla/5.0 (Windows NT 10.0; Win64; x64)')).toBe('windows')
    expect(guessDeviceFromName('妈妈的 iPad')).toBe('ios')
    expect(guessDeviceFromName('爸爸的华为')).toBe('android')
    expect(guessDeviceFromName('工作电脑')).toBe('windows')
    expect(guessDeviceFromName(null)).toBe('ios')
    expect(shareMessage('ios', 'https://x/s/1')).toBe('在 iPhone 或 iPad 上装 Shadowrocket（App Store 搜索）→ 打开下面这条链接 → 点「添加」。\nhttps://x/s/1')
    expect(shareMessage('android', 'u')).toContain('Clash Meta for Android')
  })

  it('协议展示名与倍率标签', () => {
    expect(protocolLabel('hysteria2')).toBe('Hysteria2')
    expect(protocolLabel('VLESS')).toBe('VLESS')
    expect(protocolLabel('brand-new')).toBe('brand-new')
    expect(rateLabel(1)).toBeNull()
    expect(rateLabel(1.5)).toBe('×1.5')
    expect(rateLabel(0.333)).toBe('×0.33')
  })
})

// ---------------------------------------------------------------------------
// 下单防重复（幂等键本身的测试在 core/intent.test.ts）
// ---------------------------------------------------------------------------
describe('intent', () => {
  it('刚下的待支付单：同样的请求在 30 分钟内取回，换参数、过期或 forget 后取不到', () => {
    let now = 0
    const placed = createPlacedOrder<string>(() => now)
    placed.remember({ plan: 'p', price: 'a' }, 'order-1')
    expect(placed.recall({ plan: 'p', price: 'a' })).toBe('order-1')
    expect(placed.recall({ plan: 'p', price: 'b' })).toBeNull()
    now = 30 * 60_000
    expect(placed.recall({ plan: 'p', price: 'a' })).toBeNull()
    now = 0
    placed.forget()
    expect(placed.recall({ plan: 'p', price: 'a' })).toBeNull()
  })

  it('再点时刚下的单还能付就重开它；已不可支付就忘掉，下次同样的请求不再取回', async () => {
    const placed = createPlacedOrder<string>(() => 0)
    const request = { plan: 'p', price: 'a' }
    placed.remember(request, 'order-1')
    const asked: string[] = []
    expect(await recallPayable(placed, request, async (id) => (asked.push(id), true))).toBe('order-1')
    expect(placed.recall(request)).toBe('order-1')
    // 在别处取消 / 超时 / 已付掉：忘掉并返回 null，调用方按新请求下单
    expect(await recallPayable(placed, request, async () => false)).toBeNull()
    expect(placed.recall(request)).toBeNull()
    // 没有记下的单时不去问后端
    expect(await recallPayable(placed, request, async (id) => (asked.push(id), true))).toBeNull()
    expect(asked).toEqual(['order-1'])
  })

  it('可支付：只有 pending_payment 且未到 expires_at', () => {
    const at = Date.parse('2026-09-24T12:00:00Z')
    expect(isPayable({ status: 'pending_payment', expires_at: '2026-09-24T12:10:00Z' }, at)).toBe(true)
    expect(isPayable({ status: 'pending_payment', expires_at: '2026-09-24T11:59:59Z' }, at)).toBe(false)
    for (const status of ['paid', 'fulfilled', 'cancelled', 'expired'] as const) expect(isPayable({ status, expires_at: undefined }, at)).toBe(false)
  })
})

describe('order query (我已支付，刷新状态)', () => {
  const base = { order_id: 'o', order_no: 'PD1', provider_code: 'epay', channel_status: 'paid', reconciled: true, already_recorded: false, order_status: 'fulfilled' }
  it('reads the Go response shape; quarantine_kind is omitempty', () => {
    expect(orderQuerySchema.parse(base).quarantine_kind).toBeUndefined()
    expect(orderQuerySchema.safeParse({ ...base, reconciled: undefined }).success).toBe(false)
    expect(orderQuerySchema.safeParse({ ...base, channel_status: 'failed' }).success).toBe(false)
  })

  it('sorts every answer into settled / pending / failed', () => {
    const r = orderQuerySchema.parse(base)
    expect(queryOutcome(r).kind).toBe('settled')
    expect(queryOutcome({ ...r, reconciled: false, already_recorded: true, order_status: 'paid' }).kind).toBe('settled')
    // 钱到了但订单已关闭：算到账，提示走工单
    const late = queryOutcome({ ...r, quarantine_kind: 'released_order', order_status: 'cancelled' })
    expect(late.kind).toBe('settled')
    expect(late.text).toContain('工单')
    expect(late.wallet).toBeUndefined()
    // 套餐归档后才付款（10-16）：自动转余额，不走工单；查单结果没有手续费，用统一说法，给去钱包的按钮
    const archived = queryOutcome({ ...r, quarantine_kind: 'archived_plan', order_status: 'cancelled' })
    expect(archived).toEqual({ kind: 'settled', text: '款项已到账，但套餐已停售，没有开通；应付金额已转入钱包余额，支付手续费不退。', wallet: true })
    expect(queryOutcome({ ...r, channel_status: 'unpaid', reconciled: false, order_status: 'pending_payment' }).kind).toBe('pending')
    expect(queryOutcome({ ...r, channel_status: 'not_found', reconciled: false, order_status: 'pending_payment' }).kind).toBe('pending')
    expect(queryFailure(new ApiError({ status: 409, code: 'conflict', message: '该订单从未发起过支付，无法向渠道查单' }))).toEqual({ kind: 'failed', text: '查询失败：该订单从未发起过支付，无法向渠道查单' })
    expect(queryFailure(new ApiError({ status: 429, code: 'rate_limited', message: 'x' })).text).toContain('太频繁')
    expect(queryFailure(new ApiError({ status: 0, code: 'network_error', message: 'x' })).text).toContain('网络')
  })
})

describe('在 App 里点一次更新（按 App 的帮助）', () => {
  it('clients.ts 推荐的每个 App 都有一段：在哪个页面、点哪个按钮、看到什么算成功', () => {
    const apps = new Set(DEVICES.flatMap((d) => [...CLIENTS[d].top, ...CLIENTS[d].rest].map((a) => a.name)))
    for (const app of apps) {
      const s = APP_UPDATE[app]
      expect(s, app).toBeDefined()
      expect([s!.where, s!.tap, s!.ok].every((x) => x.length > 0), app).toBe(true)
    }
    // 先常用的，再「其他 App」
    expect(guideApps('ios')).toEqual(['Shadowrocket', 'Stash', 'Quantumult X', 'Loon', 'sing-box'])
  })

  it('地址里的设备：认识的照用，不认识的按这台算', () => {
    expect(parseGuideDevice('android', 'ios')).toBe('android')
    expect(parseGuideDevice('nokia', 'mac')).toBe('mac')
    expect(parseGuideDevice(null, 'windows')).toBe('windows')
  })
})

describe('付款与建单被拒', () => {
  it('电脑上的次按钮写清是干什么的', () => {
    expect(payHereNote({ method: 'alipay', label: '支付宝' })).toBe('没带手机？在这台电脑上用支付宝网页付')
    expect(payHereNote({ method: 'wxpay', label: '微信支付' })).toBe('没带手机？在这台电脑上打开微信支付付款页，按页面提示付')
  })

  it('发起支付超过付款期限按码认（409 order_lapsed），同样文案的普通 409 不算', () => {
    expect(isPaymentLapsed(new ApiError({ status: 409, code: 'order_lapsed', message: '随便什么话' }))).toBe(true)
    expect(isPaymentLapsed(new ApiError({ status: 409, code: 'conflict', message: '这张订单已超过付款期限，请取消后重新下单' }))).toBe(false)
  })

  it('套餐停售不能再付（409 order_lapsed + reason=plan_archived）：取消按钮不说「重新下单」', () => {
    const retired = new ApiError({ status: 409, code: 'order_lapsed', message: '套餐已停售，不能再付款，可以取消这张订单', fields: { reason: 'plan_archived' } })
    expect(isPaymentLapsed(retired)).toBe(true)
    expect(lapsedCancelLabel(retired)).toBe('取消这张订单')
    expect(lapsedCancelLabel(new ApiError({ status: 409, code: 'order_lapsed', message: 'x' }))).toBe('取消这张单，重新下单')
  })

  it('套餐归档转余额：有手续费写「应付金额」并注明不退，没有写「付的钱」，不知道用统一说法', () => {
    expect(archivedRefundText(300)).toBe('应付金额已转入钱包余额（支付手续费不退）')
    expect(archivedRefundText(0)).toBe('付的钱已转入钱包余额')
    expect(archivedRefundText(undefined)).toBe('应付金额已转入钱包余额，支付手续费不退')
    expect(archivedPaidText({ surcharge_kept: 0 })).toBe('套餐已停售，没有开通；付的钱已转入钱包余额。')
    expect(isPlanArchivedRefund({ status: 'cancelled', cancel_reason: 'plan_archived' })).toBe(true)
    expect(isPlanArchivedRefund({ status: 'cancelled', cancel_reason: 'user_cancelled' })).toBe(false)
    expect(isPlanArchivedRefund({ status: 'expired', cancel_reason: 'plan_archived' })).toBe(false)
  })

  it('order_pending 按 fields 认：order_id 与 lapsed，不靠文案', () => {
    const pending = new ApiError({ status: 409, code: 'order_pending', message: '你有一张还没付款的「标准版」订单', fields: { order_id: 'o1' } })
    expect(refusalOf(pending, 'order_pending')).toEqual({ kind: 'order_pending', text: pending.message, orderId: 'o1', lapsed: false })
    const lapsed = new ApiError({ status: 409, code: 'order_pending', message: '随便什么话', fields: { order_id: 'o2', lapsed: 'true' } })
    expect(refusalOf(lapsed, 'order_pending')).toMatchObject({ orderId: 'o2', lapsed: true })
    expect(refusalOf(new Error(''), null)).toEqual({ kind: 'other', text: '没下成单，请稍后再试', lapsed: false })
  })
})
