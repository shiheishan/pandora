/**
 * [INPUT]: 依赖 run-smoke-stack.sh 写在状态目录的 smoke.env（网关地址、管理员账号、PG 容器名）与 gateway.env（主密钥，演示渠道验签用），依赖 node:fs / node:child_process / node:crypto 与全局 fetch
 * [OUTPUT]: 命令行脚本 `node seed.ts <状态目录>`：经真实网关跑一条最小业务链，让后台与门户各列表都不为空，把后续冒烟要用的 id 与门户账号写进 <状态目录>/seed.json；任何一步状态码不符即退出 1
 * [POS]: tests/smoke 的第 ② 步，在 run-smoke-stack.sh up 之后、形状校验之前运行；只发请求不做形状断言（那是第 ③ 步页面 schema 的事），唯一一处直连数据库是插入演示支付渠道——后台没有新建渠道的接口
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { spawnSync } from 'node:child_process'
import { createHmac, randomUUID } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

// ============================================================================
//  输入
// ============================================================================

const stateDir = process.argv[2]
if (!stateDir) {
  console.error('用法: node seed.ts <状态目录>')
  process.exit(2)
}

function readEnvFile(path: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of readFileSync(path, 'utf8').split('\n')) {
    const m = /^([A-Z0-9_]+)=(.*)$/.exec(line)
    if (m) out[m[1]!] = m[2]!
  }
  return out
}

function need(env: Record<string, string>, key: string): string {
  const v = env[key]
  if (!v) throw new Error(`状态目录缺少 ${key}`)
  return v
}

const smokeEnv = readEnvFile(join(stateDir, 'smoke.env'))
const gatewayEnv = readEnvFile(join(stateDir, 'gateway.env'))
const PUB = need(smokeEnv, 'SMOKE_PUBLIC_BASE')
const ADM = need(smokeEnv, 'SMOKE_ADMIN_BASE')
const NODE = need(smokeEnv, 'SMOKE_NODE_BASE')
const ADMIN_EMAIL = need(smokeEnv, 'SMOKE_ADMIN_EMAIL')
const ADMIN_PASSWORD = need(smokeEnv, 'SMOKE_ADMIN_PASSWORD')
const PG_CONTAINER = need(smokeEnv, 'SMOKE_PG_CONTAINER')
const MASTER_KEY = Buffer.from(need(gatewayEnv, 'AEGIS_MASTER_KEY'), 'base64')
const TENANT = '00000000-0000-7000-8000-000000000001'

// ============================================================================
//  HTTP：每个请求都声明期望状态码，不符就带响应片段退出
// ============================================================================

type Json = Record<string, unknown>

interface Call {
  method?: string
  token?: string
  body?: unknown
  /** 给 true 自动生成一把新键；给字符串则用它 */
  idem?: boolean | string
  expect?: number | number[]
  headers?: Record<string, string>
}

// 后台 IP 限流每分钟 240 次写死在代码里（验收认可在脚本里控节奏）：后台请求之间留 300ms
let lastAdminCall = 0
async function paceAdmin(base: string): Promise<void> {
  if (base !== ADM) return
  const wait = lastAdminCall + 300 - Date.now()
  if (wait > 0) await new Promise((r) => setTimeout(r, wait))
  lastAdminCall = Date.now()
}

async function call(base: string, path: string, c: Call = {}): Promise<Json> {
  await paceAdmin(base)
  const headers: Record<string, string> = { ...c.headers }
  if (c.body !== undefined) headers['Content-Type'] = 'application/json'
  if (c.token) headers.Authorization = `Bearer ${c.token}`
  if (c.idem) headers['Idempotency-Key'] = c.idem === true ? randomUUID() : c.idem
  const method = c.method ?? (c.body === undefined ? 'GET' : 'POST')
  const res = await fetch(base + path, {
    method,
    headers,
    ...(c.body === undefined ? {} : { body: JSON.stringify(c.body) }),
  })
  const text = await res.text()
  const expect = Array.isArray(c.expect) ? c.expect : [c.expect ?? 200]
  if (!expect.includes(res.status)) {
    throw new Error(`${method} ${path} 返回 ${res.status}（期望 ${expect.join('/')}）：${text.slice(0, 400)}`)
  }
  return text ? (JSON.parse(text) as Json) : {}
}

function str(o: Json, path: string): string {
  let v: unknown = o
  for (const k of path.split('.')) v = (v as Json | undefined)?.[k]
  if (typeof v !== 'string' || v === '') throw new Error(`响应缺少字符串字段 ${path}：${JSON.stringify(o).slice(0, 300)}`)
  return v
}

function num(o: Json, path: string): number {
  let v: unknown = o
  for (const k of path.split('.')) v = (v as Json | undefined)?.[k]
  if (typeof v !== 'number') throw new Error(`响应缺少数字字段 ${path}：${JSON.stringify(o).slice(0, 300)}`)
  return v
}

function step(title: string): void {
  console.log(`==> ${title}`)
}

// ============================================================================
//  后台登录。登录签发的令牌自带 15 分钟 reauth 窗口，整条链一分钟内跑完，用不上重认证
// ============================================================================

step('管理员登录')
const admin = str(await call(ADM, '/v1/auth/login', { body: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD } }), 'access_token')

// ============================================================================
//  节点：池 → 服务器 → 节点（建时即划进池）→ 状态逐级推进 → 服务器就绪 → UniProxy 心跳
//  没划进池的节点不服务任何用户（R104、R105）；心跳只有 UniProxy /status 会写
// ============================================================================

step('节点池')
const poolId = str(await call(ADM, '/v1/node-pools', { token: admin, body: { name: 'Smoke Pool', code: 'smoke-pool' }, expect: [200, 201] }), 'id')

step('服务器')
const server = await call(ADM, '/v1/servers', {
  token: admin,
  body: { name: 'smoke-srv-1', hostname: 'smoke.example.test', public_ipv4: '203.0.113.10', capacity_nodes: 4 },
  expect: 201,
})
const serverId = str(server, 'id')

step('节点（划进池）')
const nodeType = 'shadowsocks'
const node = await call(ADM, '/v1/nodes', {
  token: admin,
  idem: true,
  body: {
    name: 'smoke-node-1',
    server_id: serverId,
    pool_id: poolId,
    node_type: nodeType,
    server_host: 'edge.example.test',
    server_port: 8388,
    kernel: 'auto',
    traffic_rate: 1,
    display_name: 'Smoke 01',
    protocol_config: { method: 'aes-256-gcm' },
  },
  expect: 201,
})
const nodeId = str(node, 'id')
let nodeRow = num(node, 'row_version')

step('节点状态逐级推进到 active')
// 数据库触发器只允许 standby → canary → active 进入 active；批量上线接口要求服务器先 ready，
// 而服务器 ready 又要求有 active 节点，所以只能走这条逐级路径
for (const status of ['bootstrapping', 'attesting', 'installing', 'validating', 'standby', 'canary', 'active']) {
  const r = await call(ADM, `/v1/nodes/${nodeId}/status`, { token: admin, idem: true, body: { row_version: nodeRow, status, reason: 'smoke seed' } })
  nodeRow = num(r, 'row_version')
}

step('服务器就绪')
await call(ADM, `/v1/servers/${serverId}/status`, {
  token: admin,
  body: { status: 'ready', row_version: num(server, 'row_version'), reason: 'smoke seed' },
})

step('签发服务端令牌并经 UniProxy 上报心跳')
const serverToken = str(await call(ADM, `/v1/nodes/${nodeId}/server-token`, { method: 'POST', token: admin, idem: true, expect: 201 }), 'token')
await call(NODE, `/api/v1/server/UniProxy/status?node_id=${nodeId}&node_type=${nodeType}`, {
  token: serverToken,
  body: { cpu: 12.5, mem: { total: 1073741824, used: 536870912 }, swap: { total: 0, used: 0 }, disk: { total: 10737418240, used: 2147483648 } },
})

// ============================================================================
//  套餐：向导一次建好并发布，池绑在这个版本上（用户买的是哪个版本就看哪个版本的池）
// ============================================================================

step('套餐向导：建套餐、绑池、定价、发布')
const plan = await call(ADM, '/v1/plans/complete', {
  token: admin,
  idem: true,
  body: {
    code: 'smoke-std',
    name: 'Smoke Standard',
    visibility: 'public',
    traffic_gb: 100,
    max_devices: 3,
    pool_ids: [poolId],
    prices: [{ billing_interval: 'month', interval_count: 1, unit_amount: 990, currency: 'CNY', trial_days: 0 }],
    publish: true,
  },
  expect: 201,
})
const planId = str(plan, 'plan.id')

// ============================================================================
//  演示支付渠道：后台没有新建渠道的接口，只能照 deploy/seed-demo.sql 那一句插入
//  （那份种子还会建一套 USD 目录，这里只取渠道；币种加上 CNY 以便付 CNY 套餐）
// ============================================================================

step('演示支付渠道（SQL，照 seed-demo.sql）')
const providerSql = `
BEGIN;
SET LOCAL app.tenant_id = '${TENANT}';
INSERT INTO payment_providers (tenant_id, code, adapter, display_name, supported_currencies, enabled, accepting_new)
VALUES ('${TENANT}', 'demo', 'demo_hmac', '演示支付渠道', ARRAY['CNY','USD']::app.currency_code[], true, true)
ON CONFLICT (tenant_id, code) DO UPDATE SET enabled = true, accepting_new = true;
COMMIT;`
const psql = spawnSync('docker', ['exec', '-i', PG_CONTAINER, 'psql', '-X', '-q', '-v', 'ON_ERROR_STOP=1', '-U', 'aegis', '-d', 'aegis'], {
  input: providerSql,
  encoding: 'utf8',
})
if (psql.status !== 0) throw new Error(`插入演示支付渠道失败：${psql.stderr}`)

// ============================================================================
//  门户用户：注册、登录；第二个用户走邀请码，让推荐列表不空
// ============================================================================

async function register(email: string, password: string, inviteCode?: string): Promise<string> {
  const start = await call(PUB, '/v1/auth/register/start', { body: { email, ...(inviteCode ? { invite_code: inviteCode } : {}) } })
  // 新库默认不开邮箱验证，不回 dev_code；开了就用它
  const code = typeof start.dev_code === 'string' ? start.dev_code : ''
  const done = await call(PUB, '/v1/auth/register/complete', {
    body: { registration_token: str(start, 'registration_token'), code, password },
    expect: [200, 201],
  })
  return str(done, 'user_id')
}

async function portalLogin(email: string, password: string): Promise<string> {
  return str(await call(PUB, '/v1/auth/login', { body: { email, password } }), 'access_token')
}

step('门户用户注册与登录')
const userEmail = 'smoke-user@example.test'
const userPassword = `Smoke-User-${randomUUID()}`
const userId = await register(userEmail, userPassword)
const user = await portalLogin(userEmail, userPassword)

step('推荐：佣金比例 10%，第二个用户经邀请码注册')
await call(ADM, '/v1/commission/config', { token: admin, body: { rate_percent: 10 } })
const invite = await call(PUB, '/v1/me/invite', { token: user })
const inviteCode = str(invite, 'invite.code')
const inviteeEmail = 'smoke-invitee@example.test'
const inviteeId = await register(inviteeEmail, `Smoke-Invitee-${randomUUID()}`, inviteCode)

// ============================================================================
//  订单：一张经演示渠道付清（开出订阅），一张留在待支付
// ============================================================================

step('门户下单')
const catalog = await call(PUB, '/v1/plans', { token: user })
const plans = catalog.plans as Json[] | undefined
const onSale = plans?.find((p) => p.id === planId)
if (!onSale) throw new Error(`门户套餐目录里没有刚发布的套餐：${JSON.stringify(catalog).slice(0, 300)}`)
const priceId = str((onSale.prices as Json[])[0] ?? {}, 'id')
const order = await call(PUB, '/v1/orders', { token: user, idem: true, body: { plan_id: planId, price_id: priceId, use_balance: 0 }, expect: [200, 201] })
const orderId = str(order, 'order_id')

step('演示渠道支付：发起支付意图，再送签名回调')
await call(PUB, `/v1/orders/${orderId}/pay`, { token: user, body: { provider: 'demo' }, expect: [200, 201] })
const amount = num(order, 'payable_amount')
const currency = str(order, 'currency')
const eventId = `evt-smoke-${randomUUID()}`
const signature = createHmac('sha256', MASTER_KEY).update(`${eventId}|${orderId}|${amount}|${currency}`).digest('hex')
const paid = await call(PUB, '/v1/webhooks/payments/demo', {
  idem: `wh-${eventId}`,
  headers: { 'X-Aegis-Signature': signature },
  body: { event_id: eventId, event_type: 'payment.succeeded', payment_id: `pay-${eventId}`, order_id: orderId, amount, currency, fee: 0 },
})
const subscriptionId = str(paid, 'subscription_id')

step('第二张订单留在待支付')
const pendingOrderId = str(
  await call(PUB, '/v1/orders', { token: user, idem: true, body: { plan_id: planId, price_id: priceId, use_balance: 0 }, expect: [200, 201] }),
  'order_id',
)

// ============================================================================
//  支持、内容与营销各一条
// ============================================================================

step('门户工单与后台回复')
const ticket = await call(PUB, '/v1/support/tickets', {
  token: user,
  idem: true,
  body: { subject: '无法连接节点', category: 'technical', body: '今天开始所有节点都连不上，请帮忙看看。' },
  expect: [200, 201],
})
const ticketId = str(ticket, 'id')
await call(ADM, `/v1/tickets/${ticketId}/reply`, { token: admin, idem: true, body: { body: '已收到，正在排查节点。' }, expect: [200, 201] })

step('公告')
const announcementId = str(
  await call(ADM, '/v1/announcements', { token: admin, idem: true, body: { title: '系统维护通知', body: '本周六凌晨维护。', severity: 'info', publish: true }, expect: [200, 201] }),
  'id',
)

step('知识库文章')
const page = await call(ADM, '/v1/content-pages', {
  token: admin,
  idem: true,
  body: { slug: 'getting-started', kind: 'kb_article', title: '快速开始', body: '下载客户端并导入订阅链接即可开始使用。', status: 'published' },
  expect: [200, 201],
})
const pageSlug = str(page, 'page.slug')

step('礼品卡：模板、一批码，门户兑一张（余额入账，钱包不空）')
const giftCardId = str(
  await call(ADM, '/v1/gift-cards', { token: admin, body: { name: 'Smoke 10 元卡', type: 'general', rewards: { balance: 1000 } }, expect: [200, 201] }),
  'template.id',
)
const codes = await call(ADM, `/v1/gift-cards/${giftCardId}/codes`, { token: admin, idem: true, body: { count: 3, prefix: 'SMK' }, expect: [200, 201] })
const giftCode = (codes.sample as string[] | undefined)?.[0]
if (!giftCode) throw new Error(`礼品卡批次没有回样例码：${JSON.stringify(codes).slice(0, 300)}`)
await call(PUB, '/v1/gift-cards/redeem', { token: user, idem: true, body: { code: giftCode }, expect: [200, 201] })

step('优惠券')
const couponId = str(
  await call(ADM, '/v1/coupons', { token: admin, body: { code: 'SMOKE10', discount_type: 'percent', discount_value: 1000, currency: 'CNY' }, expect: [200, 201] }),
  'id',
)

// ============================================================================
//  收尾：确认各列表真的不空，再把 id 与门户账号交给第 ③ 步
// ============================================================================

/** 响应本身是数组，或顶层第一个数组字段的长度 */
function listLength(o: unknown): number {
  if (Array.isArray(o)) return o.length
  for (const v of Object.values(o as Json)) if (Array.isArray(v)) return v.length
  return -1
}

step('核对列表不空')
const lists: Array<[string, string, string]> = [
  [ADM, '/v1/node-pools', admin],
  [ADM, '/v1/servers', admin],
  [ADM, '/v1/nodes', admin],
  [ADM, '/v1/plans', admin],
  [ADM, '/v1/users', admin],
  [ADM, '/v1/orders', admin],
  [ADM, '/v1/tickets', admin],
  [ADM, '/v1/announcements', admin],
  [ADM, '/v1/content-pages', admin],
  [ADM, '/v1/gift-cards', admin],
  [ADM, '/v1/coupons', admin],
  [PUB, '/v1/plans', user],
  [PUB, '/v1/orders', user],
  [PUB, '/v1/me/subscriptions', user],
  [PUB, `/v1/me/subscriptions/${subscriptionId}/nodes`, user],
  [PUB, '/v1/support/tickets', user],
  [PUB, '/v1/me/announcements', user],
  [PUB, '/v1/content/pages', user],
]
const empty: string[] = []
for (const [base, path, token] of lists) {
  const n = listLength(await call(base, path, { token }))
  console.log(`    ${base === ADM ? 'admin ' : 'portal'} ${path}: ${n}`)
  if (n <= 0) empty.push(path)
}
if (empty.length > 0) throw new Error(`这些列表为空或没有数组字段：${empty.join(', ')}`)

writeFileSync(
  join(stateDir, 'seed.json'),
  JSON.stringify(
    {
      portal: { email: userEmail, password: userPassword, user_id: userId },
      invitee_id: inviteeId,
      pool_id: poolId,
      server_id: serverId,
      node_id: nodeId,
      plan_id: planId,
      price_id: priceId,
      order_id: orderId,
      pending_order_id: pendingOrderId,
      subscription_id: subscriptionId,
      ticket_id: ticketId,
      announcement_id: announcementId,
      page_slug: pageSlug,
      gift_card_id: giftCardId,
      coupon_id: couponId,
    },
    null,
    2,
  ),
  { mode: 0o600 },
)
console.log(`==> 造数据完成：${join(stateDir, 'seed.json')}`)
