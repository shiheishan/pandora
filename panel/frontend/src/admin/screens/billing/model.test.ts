import { describe, expect, it } from 'vitest'
import { ApiError } from '../../../core/api'
import { classifyFailure } from '../../actions'
import type { PlanRow, PriceRow } from '../plans/schemas'
import {
  adjustBody,
  adjustmentView,
  adjustProblems,
  ageDays,
  belowMinimumText,
  canCancel,
  canMarkPaid,
  canQueryChannel,
  channelLabel,
  effectiveMethods,
  emptyAdjust,
  emptyManual,
  emptyProviderForm,
  filterStatuses,
  isEditableProvider,
  isOrderFilter,
  lateReason,
  manualBelowMinimum,
  manualBody,
  manualCreatedToast,
  manualProblems,
  minAmountHint,
  minAmountText,
  orderFacts,
  paymentLines,
  parseMinAmount,
  pendingTotals,
  previewParams,
  priceChoices,
  providerBody,
  providerFormFrom,
  providerMode,
  providerProblems,
  queriedView,
  providerNote,
  rateLabel,
  reasonProblem,
  referenceProblem,
  reverseReason,
  todayLabel,
  todayLocal,
  toggleBody,
  toggleMethod,
} from './model'
import { adjustmentSchema, cancelledSchema, latePaymentsSchema, manualCreatedSchema, markedPaidSchema, orderDetailSchema, orderQueriedSchema, type OrderDetail, type PaymentHistory, type Provider } from './schemas'

const row = { provider_name: null, balance_applied: 0, total_amount: 2500 }

describe('orders', () => {
  it('maps the status segments to the multi-value status query (R63)', () => {
    expect(filterStatuses('pending')).toBe('draft,pending_payment,processing')
    expect(filterStatuses('paid')).toBe('paid,fulfilled')
    expect(filterStatuses('cancelled')).toBe('cancelled,expired')
    expect(filterStatuses('all')).toBeUndefined()
    expect(isOrderFilter('paid')).toBe(true)
    expect(isOrderFilter('refunded')).toBe(false)
    expect(isOrderFilter(null)).toBe(false)
  })

  it('falls back from the provider to balance, manual or a dash', () => {
    expect(channelLabel({ ...row, provider_name: '聚合收银台' })).toBe('聚合收银台')
    expect(channelLabel({ ...row, balance_applied: 2500 })).toBe('余额')
    expect(channelLabel({ ...row, balance_applied: 1000 })).toBe('—')
    expect(channelLabel(row, true)).toBe('人工')
    expect(channelLabel({ ...row, total_amount: 0 })).toBe('—')
  })

  it('only offers mark-paid and cancel on unpaid orders', () => {
    expect(canMarkPaid({ status: 'pending_payment', payable_amount: 100 })).toBe(true)
    expect(canMarkPaid({ status: 'processing', payable_amount: 100 })).toBe(true)
    expect(canMarkPaid({ status: 'draft', payable_amount: 100 })).toBe(false)
    expect(canMarkPaid({ status: 'pending_payment', payable_amount: 0 })).toBe(false)
    expect(canCancel({ status: 'draft' })).toBe(true)
    expect(canCancel({ status: 'paid' })).toBe(false)
    expect(canCancel({ status: 'expired' })).toBe(false)
  })

  const detail = (over: Partial<OrderDetail> = {}): OrderDetail =>
    orderDetailSchema.parse({
      id: 'o1',
      order_no: 'PD2609230001',
      user_email: 'a@b.c',
      kind: 'new',
      status: 'fulfilled',
      currency: 'CNY',
      total_amount: 0,
      payable_amount: 0,
      paid_amount: 0,
      refunded_amount: 0,
      balance_applied: 0,
      created_at: '2026-09-23T10:00:00Z',
      paid_at: null,
      provider_code: null,
      provider_name: null,
      plan_name: '专业版',
      interval: 'month',
      interval_count: 1,
      item_count: 1,
      manual: true,
      user_id: 'u1',
      organization_id: null,
      state_version: 3,
      subtotal_amount: 4500,
      discount_amount: 4500,
      tax_amount: 0,
      coupon_id: null,
      manual_reason: '补偿一个月服务',
      created_by: 'a1',
      created_by_email: 'ops@pandora.dev',
      subscription_id: null,
      expires_at: null,
      fulfilled_at: null,
      cancelled_at: null,
      expired_at: null,
      cancel_reason: null,
      updated_at: '2026-09-23T10:00:00Z',
      items: [],
      ...over,
    })

  it('builds the drawer facts with source and manual channel', () => {
    const facts = new Map(orderFacts(detail()))
    expect(facts.get('来源')).toBe('人工开单 · ops@pandora.dev')
    expect(facts.get('渠道')).toBe('人工')
    expect(facts.get('内容')).toBe('专业版 · 月付')
    expect(facts.get('优惠')).toBe('− ¥45.00')
    expect(facts.get('开单原因')).toBe('补偿一个月服务')
    const portal = new Map(orderFacts(detail({ manual_reason: null, created_by_email: null, status: 'pending_payment', expires_at: '2026-09-23T10:30:00Z' })))
    expect(portal.get('来源')).toBe('门户下单')
    expect(portal.get('渠道')).toBe('—')
    expect(portal.has('支付截止')).toBe(true)
    expect(new Map(orderFacts(detail({ created_by_email: null }))).get('来源')).toBe('人工开单 · 已删除的管理员')
  })

  it('merges intents with their payments, offline receipts and refunds into one list', () => {
    const h: PaymentHistory = {
      payment_intents: [
        { id: 'i1', provider_code: 'epay', provider_name: '聚合收银台', currency: 'CNY', amount: 2500, status: 'failed', provider_ref: null, failure_code: 'x', failure_message: '用户取消', expires_at: null, created_at: '2026-09-23T10:00:00Z', updated_at: '2026-09-23T10:00:00Z' },
        { id: 'i2', provider_code: 'epay', provider_name: '聚合收银台', currency: 'CNY', amount: 2500, status: 'succeeded', provider_ref: 'EP1', failure_code: null, failure_message: null, expires_at: null, created_at: '2026-09-23T10:05:00Z', updated_at: '2026-09-23T10:05:00Z' },
      ],
      payments: [
        { id: 'p1', payment_intent_id: 'i2', provider_code: 'epay', provider_name: '聚合收银台', provider_payment_id: 'TX9', currency: 'CNY', amount: 2500, fee_amount: 15, refunded_amount: 500, status: 'partially_refunded', method: null, paid_at: '2026-09-23T10:06:00Z' },
        { id: 'p2', payment_intent_id: null, provider_code: 'offline', provider_name: '线下收款', provider_payment_id: 'offline:ICBC-1', currency: 'CNY', amount: 100, fee_amount: 0, refunded_amount: 0, status: 'succeeded', method: null, paid_at: '2026-09-23T11:00:00Z' },
      ],
      refunds: [{ id: 'r1', payment_id: 'p1', provider_refund_id: null, currency: 'CNY', amount: 500, reason: '补差', status: 'processing', entitlement_revoked: false, commission_reversed: false, failure_message: null, succeeded_at: null, created_at: '2026-09-23T12:00:00Z' }],
    }
    expect(paymentLines(h).map((l) => [l.channel, l.ref, l.amount, l.label])).toEqual([
      ['聚合收银台', '用户取消', 2500, '失败'],
      ['聚合收银台', 'TX9', 2500, '部分退款'],
      ['线下收款', 'offline:ICBC-1', 100, '人工确认'],
      ['退款', '补差', -500, '退款处理中'],
    ])
    expect(paymentLines({ payment_intents: [], payments: [], refunds: [] })).toEqual([])
  })
})

describe('write forms', () => {
  it('checks reasons and references by characters like Go', () => {
    expect(reasonProblem('补偿', '开单原因')).toContain('至少 5 个字')
    expect(reasonProblem('  补偿一个月  ', '开单原因')).toBeNull()
    expect(reasonProblem('字'.repeat(501), '开单原因')).toContain('最多 500')
    expect(referenceProblem('   ')).toContain('凭证号')
    expect(referenceProblem('a'.repeat(129))).toContain('128')
    expect(referenceProblem(' ICBC-1 ')).toBeNull()
  })

  const price = (over: Partial<PriceRow>): PriceRow => ({ id: 'p', currency: 'CNY', unit_amount: 2500, billing_interval: 'month', interval_count: 1, trial_days: 0, status: 'active', user_group_id: null, valid_from: null, valid_until: null, row_version: 1, ...over })
  const plan = (over: Partial<PlanRow>): PlanRow => ({
    id: 'pl',
    product_id: 'pr',
    row_version: 1,
    code: 'std',
    name: '标准版',
    description: null,
    status: 'active',
    visibility: 'public',
    sort_order: 1,
    current_version_id: 'v1',
    draft_version_id: null,
    version: 1,
    max_devices: 3,
    traffic_limit: null,
    prices: [],
    active_subscriptions: 0,
    node_count: 1,
    highlights: [],
    recommended: false,
    ...over,
  })

  it('offers only active prices of published plans', () => {
    const choices = priceChoices([
      plan({ prices: [price({ id: 'a' }), price({ id: 'b', status: 'archived' }), price({ id: 'c', billing_interval: 'year', unit_amount: 19900, user_group_id: 'g', trial_days: 3 })] }),
      plan({ id: 'draft', status: 'draft', prices: [price({ id: 'd' })] }),
      plan({ id: 'nover', current_version_id: null, prices: [price({ id: 'e' })] }),
    ])
    expect(choices.map((c) => c.value)).toEqual(['pl:a', 'pl:c'])
    expect(choices[0]!.planName).toBe('标准版')
    expect(choices[0]!.label).toBe('标准版 · 每月 ¥25.00')
    expect(choices[1]!.label).toContain('（组专属，试用 3 天）')
  })

  it('validates the manual order and only sends reference for offline', () => {
    expect(Object.keys(manualProblems(emptyManual(), null))).toEqual(['user_id', 'price_id', 'reason'])
    const form = { ...emptyManual({ id: 'u1', email: 'a@b.c' }), choice: 'pl:a', reason: '对公转账客户开单', reference: 'X' }
    const target = { kind: 'new' as const }
    expect(manualProblems(form, target)).toEqual({})
    expect(manualBody(form, target)).toEqual({ user_id: 'u1', plan_id: 'pl', price_id: 'a', reason: '对公转账客户开单', settlement: 'pending', target: { kind: 'new' } })
    const offline = { ...form, settlement: 'offline' as const, reference: '' }
    expect(manualProblems(offline, target)).toHaveProperty('reference')
    expect(manualBody({ ...offline, reference: ' ICBC-1 ' }, target)).toMatchObject({ settlement: 'offline', reference: 'ICBC-1' })
  })

  it('refuses to submit without a placement once user and price are chosen', () => {
    const form = { ...emptyManual({ id: 'u1', email: 'a@b.c' }), choice: 'pl:a', reason: '对公转账客户开单' }
    expect(manualProblems(form, null)).toEqual({ target: '先选这单落到哪一份' })
    expect(manualBody(form, null)).not.toHaveProperty('target')
    // 还没选价格时先提示价格，不提示落点
    expect(manualProblems({ ...form, choice: '' }, null)).toEqual({ price_id: '选择套餐与周期' })
    expect(manualBody(form, { kind: 'renew', subscription_id: 's1' })).toHaveProperty('target', { kind: 'renew', subscription_id: 's1' })
  })

  it('asks for the placement preview only when user, plan and price are all chosen', () => {
    const form = { ...emptyManual({ id: 'u1', email: 'a@b.c' }), choice: 'pl:a' }
    expect(previewParams(emptyManual(), null)).toBeNull()
    expect(previewParams({ ...form, choice: '' }, null)).toBeNull()
    expect(previewParams({ ...form, user: null }, null)).toBeNull()
    expect(previewParams(form, null)).toEqual({ user_id: 'u1', plan_id: 'pl', price_id: 'a' })
    expect(previewParams(form, 'sub1')).toEqual({ user_id: 'u1', plan_id: 'pl', price_id: 'a', entry_subscription_id: 'sub1' })
  })

  it('validates a revenue adjustment and omits an empty effective date', () => {
    const today = '2026-09-24'
    expect(Object.keys(adjustProblems(emptyAdjust(), today))).toEqual(['amount', 'reason'])
    const f = { ...emptyAdjust(), amount: '-12.5', reason: '重复扣款退回' }
    expect(adjustProblems(f, today)).toEqual({})
    expect(adjustBody(f)).toEqual({ currency: 'CNY', amount: -1250, reason: '重复扣款退回' })
    expect(adjustProblems({ ...f, effectiveOn: '2026-09-25' }, today)).toHaveProperty('effective_on')
    expect(adjustBody({ ...f, effectiveOn: '2026-09-01' })).toHaveProperty('effective_on', '2026-09-01')
    expect(adjustProblems({ ...f, amount: '20000000000.01' }, today)).toHaveProperty('amount')
    expect(todayLocal(new Date(2026, 0, 5))).toBe('2026-01-05')
  })
})

describe('late payments, providers and adjustments', () => {
  it('words late cases as money owed to the user and totals per currency (R3)', () => {
    expect(lateReason({ case_kind: 'released_order', order_no: 'PD1' })).toBe('订单取消后到账 · PD1')
    expect(lateReason({ case_kind: 'excess_capture', order_no: 'PD2' })).toBe('超额扣款 · PD2')
    expect(lateReason({ case_kind: 'ineligible_subscription', order_no: 'PD3' })).toBe('订阅已结束后到账 · PD3')
    expect(pendingTotals({ USD: 120, CNY: 700000, EUR: 0 })).toEqual(['¥7,000.00', '$1.20'])
    expect(pendingTotals({})).toEqual([])
    expect(ageDays('2026-09-20T12:00:00Z', new Date('2026-09-24T11:00:00Z'))).toBe(3)
  })

  const provider = (over: Partial<Provider>): Provider => ({
    id: 'x',
    code: 'epay',
    adapter: 'epay',
    display_name: '聚合收银台',
    enabled: true,
    accepting_new: true,
    has_credentials: true,
    base_url: '',
    currencies: ['CNY'],
    submit_path: '/submit.php',
    api_path: '/api.php',
    methods: ['alipay', 'wxpay'],
    default_method: 'alipay',
    allow_private_host: false,
    min_amount: 100,
    today: {},
    success_rate_24h: null,
    last_callback_at: null,
    ...over,
  })

  it('maps the switch to accepting_new and full stop to enabled (PAY-009)', () => {
    expect(providerMode(provider({}))).toBe('on')
    expect(providerMode(provider({ accepting_new: false }))).toBe('paused')
    expect(providerMode(provider({ enabled: false, accepting_new: true }))).toBe('off')
    expect(toggleBody('on')).toEqual({ enabled: true, accepting_new: true })
    expect(toggleBody('paused')).toEqual({ enabled: true, accepting_new: false })
    expect(toggleBody('off')).toEqual({ enabled: false, accepting_new: false })
  })

  it('formats the provider card stats and note', () => {
    const now = new Date('2026-09-24T12:00:00Z')
    expect(todayLabel({ CNY: 581200, USD: 68400 })).toBe('¥5,812.00 · $684.00')
    expect(todayLabel({})).toBe('—')
    expect(rateLabel(0.9724)).toBe('97.2%')
    expect(rateLabel(null)).toBe('—')
    expect(providerNote(provider({ accepting_new: false, has_credentials: false }), now)).toEqual({ text: '已停止新单，进行中的支付仍会回调 · 未配置凭据 · 最低付款 ¥1.00 · 尚无回调', tone: 'warn' })
    expect(providerNote(provider({ last_callback_at: '2026-09-24T11:50:00Z' }), now)).toEqual({ text: '最低付款 ¥1.00 · 最近回调 10 分钟前', tone: 'neutral' })
    // 没配最低额（0）的渠道不写这一项
    expect(providerNote(provider({ min_amount: 0, last_callback_at: '2026-09-24T11:50:00Z' }), now).text).toBe('最近回调 10 分钟前')
    expect(providerNote(provider({ enabled: false }), now).tone).toBe('danger')
    expect(providerNote(provider({ code: 'offline' }), now).text).toContain('系统内置')
  })

  it('prefills the provider form without secrets and falls back to default_method (w2pay)', () => {
    expect(effectiveMethods(provider({ methods: [], default_method: 'alipay' }))).toEqual(['alipay'])
    expect(effectiveMethods(provider({ methods: [], default_method: '' }))).toEqual([])
    expect(isEditableProvider(provider({}))).toBe(true)
    expect(isEditableProvider(provider({ code: 'offline', adapter: 'offline' }))).toBe(false)
    expect(isEditableProvider(provider({ code: 'demo', adapter: 'demo_hmac' }))).toBe(false)
    const legacy = providerFormFrom(provider({ methods: [], default_method: 'wxpay', submit_path: '', base_url: 'https://pay.example.com' }))
    expect(legacy).toMatchObject({ methods: ['wxpay'], default_method: 'wxpay', submit_path: '/submit.php', merchant_id: '', key: '' })
  })

  it('edits the minimum payment in yuan and sends it in cents (min_amount)', () => {
    expect(parseMinAmount('1')).toBe(100)
    expect(parseMinAmount(' ¥0.50 ')).toBe(50)
    expect(parseMinAmount('1000')).toBe(100_000)
    expect(parseMinAmount('1000.01')).toBeNull()
    expect(parseMinAmount('0')).toBeNull()
    expect(parseMinAmount('0.001')).toBeNull()
    expect(parseMinAmount('-1')).toBeNull()
    expect(parseMinAmount('')).toBeNull()
    expect(minAmountText(100)).toBe('1.00')
    // 新建默认 ¥1.00；编辑回填渠道现值，旧渠道没配过（0）时从默认值起步
    expect(emptyProviderForm().min_amount).toBe('1.00')
    expect(providerFormFrom(provider({ min_amount: 250 })).min_amount).toBe('2.50')
    expect(providerFormFrom(provider({ min_amount: 0 })).min_amount).toBe('1.00')
    const good = { ...emptyProviderForm(), code: 'epay2', base_url: 'https://pay.example.com', merchant_id: '1001', key: 'k' }
    expect(providerProblems({ ...good, min_amount: '0' }, 'create').min_amount).toBeDefined()
    expect(providerProblems({ ...good, min_amount: '1.5' }, 'create')).toEqual({})
    expect(providerBody({ ...good, min_amount: '1.5' }, 'create')).toMatchObject({ min_amount: 150 })
    expect(providerBody({ ...good, min_amount: '3' }, 'edit')).toMatchObject({ min_amount: 300 })
  })

  it('leaves the minimum payment out when blank: edit keeps the old value, create uses ¥1.00, never sends 0', () => {
    const good = { ...emptyProviderForm(), code: 'epay2', base_url: 'https://pay.example.com', merchant_id: '1001', key: 'k' }
    // 留空不是错：后端把不传（或 0）当作「编辑保留原值、新建用默认」
    expect(providerProblems({ ...good, min_amount: '  ' }, 'create')).toEqual({})
    expect(providerProblems({ ...good, min_amount: '' }, 'edit')).toEqual({})
    expect(providerBody({ ...good, min_amount: '' }, 'edit')).not.toHaveProperty('min_amount')
    expect(providerBody({ ...good, min_amount: '' }, 'create')).not.toHaveProperty('min_amount')
    // 写了 0 是错，不会当成「不改」悄悄发出去
    expect(providerProblems({ ...good, min_amount: '0' }, 'edit').min_amount).toBeDefined()
    expect(providerBody({ ...good, min_amount: '0' }, 'edit')).not.toHaveProperty('min_amount')
    // 文案和行为一致
    expect(minAmountHint('edit')).toContain('留空表示不改')
    expect(minAmountHint('create')).toContain('留空按默认 ¥1.00')
  })

  it('warns before submit from what preview says (due / below_minimum / min_payment), without reading channels', () => {
    const below = { due: 2500, below_minimum: true }
    expect(manualBelowMinimum('pending', below, 3000, 'CNY')).toEqual({ due: 2500, min: 3000, currency: 'CNY' })
    // 赠送、线下已收款不走在线支付
    expect(manualBelowMinimum('grant', below, 3000, 'CNY')).toBeNull()
    expect(manualBelowMinimum('offline', below, 3000, 'CNY')).toBeNull()
    // 服务端说不低于就不拦（换套餐抵完之后够门槛、或门槛 0 / 1 等于不限，都由服务端判）
    expect(manualBelowMinimum('pending', { due: 0, below_minimum: false }, 3000, 'CNY')).toBeNull()
    // 还没选落点、preview 还没回来：不拦
    expect(manualBelowMinimum('pending', undefined, 3000, 'CNY')).toBeNull()
    expect(manualBelowMinimum('pending', below, undefined, 'CNY')).toBeNull()
    expect(belowMinimumText({ due: 30, min: 100, currency: 'CNY' })).toBe('这单应付 ¥0.30，低于支付渠道的最低付款额 ¥1.00，用户没法在线付。请改用「赠送」或「线下已收款」')
  })

  it('lands the server-side below-minimum 422 on the settlement field by its fields, not its wording', () => {
    const e = new ApiError({ status: 422, code: 'validation_failed', message: '随便哪句话', fields: { settlement: '应付金额低于支付渠道的最低付款额' } })
    expect(classifyFailure(e, true)).toEqual({ kind: 'fields', fields: { settlement: '应付金额低于支付渠道的最低付款额' } })
  })

  it('keeps methods in canonical order and moves the default off an unticked method', () => {
    const f = { ...emptyProviderForm(), methods: ['wxpay'], default_method: 'wxpay' }
    expect(toggleMethod(f, 'alipay', true)).toMatchObject({ methods: ['alipay', 'wxpay'], default_method: 'wxpay' })
    expect(toggleMethod(f, 'wxpay', false)).toMatchObject({ methods: [], default_method: '' })
    expect(toggleMethod({ ...f, methods: ['alipay', 'wxpay'] }, 'wxpay', false)).toMatchObject({ methods: ['alipay'], default_method: 'alipay' })
  })

  it('validates the provider form like the backend and leaves blank secrets blank', () => {
    const good = { ...emptyProviderForm(), code: 'epay2', base_url: 'https://pay.example.com', merchant_id: '1001', key: 'k' }
    expect(providerProblems(good, 'create')).toEqual({})
    expect(providerProblems({ ...good, code: 'offline' }, 'create').code).toBeDefined()
    expect(providerProblems({ ...good, code: 'A b' }, 'create').code).toBeDefined()
    expect(providerProblems({ ...good, base_url: 'http://pay.example.com' }, 'create').base_url).toBe('站点地址必须使用 https')
    expect(providerProblems({ ...good, base_url: 'http://127.0.0.1:8080', allow_private_host: true }, 'create')).toEqual({})
    expect(providerProblems({ ...good, base_url: 'https://pay.example.com?a=1' }, 'create').base_url).toBeDefined()
    expect(providerProblems({ ...good, submit_path: '//evil.example.com/x' }, 'create').submit_path).toBeDefined()
    expect(providerProblems({ ...good, methods: [] }, 'create').methods).toBe('至少选一种支付方式')
    expect(providerProblems({ ...good, merchant_id: '', key: '' }, 'create')).toMatchObject({ merchant_id: '必填', key: '必填' })
    // 编辑：已有凭据时留空 = 不改；没有凭据时必须补齐
    expect(providerProblems({ ...good, merchant_id: '', key: '' }, 'edit', true)).toEqual({})
    expect(Object.keys(providerProblems({ ...good, merchant_id: '', key: '' }, 'edit', false))).toEqual(['merchant_id', 'key'])

    expect(providerBody(good, 'create')).toMatchObject({ code: 'epay2', adapter: 'epay', merchant_id: '1001' })
    const edit = providerBody({ ...good, merchant_id: ' ', key: '' }, 'edit')
    expect(edit).not.toHaveProperty('code')
    expect(edit).not.toHaveProperty('adapter')
    expect(edit).toMatchObject({ merchant_id: '', key: '', methods: ['alipay', 'wxpay'] })
  })

  it('shows adjustments and defaults the reversal reason', () => {
    const a = adjustmentSchema.parse({ id: 'a', currency: 'USD', amount: -1200, reason: '重复扣款退回', effective_on: '2026-09-18', created_by: 'u', created_by_email: null, created_at: '2026-09-18T10:40:00Z', reversed: false })
    expect(adjustmentView(a)).toMatchObject({ amount: '−$12.00', tone: 'danger', state: 'open' })
    expect(adjustmentView(a).meta).toMatch(/^已删除的账号 · /)
    expect(adjustmentView({ ...a, reversed: true }).state).toBe('reversed')
    expect(adjustmentView({ ...a, reversal_of: 'b' }).state).toBe('reversal')
    expect(reverseReason(a)).toBe('冲销：重复扣款退回')
    expect([...reverseReason({ reason: '长'.repeat(500) })]).toHaveLength(500)
  })
})

describe('schemas', () => {
  it('accepts omitempty keys as absent and rejects unknown enums', () => {
    expect(cancelledSchema.parse({ order: { order_id: 'o', status: 'cancelled', state_version: 2, already_terminal: true }, already_terminal: true }).order.cancelled_at).toBeUndefined()
    const late = { cases: [{ id: 'c', case_kind: 'released_order', status: 'suspense', amount: 100, currency: 'CNY', order_no: 'PD1', order_status: 'cancelled', user_id: 'u', user_email: 'a@b.c', received_at: '2026-09-20T00:00:00Z' }], total: 1, pending_amounts: { CNY: 100 } }
    expect(latePaymentsSchema.parse(late).cases[0]!.resolved_at).toBeUndefined()
    expect(latePaymentsSchema.safeParse({ ...late, cases: [{ ...late.cases[0], case_kind: 'overdue' }] }).success).toBe(false)
    // R3 之前的形状（只有跨币种相加的 pending_amount）判为不符
    expect(latePaymentsSchema.safeParse({ cases: [], total: 0, pending_amount: 5 }).success).toBe(false)
  })

  it('reads the snake_case mark-paid response (R2)', () => {
    expect(markedPaidSchema.safeParse({ processed: true, already_handled: false, payment_id: 'p', subscription_id: 's', ledger_txn_id: 'l' }).success).toBe(true)
    expect(markedPaidSchema.safeParse({ Processed: true, AlreadyHandled: false, PaymentID: 'p', SubscriptionID: 's', LedgerTxnID: 'l' }).success).toBe(false)
  })
})

describe('channel query (PAY-009)', () => {
  it('offers the query only on unfinished orders that went to a channel', () => {
    expect(canQueryChannel({ status: 'pending_payment', provider_code: 'epay' })).toBe(true)
    expect(canQueryChannel({ status: 'processing', provider_code: 'epay' })).toBe(true)
    // 从没发起过支付（人工开的待支付单）、线下渠道、已完结的单都不给
    expect(canQueryChannel({ status: 'pending_payment', provider_code: null })).toBe(false)
    expect(canQueryChannel({ status: 'pending_payment', provider_code: 'offline' })).toBe(false)
    for (const status of ['draft', 'paid', 'fulfilled', 'cancelled', 'expired'] as const) expect(canQueryChannel({ status, provider_code: 'epay' })).toBe(false)
  })

  it('reads the query response and says what happened', () => {
    const base = { order_id: 'o', order_no: 'PD1', provider_code: 'epay', channel_status: 'paid', reconciled: true, already_recorded: false, order_status: 'fulfilled' }
    const r = orderQueriedSchema.parse(base)
    expect(r.quarantine_kind).toBeUndefined()
    expect(queriedView(r)).toMatchObject({ tone: 'ok' })
    expect(queriedView(r).text).toContain('补记')
    expect(queriedView({ ...r, reconciled: false, already_recorded: true }).tone).toBe('info')
    const late = orderQueriedSchema.parse({ ...base, quarantine_kind: 'released_order', order_status: 'cancelled' })
    expect(queriedView(late)).toMatchObject({ tone: 'warn' })
    expect(queriedView(late).text).toContain('挂账')
    expect(queriedView({ ...r, channel_status: 'unpaid', reconciled: false, order_status: 'pending_payment' }).text).toContain('尚未付款')
    expect(queriedView({ ...r, channel_status: 'not_found', reconciled: false, order_status: 'pending_payment' }).text).toContain('没有这笔订单的付款记录')
    expect(orderQueriedSchema.safeParse({ ...base, channel_status: 'error' }).success).toBe(false)
    expect(orderQueriedSchema.safeParse({ ...base, quarantine_kind: 'overdue' }).success).toBe(false)
  })
})

describe('manualCreatedToast', () => {
  const base = { order_id: 'o1', order_no: 'NO1', currency: 'CNY', discount_amount: 0, total_amount: 1000, balance_applied: 0, payable_amount: 1000, status: 'fulfilled' as const }
  it('另开一份：说明另开了一份订阅', () => {
    expect(manualCreatedToast(base, 'grant')).toBe('订单 NO1 已赠送开通，已另开一份订阅')
    expect(manualCreatedToast(base, 'offline', 'new')).toBe('订单 NO1 已按线下收款入账，已另开一份订阅')
    expect(manualCreatedToast(base, 'pending', 'new')).toContain('等待用户在 30 分钟内支付')
  })
  it('续一期：说明订阅链接不变', () => {
    expect(manualCreatedToast(base, 'grant', 'renew')).toBe('订单 NO1 已赠送续期，订阅链接不变')
    expect(manualCreatedToast(base, 'offline', 'renew')).toBe('订单 NO1 已按线下收款入账，订阅已续期，链接不变')
    expect(manualCreatedToast(base, 'pending', 'renew')).toBe('订单 NO1 已创建（续费），等待用户在 30 分钟内支付')
  })
  it('落成原订阅上的换套餐：说明链接不变与退回余额的金额', () => {
    const changed = manualCreatedSchema.parse({ ...base, proration_credit: 995, balance_refund: 995 })
    expect(manualCreatedToast(changed, 'grant', 'change')).toBe('订单 NO1 已在原订阅上换套餐，订阅链接不变，原套餐剩余价值 ¥9.95 已退回余额')
    expect(manualCreatedToast({ ...changed, balance_refund: 0 }, 'offline', 'change')).toBe('订单 NO1 已在原订阅上换套餐，订阅链接不变')
    expect(manualCreatedToast({ ...changed, status: 'pending_payment', balance_refund: 0 }, 'pending', 'change')).toContain('在原订阅上换套餐')
    // 响应里没有退款项（Go 的 omitempty）也不报错
    expect(manualCreatedToast(base, 'grant', 'change')).toBe('订单 NO1 已在原订阅上换套餐，订阅链接不变')
  })
})
