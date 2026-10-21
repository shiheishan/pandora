import { isProto } from './scenario.ts'

export const GIB = 1024 ** 3

export interface CatalogPrice {
  id: string
  billing_interval: 'month' | 'year' | 'one_time'
  interval_count: number
  unit_amount: number
  validity_days: number | null
}

export interface CatalogPlan {
  id: string
  code: string
  name: string
  description: string | null
  kind: 'subscription' | 'one_time'
  recommended: boolean
  /** 订阅 = 每期流量，一次性 = 总流量；null = 不限 */
  trafficBytes: number | null
  max_devices: number | null
  throttle_kbps: number | null
  prices: CatalogPrice[]
  reset: { price: number; limit: number | null } | null
  /** 销售开关：目录 DTO 回 allow_new_purchase / allow_renewal；allowUpgrade 只给报价 / 落点 / 下单判断用 */
  allowNew: boolean
  allowRenewal: boolean
  /** 能从别的套餐换过来 */
  allowUpgrade: boolean
}

const price = (
  id: string,
  unit_amount: number,
  billing_interval: CatalogPrice['billing_interval'],
  interval_count = 1,
  validity_days: number | null = null,
): CatalogPrice => ({
  id,
  unit_amount,
  billing_interval,
  interval_count,
  validity_days,
})

export const PLANS: readonly CatalogPlan[] = [
  {
    id: '6f1c2a10-0000-4000-8000-000000000001',
    code: 'std',
    name: '标准版',
    description: '日常浏览与社交，全部常规线路。',
    kind: 'subscription',
    recommended: false,
    trafficBytes: 200 * GIB,
    max_devices: 3,
    throttle_kbps: null,
    prices: [
      price('6f1c2a10-0000-4000-8000-000000000011', 2900, 'month'),
      price('6f1c2a10-0000-4000-8000-000000000012', 7900, 'month', 3),
      price('6f1c2a10-0000-4000-8000-000000000014', 14900, 'month', 6),
      price('6f1c2a10-0000-4000-8000-000000000013', 29900, 'year'),
    ],
    reset: { price: 1000, limit: 2 },
    allowNew: true,
    allowRenewal: true,
    allowUpgrade: true,
  },
  {
    id: '6f1c2a10-0000-4000-8000-000000000002',
    code: 'pro',
    name: '专业版',
    description: '亚太精选与欧美线路，流媒体解锁。',
    kind: 'subscription',
    recommended: true,
    trafficBytes: 500 * GIB,
    max_devices: 5,
    throttle_kbps: null,
    prices: [
      price('6f1c2a10-0000-4000-8000-000000000021', 5900, 'month'),
      price('6f1c2a10-0000-4000-8000-000000000022', 15900, 'month', 3),
      price('6f1c2a10-0000-4000-8000-000000000023', 59900, 'year'),
    ],
    reset: null,
    allowNew: true,
    allowRenewal: true,
    allowUpgrade: true,
  },
  {
    id: '6f1c2a10-0000-4000-8000-000000000003',
    code: 'fam',
    name: '家庭版',
    description: null,
    kind: 'subscription',
    recommended: false,
    trafficBytes: 300 * GIB,
    max_devices: 6,
    throttle_kbps: 100_000,
    prices: [
      price('6f1c2a10-0000-4000-8000-000000000031', 4900, 'month'),
      price('6f1c2a10-0000-4000-8000-000000000032', 13900, 'month', 3),
      price('6f1c2a10-0000-4000-8000-000000000033', 49900, 'year'),
    ],
    reset: { price: 1500, limit: null },
    allowNew: true,
    allowRenewal: true,
    allowUpgrade: false,
  },
  {
    id: '6f1c2a10-0000-4000-8000-000000000004',
    code: 'traffic-100',
    name: '流量版',
    description: '100 GB 流量，有效 90 天。',
    kind: 'one_time',
    recommended: false,
    trafficBytes: 100 * GIB,
    max_devices: 3,
    throttle_kbps: null,
    prices: [price('6f1c2a10-0000-4000-8000-000000000041', 3000, 'one_time', 1, 90)],
    reset: { price: 500, limit: 1 },
    allowNew: true,
    allowRenewal: false,
    allowUpgrade: true,
  },
  {
    id: '6f1c2a10-0000-4000-8000-000000000005',
    code: 'forever-500',
    name: '永久版',
    description: '500 GB 总量，永久有效。',
    kind: 'one_time',
    recommended: false,
    trafficBytes: 500 * GIB,
    max_devices: 3,
    throttle_kbps: null,
    prices: [price('6f1c2a10-0000-4000-8000-000000000051', 9900, 'one_time', 1, null)],
    reset: null,
    allowNew: true,
    allowRenewal: false,
    allowUpgrade: true,
  },
  {
    id: '6f1c2a10-0000-4000-8000-000000000006',
    code: 'classic',
    name: '经典版',
    description: '老用户续费专用，不再新卖。',
    kind: 'subscription',
    recommended: false,
    trafficBytes: 100 * GIB,
    max_devices: 2,
    throttle_kbps: null,
    prices: [price('6f1c2a10-0000-4000-8000-000000000061', 1900, 'month')],
    reset: null,
    allowNew: false,
    allowRenewal: true,
    allowUpgrade: true,
  },
]

// ---------------------------------------------------------------------------
// 原型目录（proto-* 场景）：基础版 / 标准版 / 进阶版三档，价格与流量照原型 PLANS
// ---------------------------------------------------------------------------
const protoPlan = (
  n: number,
  code: string,
  name: string,
  gb: number,
  devices: number,
  prices: [number, number, number],
  recommended = false,
): CatalogPlan => ({
  id: `a1000000-0000-4000-8000-00000000000${n}`,
  code,
  name,
  description: null,
  kind: 'subscription',
  recommended,
  trafficBytes: gb * GIB,
  max_devices: devices,
  throttle_kbps: null,
  prices: [
    price(`a1000000-0000-4000-8000-0000000000${n}1`, prices[0], 'month'),
    price(`a1000000-0000-4000-8000-0000000000${n}3`, prices[1], 'month', 3),
    price(`a1000000-0000-4000-8000-0000000000${n}9`, prices[2], 'year'),
  ],
  reset: null,
  allowNew: true,
  allowRenewal: true,
  allowUpgrade: true,
})

export const PROTO_PLANS: readonly CatalogPlan[] = [
  protoPlan(1, 'basic', '基础版', 50, 2, [1500, 4200, 15000]),
  protoPlan(2, 'std', '标准版', 100, 3, [3000, 8400, 30000], true),
  protoPlan(3, 'pro', '进阶版', 300, 5, [4500, 12600, 45000]),
]

/** 当前场景的在售套餐 */
export const plansNow = (): readonly CatalogPlan[] => (isProto() ? PROTO_PLANS : PLANS)

export const findPlan = (id: unknown) => PLANS.find((p) => p.id === id) ?? PROTO_PLANS.find((p) => p.id === id)
export const findPrice = (plan: CatalogPlan, id: unknown) => plan.prices.find((p) => p.id === id)

/** 按时长排：1 个月、3 个月、半年、1 年、一次性 */
function priceSortKey(p: CatalogPrice): number {
  if (p.billing_interval === 'one_time') return 10_000
  if (p.billing_interval === 'year') return 1000
  return p.interval_count
}

/** 契约门户-03 GET v1/plans 的一行（与 Go catalog handler DTO 一致，无 omitempty） */
export function planView(p: CatalogPlan) {
  return {
    id: p.id,
    name: p.name,
    description: p.description,
    kind: p.kind,
    recommended: p.recommended,
    allow_new_purchase: p.allowNew,
    allow_renewal: p.allowRenewal,
    traffic_bytes: p.trafficBytes,
    max_devices: p.max_devices,
    throttle_kbps: p.throttle_kbps,
    prices: [...p.prices].sort((a, b) => priceSortKey(a) - priceSortKey(b)),
    reset: p.reset,
  }
}

// ---------------------------------------------------------------------------
// 优惠码：percent 的 value 是万分比。error 模拟后端的各种拒绝（契约 coupons/preview 的错误列表）
// ---------------------------------------------------------------------------
export interface CatalogCoupon {
  type: 'percent' | 'fixed'
  value: number
  /** 只适用于这些价格 id（其余 422「这个优惠码不适用于所选套餐」） */
  onlyPrices?: readonly string[]
  /** 使用门槛（分） */
  minAmount?: number
  error?: { status: number; code: string; message: string }
}

export const COUPONS: Readonly<Record<string, CatalogCoupon>> = {
  AUTUMN26: { type: 'percent', value: 2000 },
  WELCOME: { type: 'percent', value: 1000 },
  PROYEAR: { type: 'fixed', value: 10000, onlyPrices: ['6f1c2a10-0000-4000-8000-000000000023'] },
  BIG50: { type: 'fixed', value: 5000, minAmount: 10000 },
  // 99% 折扣：把应付压到支付最低额以下，测 below_minimum（proto-s7d）
  LUCKY99: { type: 'percent', value: 9900 },
  EXPIRED: { type: 'percent', value: 1000, error: { status: 422, code: 'validation_failed', message: '优惠码已过期' } },
  USED: { type: 'percent', value: 1000, error: { status: 409, code: 'conflict', message: '你已使用过这个优惠码' } },
}

// ---------------------------------------------------------------------------
// 支付方式（修订 R61）：epay 两种 + 一个产出 POST 跳转的渠道（前端应按「暂不可用」处理）
// ---------------------------------------------------------------------------
export interface PayMethod {
  provider: string
  method: string
  label: string
  httpMethod: 'GET' | 'POST'
  fee_fixed: number
  fee_percent: string
  /** 渠道最低付款额（分）；订单应付低于它时这个方式付不了，与 Go payment_providers.min_amount 同口径 */
  min_amount: number
}

export const PAY_METHODS: readonly PayMethod[] = [
  { provider: 'epay', method: 'alipay', label: '支付宝', httpMethod: 'GET', fee_fixed: 0, fee_percent: '0.00', min_amount: 100 },
  { provider: 'epay', method: 'wxpay', label: '微信支付', httpMethod: 'GET', fee_fixed: 0, fee_percent: '0.00', min_amount: 100 },
  { provider: 'legacy', method: 'qqpay', label: 'QQ 钱包', httpMethod: 'POST', fee_fixed: 0, fee_percent: '0.00', min_amount: 100 },
  { provider: 'btcpay', method: '', label: 'BTCPay', httpMethod: 'GET', fee_fixed: 0, fee_percent: '1.00', min_amount: 2000 },
]

/** 站点最低付款额（分）：启用且接单的在线渠道里最小的 min_amount（Go payment repo.MinAcceptingAmount）。报价、建单按它判；发起支付按所选方式自己的 min_amount 判 */
export const MIN_PAYMENT = Math.min(...PAY_METHODS.map((m) => m.min_amount))

// ---------------------------------------------------------------------------
// 礼品卡（契约门户-05，修订 R31、R68）：卡码 → 模板；error 模拟兑换条件不满足
// ---------------------------------------------------------------------------
export interface GiftTemplate {
  name: string
  type: 'general' | 'plan' | 'mystery'
  rewards: { balance?: number; traffic_bytes?: number; expire_days?: number; reset_quota?: boolean; plan_id?: string; price_id?: string; pool?: Array<{ label: string; weight: number; balance?: number; traffic_bytes?: number; expire_days?: number; reset_quota?: boolean }> }
  /** 兑换时的拒绝（预览照常能看到卡面） */
  redeemError?: string
}

const TRAFFIC_PLAN = PLANS[3]!

export const GIFT_CARDS: Readonly<Record<string, GiftTemplate>> = {
  'GC-0923-H2K9-7QPA': { name: '国庆余额卡', type: 'general', rewards: { balance: 10000 } },
  'GC-1001-TRAF-0200': { name: '流量礼包', type: 'general', rewards: { traffic_bytes: 200 * GIB } },
  'GC-0911-Q7ZP-M3VX': {
    name: '标准版月卡',
    type: 'plan',
    rewards: { plan_id: PLANS[0]!.id, price_id: PLANS[0]!.prices[0]!.id },
  },
  'GC-1024-MYST-BOX1': {
    name: '万圣节盲盒',
    type: 'mystery',
    rewards: {
      pool: [
        { label: '¥5 余额', weight: 0, balance: 500 },
        { label: '50 GB 流量', weight: 0, traffic_bytes: 50 * GIB },
        { label: '延长 7 天', weight: 0, expire_days: 7 },
      ],
    },
  },
  'GC-EXTD-0007-DAYS': { name: '续命卡', type: 'general', rewards: { expire_days: 7 } },
  'GC-0000-NEWU-0001': { name: '新人专享卡', type: 'general', rewards: { balance: 2000 }, redeemError: '这张卡只能新用户使用' },
  'GC-1002-RSET-QUOT': { name: '流量重置卡', type: 'general', rewards: { reset_quota: true } },
  'GC-1016-ONCE-0100': {
    name: '流量版卡',
    type: 'plan',
    rewards: { plan_id: TRAFFIC_PLAN.id, price_id: TRAFFIC_PLAN.prices[0]!.id },
  },
  // 原型 CARDS（proto-s5a / s5b）
  'A7K2-STD1-M9QX': { name: '标准版 · 1 个月', type: 'plan', rewards: { plan_id: PROTO_PLANS[1]!.id, price_id: PROTO_PLANS[1]!.prices[0]!.id } },
  'B3P8-PRO1-W4RT': { name: '进阶版 · 1 个月', type: 'plan', rewards: { plan_id: PROTO_PLANS[2]!.id, price_id: PROTO_PLANS[2]!.prices[0]!.id } },
  'C5D3-D030-H8NE': { name: '加 30 天', type: 'general', rewards: { expire_days: 30 } },
  'D9R1-RSET-J2LU': { name: '流量清零重算一次', type: 'general', rewards: { reset_quota: true } },
}
