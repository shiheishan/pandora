import { describe, expect, it } from 'vitest'
import { expiryText, isExpiredNow, leakSources, usageText } from './card-text'
import { appLink, detectDevice, guessDeviceFromName, protocolLabel, rateLabel, shareMessage } from './clients'
import { throttleNote } from './catalog'
import { createPlacedOrder, recallPayable } from './intent'
import { ApiError } from '../../../core/api'
import { orderQuerySchema, queryFailure, queryOutcome } from './order-query'
import { expiryNote, intervalLabel, isPayable, orderRowSchema, orderTitle } from './orders'
import { canRenew, pickPrimary, subscriptionSchema, usageReportSchema, type Subscription } from './subscriptions'
import { buildUsageBars, bytesParts, expiryInfo, pickTrafficQuota, projectUsage, resetAtOf, trafficSummary, usageLevel } from './traffic'

const NOW = new Date('2026-09-24T12:00:00Z')
const GIB = 1024 ** 3

// 字段与 Go 的 mySubscriptionView 一一对应；renewable 同口径：生效状态即可续费
function sub(over: Partial<Subscription> = {}): Subscription {
  const status = over.status ?? 'active'
  return subscriptionSchema.parse({
    id: 's1',
    plan_id: 'p1',
    price_id: '',
    plan_name: '专业版',
    plan_version: 3,
    status,
    current_period_start: '2026-09-01T00:00:00Z',
    current_period_end: '2026-11-05T10:00:00Z',
    currency: 'CNY',
    amount: 5900,
    quotas: [{ metric: 'traffic.bytes', limit: 500 * GIB, consumed: 312 * GIB, remaining: 188 * GIB, period: 'cycle', period_start: '2026-09-01T00:00:00Z', period_end: null, granted_addon: 0, adjusted: 0 }],
    device_limit: null,
    online_devices: 0,
    quota_reset_strategy: 'billing_cycle',
    next_reset_at: null,
    renewable: ['active', 'trialing', 'grace', 'past_due'].includes(status),
    renewal_price: null,
    pack_remaining_bytes: 0,
    label: null,
    client_name: 'Pandora · 专业版',
    changeable: ['active', 'trialing', 'grace', 'past_due'].includes(status),
    renew_until: null,
    legacy_movable_pack_bytes: 0,
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

  it('摘要：总量 = 已用 + 剩余，remaining=null 为不限', () => {
    expect(trafficSummary({ metric: 'traffic.bytes', limit: 500, consumed: 400, remaining: 100 }, 30)).toEqual({ used: 400, total: 500, remaining: 100, pack: 30, ratio: 0.8 })
    expect(trafficSummary({ metric: 'traffic.bytes', limit: null, consumed: 7, remaining: null })).toMatchObject({ total: null, remaining: null, ratio: 0 })
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

  it('用量等级：80% 警示、95% 危险', () => {
    expect(usageLevel(0.79)).toBe('ok')
    expect(usageLevel(0.8)).toBe('warn')
    expect(usageLevel(0.95)).toBe('danger')
  })

  it('到期：向上取整天数，7 天内为紧急，时刻精确到分钟、按切日时区', () => {
    expect(expiryInfo('2026-09-30T00:00:00Z', NOW)).toMatchObject({ days: 6, urgent: true, expired: false, label: '6 天后到期' })
    expect(expiryInfo('2026-11-05T10:00:00Z', NOW)).toMatchObject({ days: 42, urgent: false })
    expect(expiryInfo(null, NOW)).toBeNull()
    // 给了切日时区就按它出时刻：上海 09-27 零点
    expect(expiryInfo('2026-09-26T16:00:00Z', NOW, 'Asia/Shanghai')?.date).toBe('2026-09-27 00:00')
    expect(expiryInfo('2026-09-26T16:00:00Z', NOW, 'UTC')?.date).toBe('2026-09-26 16:00')
  })

  it('到期：最后 24 小时写「还剩 X 小时」，过了写「已于 … 到期」（w5expiry）', () => {
    const in5h = new Date(NOW.getTime() + 4.5 * 3_600_000).toISOString()
    expect(expiryInfo(in5h, NOW, 'UTC')).toMatchObject({ hours: 5, urgent: true, expired: false, label: '还剩 5 小时' })
    const past = expiryInfo(new Date(NOW.getTime() - 3_600_000).toISOString(), NOW, 'UTC')
    expect(past).toMatchObject({ hours: 0, urgent: true, expired: true })
    expect(past?.label).toBe(`已于 ${past?.date} 到期`)
  })

  it('重置日：never 不重置；next_reset_at → 额度周期末 → 用量窗口末', () => {
    expect(resetAtOf({ quota_reset_strategy: 'never', next_reset_at: 'x' }, null, undefined)).toBeNull()
    expect(resetAtOf({ next_reset_at: 'a' }, { period_end: 'b' }, { period_end: 'c' })).toBe('a')
    expect(resetAtOf({}, { period_end: null }, { period_end: 'c' })).toBe('c')
    expect(resetAtOf({}, null, undefined)).toBeNull()
  })

  it('预测：够用 / 比重置早用完（含流量包）/ 不限 / 无用量', () => {
    const s = trafficSummary({ metric: 'traffic.bytes', limit: 500 * GIB, consumed: 312 * GIB, remaining: 188 * GIB })!
    const reset = '2026-09-27T12:00:00Z' // 3 天后
    expect(projectUsage(s, 11 * GIB, reset, NOW)).toEqual({ text: '按目前的速度，本期预计用到 345 GB，够用。', short: false })
    const tight = trafficSummary({ metric: 'traffic.bytes', limit: 100 * GIB, consumed: 90 * GIB, remaining: 10 * GIB }, 2 * GIB)!
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
    expect(orderTitle({ kind: 'addon', plan_name: '100 GB', item_name: '100 GB' })).toBe('流量包 · 100 GB')
    expect(orderTitle({ kind: 'new', plan_name: '专业版', item_name: '专业版', interval: 'month', interval_count: 1 })).toBe('专业版 · 月付')
    expect(orderTitle({ kind: 'new', plan_name: '专业版', item_name: '专业版' })).toBe('专业版')
    expect(orderTitle({ kind: 'renewal', plan_name: '专业版', item_name: '专业版', interval: 'year', interval_count: 1 })).toBe('专业版 · 年付续费')
    expect(orderTitle({ kind: 'upgrade', plan_name: '家庭版', item_name: '家庭版' })).toBe('家庭版 · 变更套餐')
  })

  it('待支付期限按 expires_at 倒数', () => {
    expect(expiryNote('2026-09-24T12:17:30Z', NOW)).toBe('18 分钟内未支付将自动取消')
    expect(expiryNote('2026-09-24T11:59:00Z', NOW)).toBe('即将自动取消')
    expect(expiryNote(undefined, NOW)).toBe('请尽快完成支付')
  })

  it('订单行 schema：无 omitempty 的字段必填，omitempty 的可缺席，未知 kind 拒收', () => {
    const row = { id: 'o', order_no: 'PD-1', kind: 'addon', status: 'pending_payment', currency: 'CNY', total_amount: 1, discount_amount: 0, balance_applied: 0, payable_amount: 1, paid_amount: 0, refunded_amount: 0, item_name: '', cancellable: true, has_payment_intent: false, created_at: 'x' }
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
    delete missing.pack_remaining_bytes
    expect(subscriptionSchema.safeParse(missing).success).toBe(false)
    expect(subscriptionSchema.safeParse({ ...sub(), status: 'weird' }).success).toBe(false)
    expect(subscriptionSchema.safeParse({ ...sub(), quotas: null }).success).toBe(false)
  })

  it('主订阅：生效状态中 current_period_end 最晚的一条', () => {
    const list = [
      sub({ id: 'old', status: 'expired', current_period_end: '2027-01-01T00:00:00Z' }),
      sub({ id: 'a', current_period_end: '2026-10-01T00:00:00Z' }),
      sub({ id: 'b', status: 'past_due', current_period_end: '2026-12-01T00:00:00Z' }),
    ]
    expect(pickPrimary(list)?.id).toBe('b')
    expect(pickPrimary([sub({ status: 'cancelled' })])).toBeNull()
  })

  it('主订阅：没有生效订阅时回落到能原地续费的已过期订阅（w5expiry）', () => {
    const expired = [
      sub({ id: 'closed', status: 'expired', renewable: false, current_period_end: '2026-09-01T00:00:00Z' }),
      sub({ id: 'older', status: 'expired', renewable: true, current_period_end: '2026-09-10T00:00:00Z' }),
      sub({ id: 'newer', status: 'expired', renewable: true, current_period_end: '2026-09-20T00:00:00Z' }),
    ]
    expect(pickPrimary(expired)?.id).toBe('newer')
    expect(pickPrimary([expired[0]!])).toBeNull()
    expect(pickPrimary([...expired, sub({ id: 'live' })])?.id).toBe('live')
  })

  it('续费入口：取服务端的 renewable', () => {
    expect(canRenew(sub())).toBe(true)
    expect(canRenew(sub({ renewable: false }))).toBe(false)
    expect(canRenew(sub({ status: 'paused' }))).toBe(false)
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
    expect(isExpiredNow(sub({ status: 'expired' }), NOW)).toBe(true)
    expect(isExpiredNow(sub({ current_period_end: '2026-09-20T08:30:00Z' }), NOW)).toBe(true)
    expect(isExpiredNow(sub(), NOW)).toBe(false)
  })

  it('卡片用量：剩余含这一份的流量包，低于 15% 标快用完了', () => {
    expect(usageText(sub())).toMatchObject({ label: '本期流量', right: '剩 188G / 共 500G', pct: 62, low: false })
    const low = sub({ quotas: [{ metric: 'traffic.bytes', limit: 100 * GIB, consumed: 92 * GIB, remaining: 8 * GIB, period: 'month', period_start: '2026-09-01T00:00:00Z', period_end: null, granted_addon: 0, adjusted: 0 }] })
    expect(usageText(low)).toMatchObject({ label: '本月流量 · 快用完了', right: '剩 8G / 共 100G', low: true })
    expect(usageText({ ...low, pack_remaining_bytes: 30 * GIB }).right).toBe('剩 38G / 共 130G（含流量包 30G）')
  })

  it('疑似泄露：近 24 小时来源超过设备上限，过期的不提示', () => {
    const l = { subscription_id: 's1', url: 'u', expires_at: null, fetch_count: 12, last_fetched_at: null, distinct_sources_24h: 7, expired: false }
    expect(leakSources(sub({ device_limit: 2 }), l)).toBe(7)
    expect(leakSources(sub({ device_limit: 8 }), l)).toBeNull()
    expect(leakSources(sub({ device_limit: null }), l)).toBeNull()
    expect(leakSources(sub({ device_limit: 2, status: 'expired' }), l)).toBeNull()
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

  it('可支付：只有 draft / pending_payment 且未到 expires_at', () => {
    const at = Date.parse('2026-09-24T12:00:00Z')
    expect(isPayable({ status: 'pending_payment', expires_at: '2026-09-24T12:10:00Z' }, at)).toBe(true)
    expect(isPayable({ status: 'draft', expires_at: undefined }, at)).toBe(true)
    expect(isPayable({ status: 'pending_payment', expires_at: '2026-09-24T11:59:59Z' }, at)).toBe(false)
    for (const status of ['processing', 'paid', 'fulfilled', 'cancelled', 'expired', 'refunded'] as const) expect(isPayable({ status, expires_at: undefined }, at)).toBe(false)
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
    expect(queryOutcome({ ...r, channel_status: 'unpaid', reconciled: false, order_status: 'pending_payment' }).kind).toBe('pending')
    expect(queryOutcome({ ...r, channel_status: 'not_found', reconciled: false, order_status: 'pending_payment' }).kind).toBe('pending')
    expect(queryFailure(new ApiError({ status: 409, code: 'conflict', message: '该订单从未发起过支付，无法向渠道查单' }))).toEqual({ kind: 'failed', text: '查询失败：该订单从未发起过支付，无法向渠道查单' })
    expect(queryFailure(new ApiError({ status: 429, code: 'rate_limited', message: 'x' })).text).toContain('太频繁')
    expect(queryFailure(new ApiError({ status: 0, code: 'network_error', message: 'x' })).text).toContain('网络')
  })
})
