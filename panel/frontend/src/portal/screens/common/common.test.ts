/**
 * [INPUT]: 依赖 vitest，依赖 ../../../core/api 的 ApiError，依赖同目录 traffic / orders / clients / subscriptions / intent 的纯函数与 schema，依赖 ../subs/labels
 * [OUTPUT]: 无（测试文件）
 * [POS]: portal/screens/common 与 subs 文案映射的单元测试：流量摘要与预测、用量柱、到期、订单标题与期限、深链与协议名、主订阅选择、schema 对契约形状（含待补字段缺席）的收放、幂等键的复用与丢弃（成功或 4xx reset、断网与 5xx 保留）与刚下待支付单的取回（已不可支付即 forget、按新请求下单）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it } from 'vitest'
import { fetchStats, metaLabel } from '../subs/labels'
import { ApiError } from '../../../core/api'
import { importClients, protocolLabel, rateLabel } from './clients'
import { createIntentKey, createPlacedOrder, endsIntent, recallPayable } from './intent'
import { expiryNote, intervalLabel, isPayable, orderRowSchema, orderTitle } from './orders'
import { canRenew, pickPrimary, subscriptionSchema, usageReportSchema, type Subscription } from './subscriptions'
import { buildUsageBars, bytesParts, expiryInfo, pickTrafficQuota, projectUsage, resetAtOf, trafficSummary, usageLevel } from './traffic'

const NOW = new Date('2026-09-24T12:00:00Z')
const GIB = 1024 ** 3

function sub(over: Partial<Subscription> = {}): Subscription {
  return subscriptionSchema.parse({
    id: 's1',
    plan_id: 'p1',
    price_id: '',
    plan_name: '专业版',
    plan_version: 3,
    status: 'active',
    current_period_start: '2026-09-01T00:00:00Z',
    current_period_end: '2026-11-05T10:00:00Z',
    currency: 'CNY',
    amount: 5900,
    quotas: [{ metric: 'traffic.bytes', limit: 500 * GIB, consumed: 312 * GIB, remaining: 188 * GIB }],
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

  it('到期：向上取整天数，7 天内为紧急，日期按切日时区', () => {
    expect(expiryInfo('2026-09-30T00:00:00Z', NOW)).toMatchObject({ days: 6, urgent: true })
    expect(expiryInfo('2026-11-05T10:00:00Z', NOW)).toMatchObject({ days: 42, urgent: false })
    expect(expiryInfo(null, NOW)).toBeNull()
    // 给了切日时区就按它出日期：上海 09-27 零点
    expect(expiryInfo('2026-09-26T16:00:00Z', NOW, 'Asia/Shanghai')?.date).toBe('2026-09-27')
    expect(expiryInfo('2026-09-26T16:00:00Z', NOW, 'UTC')?.date).toBe('2026-09-26')
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

describe('orders', () => {
  it('周期文案', () => {
    expect(intervalLabel('month', 1)).toBe('月付')
    expect(intervalLabel('month', 3)).toBe('季付')
    expect(intervalLabel('year', 1)).toBe('年付')
    expect(intervalLabel('month', 2)).toBe('2 个月')
    expect(intervalLabel(undefined)).toBe('')
  })

  it('标题按 kind 映射，待补字段缺席时退回 plan_name', () => {
    expect(orderTitle({ kind: 'topup' })).toBe('余额充值')
    expect(orderTitle({ kind: 'addon', plan_name: '100 GB' })).toBe('流量包 · 100 GB')
    expect(orderTitle({ kind: 'new', plan_name: '专业版', interval: 'month', interval_count: 1 })).toBe('专业版 · 月付')
    expect(orderTitle({ kind: 'new', plan_name: '专业版' })).toBe('专业版')
    expect(orderTitle({ kind: 'renewal', plan_name: '专业版', interval: 'year', interval_count: 1 })).toBe('专业版 · 年付续费')
    expect(orderTitle({ kind: 'upgrade', plan_name: '家庭版' })).toBe('家庭版 · 变更套餐')
  })

  it('待支付期限按 expires_at 倒数', () => {
    expect(expiryNote('2026-09-24T12:17:30Z', NOW)).toBe('18 分钟内未支付将自动取消')
    expect(expiryNote('2026-09-24T11:59:00Z', NOW)).toBe('即将自动取消')
    expect(expiryNote(undefined, NOW)).toBe('请尽快完成支付')
  })

  it('订单行 schema：现有字段必填，待补字段可缺席，未知 kind 拒收', () => {
    const row = { id: 'o', order_no: 'PD-1', kind: 'addon', status: 'pending_payment', currency: 'CNY', total_amount: 1, discount_amount: 0, balance_applied: 0, payable_amount: 1, paid_amount: 0, refunded_amount: 0, cancellable: true, created_at: 'x' }
    expect(orderRowSchema.safeParse(row).success).toBe(true)
    expect(orderRowSchema.safeParse({ ...row, kind: 'gift' }).success).toBe(false)
    const missing: Partial<typeof row> = { ...row }
    delete missing.cancellable
    expect(orderRowSchema.safeParse(missing).success).toBe(false)
  })
})

describe('subscriptions', () => {
  it('现有形状可解析，待补字段缺席不报错；状态枚举外的值拒收', () => {
    expect(sub().device_limit).toBeUndefined()
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

  it('续费入口：renewable 优先，缺席时按生效状态', () => {
    expect(canRenew(sub())).toBe(true)
    expect(canRenew(sub({ renewable: false }))).toBe(false)
    expect(canRenew(sub({ status: 'paused' }))).toBe(false)
  })

  it('按日用量 schema 与契约一致', () => {
    const ok = { timezone: 'UTC', period_start: 'x', period_end: null, days: [{ date: '2026-09-24', bytes: 1 }], today_bytes: 1, avg_daily_bytes: 1 }
    expect(usageReportSchema.safeParse(ok).success).toBe(true)
    expect(usageReportSchema.safeParse({ ...ok, avg_daily_bytes: 1.5 }).success).toBe(false)
  })

  it('头部元信息：待补字段缺席时省掉设备段', () => {
    expect(metaLabel(sub(), NOW)).toBe('42 天后到期 · 2026-11-05')
    expect(metaLabel(sub({ device_limit: 5, online_devices: 3 }), NOW)).toBe('42 天后到期 · 2026-11-05 · 5 台设备 · 当前在线 3')
    expect(metaLabel(sub({ current_period_end: null, device_limit: null }), NOW)).toBe('长期有效 · 不限设备')
  })

  it('拉取统计：相对时间加「前」，来源数超过设备上限提示泄露', () => {
    const link = { subscription_id: 's1', url: 'u', expires_at: null, fetch_count: 12, last_fetched_at: '2026-09-24T11:54:00Z', distinct_sources_24h: 7 }
    expect(fetchStats(link, 5, NOW)).toEqual({ text: '已被拉取 12 次 · 最近 6 分钟前 · 近 24 小时 7 个来源', leak: true })
    expect(fetchStats({ ...link, last_fetched_at: null }, null, NOW)).toEqual({ text: '已被拉取 12 次 · 还没有被拉取过 · 近 24 小时 7 个来源', leak: false })
    expect(fetchStats(link, undefined, NOW).leak).toBe(false)
  })
})

describe('clients', () => {
  it('深链带原地址，v2rayN 无 scheme', () => {
    const url = 'https://sub.example.com/s/abc?x=1'
    const apps = Object.fromEntries(importClients(url, 'Pandora').map((c) => [c.name, c.href]))
    expect(apps['Clash Verge']).toBe(`clash://install-config?url=${encodeURIComponent(url)}&name=Pandora`)
    expect(apps['Shadowrocket']).toBe(`shadowrocket://add/sub://${btoa(url)}?remark=Pandora`)
    expect(apps['sing-box']).toBe(`sing-box://import-remote-profile?url=${encodeURIComponent(url)}#Pandora`)
    expect(apps['v2rayN']).toBeNull()
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
// 幂等键口径（协调会话定）：成功或 4xx 业务拒绝即 reset，断网与 5xx 保留键
// ---------------------------------------------------------------------------
describe('intent', () => {
  it('同样的请求复用键、改参数换键，reset 后同样的请求也换键', () => {
    let n = 0
    const key = createIntentKey(() => `k${++n}`)
    expect(key({ amount: 1 })).toBe('k1')
    expect(key({ amount: 1 })).toBe('k1')
    expect(key({ amount: 2 })).toBe('k2')
    key.reset()
    expect(key({ amount: 2 })).toBe('k3')
  })

  it('4xx 结束意图；断网（status 0）、5xx 与非 ApiError 保留键', () => {
    const err = (status: number) => new ApiError({ status, code: status === 0 ? 'network_error' : status >= 500 ? 'internal_error' : 'conflict', message: 'x' })
    expect([400, 404, 409, 422, 429].map((s) => endsIntent(err(s)))).toEqual([true, true, true, true, true])
    expect([0, 500, 503].map((s) => endsIntent(err(s)))).toEqual([false, false, false])
    expect(endsIntent(new Error('x'))).toBe(false)
  })

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
