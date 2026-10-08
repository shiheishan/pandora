import { randomBytes } from 'node:crypto'
import { existsSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { admin, call, newUser, portalToken, str, type Json } from './api.ts'
import { ensureOut, OUT, PUB, smokePoolId, STEPS_FILE, WORLD_FILE } from './env.ts'

// ============================================================================
//  浏览器测试自己的种子（Playwright globalSetup）：全部经后台真实接口造，不碰 tests/smoke/seed.ts。
//  照 w8walk 的 31 步路径备料：五个套餐、一个流量包、六种礼品卡模板、一个演示易支付渠道。
//  建好的 id 与渠道测试密钥写进状态目录的 browser-world.json（0600）；文件已在时整份复用，
//  本机对同一套栈重跑不会撞套餐编码
// ============================================================================

export interface PlanRef {
  id: string
  name: string
  /** 价格 id，顺序同建套餐时的 prices */
  prices: string[]
}

export interface World {
  provider: { code: string; merchant: string; key: string }
  plans: Record<'basic' | 'std' | 'plus' | 'pro' | 'mini', PlanRef>
  packId: string
  cards: Record<'days7' | 'reset' | 'traffic2g' | 'planStd' | 'planPro' | 'mystery', string>
}

export function loadWorld(): World {
  return JSON.parse(readFileSync(WORLD_FILE, 'utf8')) as World
}

const GB = 1024 ** 3

// 名字互不为前缀（页面上按名字找卡片与选项）；价格照 w8walk：标准加比标准只贵 ¥0.40，换过去只差几分钱
const PLANS: Array<[keyof World['plans'], string, string, number, Array<[number, number]>]> = [
  ['basic', 'w9b-basic', 'W9 基础', 10, [[1, 1000]]],
  ['std', 'w9b-std', 'W9 标准', 100, [[1, 3000], [3, 8000]]],
  ['plus', 'w9b-plus', 'W9 加强', 100, [[1, 3040]]],
  ['pro', 'w9b-pro', 'W9 进阶', 200, [[1, 4200]]],
  ['mini', 'w9b-mini', 'W9 迷你', 1, [[1, 50]]],
]

async function build(): Promise<World> {
  const pool = smokePoolId()

  // 演示易支付渠道：商户号与密钥本次随机生成，收银台地址指向回环上没人听的端口（只用来拼跳转地址，
  // 测试从不打开它）；allow_private_host 只在非生产（AEGIS_ENV=test）放行，与 run-smoke-e2e.sh 的
  // aegis-payctl --allow-private-host 同一口子，没有放宽任何校验
  const provider = { code: 'w9pay', merchant: `w9b${randomBytes(3).toString('hex')}`, key: randomBytes(16).toString('hex') }
  await admin('/v1/payment-providers', {
    body: {
      code: provider.code,
      adapter: 'epay',
      display_name: 'W9 演示收银（假商户）',
      base_url: 'http://127.0.0.1:9',
      submit_path: '/submit.php',
      api_path: '/api.php',
      methods: ['alipay'],
      default_method: 'alipay',
      allow_private_host: true,
      min_amount: 100,
      merchant_id: provider.merchant,
      key: provider.key,
    },
    expect: [200, 201],
  })

  const plans = {} as World['plans']
  for (const [key, code, name, gb, prices] of PLANS) {
    const r = await admin('/v1/plans/complete', {
      body: {
        code,
        name,
        visibility: 'public',
        traffic_gb: gb,
        max_devices: 5,
        allow_new_purchase: true,
        allow_renewal: true,
        allow_upgrade: true,
        pool_ids: [pool],
        prices: prices.map(([count, amount]) => ({ billing_interval: 'month', interval_count: count, unit_amount: amount, currency: 'CNY', trial_days: 0 })),
        publish: true,
      },
      expect: [200, 201],
    })
    if (r.published === false) throw new Error(`套餐 ${name} 没发布出去（池里没有可服务节点？）：${JSON.stringify(r).slice(0, 300)}`)
    plans[key] = { id: str(r, 'plan.id'), name, prices: (r.price_ids as string[] | undefined) ?? [] }
    if (plans[key].prices.length !== prices.length) throw new Error(`套餐 ${name} 的价格档数不对：${JSON.stringify(r).slice(0, 300)}`)
  }

  const pack = await admin('/v1/traffic-packs', {
    body: { name: 'W9 12G', traffic_bytes: 12 * GB, currency: 'CNY', unit_amount: 500, recommended: false, sort_order: 90 },
    expect: [200, 201],
  })
  const packId = typeof pack.id === 'string' ? pack.id : str(pack, 'pack.id')

  const specs: Record<keyof World['cards'], Json> = {
    days7: { type: 'general', rewards: { expire_days: 7 } },
    reset: { type: 'general', rewards: { reset_quota: true } },
    traffic2g: { type: 'general', rewards: { traffic_bytes: 2 * GB } },
    planStd: { type: 'plan', rewards: { plan_id: plans.std.id, price_id: plans.std.prices[0] } },
    planPro: { type: 'plan', rewards: { plan_id: plans.pro.id, price_id: plans.pro.prices[0] } },
    mystery: {
      type: 'mystery',
      rewards: {
        pool: [
          { label: '加 3 天', weight: 1, expire_days: 3 },
          { label: '送 1G', weight: 1, traffic_bytes: GB },
        ],
      },
    },
  }
  const cards = {} as World['cards']
  for (const [key, spec] of Object.entries(specs) as Array<[keyof World['cards'], Json]>) {
    const t = await admin('/v1/gift-cards', {
      body: { name: `W9 ${key}`, description: '', status: 'active', conditions: {}, limits: {}, theme_color: '', ...spec },
      expect: [200, 201],
    })
    cards[key] = str(t, 'template.id')
  }
  return { provider, plans, packId, cards }
}

/** 一张礼品卡的明文卡号：每次现生成一批，只取样例里的第一张（生码响应只回前 4 张明文） */
export async function giftCode(templateId: string): Promise<string> {
  const r = await admin(`/v1/gift-cards/${templateId}/codes`, { body: { count: 1, prefix: 'W9B' }, expect: [200, 201] })
  const code = (r.sample as string[] | undefined)?.[0]
  if (!code) throw new Error(`生码响应没有样例：${JSON.stringify(r).slice(0, 300)}`)
  return code
}

/**
 * 只留本测试的渠道收新单：w8walk 的路径要求站点最低付款额是 ¥1.00（凑最低额、免零头、低于最低额被拦），
 * 而冒烟种子的演示渠道没有最低额（站点最低额取所有收单渠道里最小的那个）。暂停（enabled 留着、
 * accepting_new 关掉）只影响新单，与后台卡片上的开关同一个接口；这一步在 panel-smoke 的最后跑，不影响别的步骤
 */
async function onlyOurChannel(world: World): Promise<void> {
  const list = ((await admin('/v1/payment-providers')).providers as Json[] | undefined) ?? []
  for (const p of list) {
    const code = String(p.code)
    if (code === 'offline') continue
    const want = code === world.provider.code
    if (p.enabled === true && p.accepting_new === want) continue
    if (!want && p.accepting_new !== true) continue
    await admin(`/v1/payment-providers/${code}/toggle`, { body: { enabled: true, accepting_new: want } })
  }
}

/** 门户报价的最低付款额按进程缓存一分钟：轮询到 ¥1.00 再放测试进来，不靠固定等待 */
async function waitMinPayment(world: World): Promise<void> {
  const u = await newUser('probe')
  const token = await portalToken(u)
  const deadline = Date.now() + 90_000
  let last: unknown = null
  while (Date.now() < deadline) {
    const q = await call(PUB, '/v1/me/checkout/quote', { token, body: { action: 'new', plan_id: world.plans.mini.id } })
    last = q.min_payment
    if (last === 100) return
    await new Promise((r) => setTimeout(r, 3000))
  }
  throw new Error(`门户报价的最低付款额 90 秒内没变成 100（最后是 ${String(last)}）`)
}

export default async function globalSetup(): Promise<void> {
  ensureOut()
  rmSync(STEPS_FILE, { force: true })
  let world: World
  if (existsSync(WORLD_FILE)) {
    world = loadWorld()
    console.log(`==> 复用 ${WORLD_FILE}`)
  } else {
    console.log('==> 造浏览器测试的种子：渠道、套餐、流量包、礼品卡模板')
    world = await build()
    writeFileSync(WORLD_FILE, JSON.stringify(world, null, 2), { mode: 0o600 })
  }
  await onlyOurChannel(world)
  await waitMinPayment(world)
  console.log(`==> 种子就绪；产物写到 ${OUT}`)
}
