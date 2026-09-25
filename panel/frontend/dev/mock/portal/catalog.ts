/**
 * [INPUT]: 无外部依赖（静态夹具）
 * [OUTPUT]: 对外提供 CatalogPlan / CatalogPrice / CatalogPack / CatalogCoupon / PayMethod / GiftTemplate 类型与 PLANS、PACKS、COUPONS、PAY_METHODS、GIFT_CARDS、findPlan、findPrice、findPack、planView、GIB、intervalMonths
 * [POS]: dev/mock/portal 的商品目录夹具（不是模块，不进登记表）：选购页、结账页与订阅夹具共用同一批套餐 / 价格 / 流量包 / 优惠码 / 支付方式，形状照契约门户-03（含修订 R30、R61、R69、R99、R100）；专业版标为推荐、家庭版 allow_upgrade=false 且限速 100 Mbps、优惠码覆盖各种错误、一个 POST 跳转渠道，便于浏览器实测每条分支
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
export const GIB = 1024 ** 3

export interface CatalogPrice {
  id: string
  currency: 'CNY' | 'USD'
  unit_amount: number
  billing_interval: 'day' | 'week' | 'month' | 'quarter' | 'year' | 'one_time'
  interval_count: number
  trial_days: number
}

export interface CatalogPlan {
  id: string
  code: string
  name: string
  description: string | null
  max_devices: number | null
  trafficBytes: number | null
  quota_reset_strategy: 'never' | 'natural_month' | 'billing_cycle' | 'fixed_day'
  quota_reset_day: number | null
  allow_renewal: boolean
  allow_upgrade: boolean
  throttle_kbps: number | null
  highlights: string[]
  recommended: boolean
  prices: CatalogPrice[]
}

const price = (id: string, unit_amount: number, billing_interval: CatalogPrice['billing_interval'], interval_count = 1, currency: CatalogPrice['currency'] = 'CNY'): CatalogPrice => ({
  id,
  currency,
  unit_amount,
  billing_interval,
  interval_count,
  trial_days: 0,
})

export const PLANS: readonly CatalogPlan[] = [
  {
    id: '6f1c2a10-0000-4000-8000-000000000001',
    code: 'std',
    name: '标准版',
    description: '日常浏览与社交，全部常规线路。',
    max_devices: 3,
    trafficBytes: 200 * GIB,
    quota_reset_strategy: 'natural_month',
    quota_reset_day: null,
    allow_renewal: true,
    allow_upgrade: true,
    throttle_kbps: null,
    highlights: ['全部常规线路', '工单支持'],
    recommended: false,
    prices: [
      price('6f1c2a10-0000-4000-8000-000000000011', 2900, 'month'),
      price('6f1c2a10-0000-4000-8000-000000000012', 7900, 'quarter'),
      price('6f1c2a10-0000-4000-8000-000000000013', 29900, 'year'),
      price('6f1c2a10-0000-4000-8000-000000000014', 499, 'month', 1, 'USD'),
    ],
  },
  {
    id: '6f1c2a10-0000-4000-8000-000000000002',
    code: 'pro',
    name: '专业版',
    description: '亚太精选与欧美线路，流媒体解锁。',
    max_devices: 5,
    trafficBytes: 500 * GIB,
    quota_reset_strategy: 'billing_cycle',
    quota_reset_day: null,
    allow_renewal: true,
    allow_upgrade: true,
    throttle_kbps: null,
    highlights: ['亚太精选 + 欧美线路', '流媒体解锁'],
    recommended: true,
    prices: [price('6f1c2a10-0000-4000-8000-000000000021', 5900, 'month'), price('6f1c2a10-0000-4000-8000-000000000022', 15900, 'month', 3), price('6f1c2a10-0000-4000-8000-000000000023', 59900, 'year')],
  },
  {
    id: '6f1c2a10-0000-4000-8000-000000000003',
    code: 'fam',
    name: '家庭版',
    description: null,
    max_devices: 6,
    trafficBytes: 300 * GIB,
    quota_reset_strategy: 'fixed_day',
    quota_reset_day: 15,
    allow_renewal: true,
    // 用来实测「不支持变更」
    allow_upgrade: false,
    // 用来实测「限速 N Mbps」
    throttle_kbps: 100_000,
    highlights: ['适合家庭与小团队'],
    recommended: false,
    prices: [price('6f1c2a10-0000-4000-8000-000000000031', 4900, 'month'), price('6f1c2a10-0000-4000-8000-000000000032', 13900, 'month', 3), price('6f1c2a10-0000-4000-8000-000000000033', 49900, 'year')],
  },
]

export const findPlan = (id: unknown) => PLANS.find((p) => p.id === id)
export const findPrice = (plan: CatalogPlan, id: unknown) => plan.prices.find((p) => p.id === id)

/** 契约门户-03 GET v1/plans 的一行；legacy 场景只去掉修订 R69 的四个字段（R99 / R100 的字段照带） */
export function planView(p: CatalogPlan, legacy: boolean) {
  const base = {
    id: p.id,
    code: p.code,
    name: p.name,
    description: p.description,
    version: 2,
    max_devices: p.max_devices,
    quotas: [{ metric: 'traffic.bytes', limit: p.trafficBytes, unit: 'bytes', period: 'cycle' }],
    prices: [...p.prices].sort((a, b) => a.unit_amount - b.unit_amount),
    throttle_kbps: p.throttle_kbps,
    highlights: p.highlights,
    recommended: p.recommended,
  }
  if (legacy) return base
  return { ...base, quota_reset_strategy: p.quota_reset_strategy, quota_reset_day: p.quota_reset_day, allow_renewal: p.allow_renewal, allow_upgrade: p.allow_upgrade }
}

/** 价格周期折成月数，续费 / 新购延长订阅用 */
export function intervalMonths(p: Pick<CatalogPrice, 'billing_interval' | 'interval_count'>): number {
  const per = { day: 1 / 30, week: 7 / 30, month: 1, quarter: 3, year: 12, one_time: 1 }[p.billing_interval]
  return per * p.interval_count
}

export interface CatalogPack {
  id: string
  name: string
  traffic_bytes: number
  currency: 'CNY'
  unit_amount: number
  recommended: boolean
}

export const PACKS: readonly CatalogPack[] = [
  { id: '7a2d3b20-0000-4000-8000-000000000050', name: '50 GB', traffic_bytes: 50 * GIB, currency: 'CNY', unit_amount: 1500, recommended: false },
  { id: '7a2d3b20-0000-4000-8000-000000000200', name: '200 GB', traffic_bytes: 200 * GIB, currency: 'CNY', unit_amount: 4900, recommended: false },
  { id: '7a2d3b20-0000-4000-8000-000000000500', name: '500 GB', traffic_bytes: 500 * GIB, currency: 'CNY', unit_amount: 9900, recommended: true },
  { id: '7a2d3b20-0000-4000-8000-000000001000', name: '1 TB', traffic_bytes: 1024 * GIB, currency: 'CNY', unit_amount: 17900, recommended: false },
]

export const findPack = (id: unknown) => PACKS.find((p) => p.id === id)

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
  currencies: string[]
  httpMethod: 'GET' | 'POST'
}

export const PAY_METHODS: readonly PayMethod[] = [
  { provider: 'epay', method: 'alipay', label: '支付宝', currencies: ['CNY'], httpMethod: 'GET' },
  { provider: 'epay', method: 'wxpay', label: '微信支付', currencies: ['CNY'], httpMethod: 'GET' },
  { provider: 'legacy', method: 'qqpay', label: 'QQ 钱包', currencies: ['CNY'], httpMethod: 'POST' },
  { provider: 'stripe', method: 'card', label: '银行卡（美元）', currencies: ['USD'], httpMethod: 'GET' },
]

// ---------------------------------------------------------------------------
// 礼品卡（契约门户-05，修订 R31、R68）：卡码 → 模板；error 模拟兑换条件不满足
// ---------------------------------------------------------------------------
export interface GiftTemplate {
  name: string
  description: string
  type: 'general' | 'plan' | 'mystery'
  rewards: { balance?: number; traffic_bytes?: number; expire_days?: number; reset_quota?: boolean; plan_id?: string; price_id?: string; pool?: Array<{ label: string; weight: number; balance?: number; traffic_bytes?: number; expire_days?: number }> }
  /** 兑换时的拒绝（预览照常能看到卡面） */
  redeemError?: string
}

export const GIFT_CARDS: Readonly<Record<string, GiftTemplate>> = {
  'GC-0923-H2K9-7QPA': { name: '国庆余额卡', description: '', type: 'general', rewards: { balance: 10000 } },
  'GC-1001-TRAF-0200': { name: '流量礼包', description: '', type: 'general', rewards: { traffic_bytes: 200 * GIB } },
  'GC-0911-Q7ZP-M3VX': { name: '专业版月卡', description: '兑换后开通专业版，已有订阅则顺延 30 天', type: 'plan', rewards: { plan_id: PLANS[1]!.id, price_id: PLANS[1]!.prices[0]!.id } },
  'GC-1024-MYST-BOX1': {
    name: '万圣节盲盒',
    description: '',
    type: 'mystery',
    rewards: {
      pool: [
        { label: '¥5 余额', weight: 0, balance: 500 },
        { label: '50 GB 流量', weight: 0, traffic_bytes: 50 * GIB },
        { label: '延长 7 天', weight: 0, expire_days: 7 },
      ],
    },
  },
  'GC-EXTD-0007-DAYS': { name: '续命卡', description: '', type: 'general', rewards: { expire_days: 7 } },
  'GC-0000-NEWU-0001': { name: '新人专享卡', description: '仅限新注册用户', type: 'general', rewards: { balance: 2000 }, redeemError: '这张卡只能新用户使用' },
}
