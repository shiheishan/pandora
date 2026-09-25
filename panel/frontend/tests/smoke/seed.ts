/**
 * [INPUT]: 依赖 run-smoke-stack.sh 写在状态目录的 smoke.env（网关地址、管理员账号、PG 容器名）与 gateway.env（主密钥，演示渠道验签用），依赖同目录 hook-receiver.ts（插件投递接收端），依赖 node:fs / node:child_process / node:crypto 与全局 fetch
 * [OUTPUT]: 命令行脚本 `node seed.ts <状态目录>`：经真实网关跑一条业务链，让后台与门户各列表都至少一条，把后续冒烟要用的 id、门户账号与节点运行令牌写进 <状态目录>/seed.json；任何一步状态码不符即退出 1
 * [POS]: tests/smoke 的造数据步骤，在 run-smoke-stack.sh up 之后、形状校验之前运行；只发请求不做形状断言（那是 *.smoke.ts 的事）。节点走真实的两段式接入与一步上线（R108 / R113）；两处 SQL 夹具（演示支付渠道、提现申请）都是产品接口造不出来的，各自写明原因
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { spawn, spawnSync } from 'node:child_process'
import { createHash, createHmac, generateKeyPairSync, randomBytes, randomUUID, sign } from 'node:crypto'
import { appendFileSync, readFileSync, writeFileSync } from 'node:fs'
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
// 插件投递接收端的端口：只在 runner 上、只听回环
const HOOK_PORT = 18089

// ============================================================================
//  HTTP：每个请求都声明期望状态码，不符就带响应片段退出
// ============================================================================

type Json = Record<string, unknown>

interface Call {
  method?: string
  token?: string
  body?: unknown
  /** 已序列化好的请求体：签名覆盖的是原始字节，发出去的必须是同一份 */
  rawBody?: string
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
  const payload = c.rawBody ?? (c.body === undefined ? undefined : JSON.stringify(c.body))
  if (payload !== undefined) headers['Content-Type'] = 'application/json'
  if (c.token) headers.Authorization = `Bearer ${c.token}`
  if (c.idem) headers['Idempotency-Key'] = c.idem === true ? randomUUID() : c.idem
  const method = c.method ?? (payload === undefined ? 'GET' : 'POST')
  const res = await fetch(base + path, { method, headers, ...(payload === undefined ? {} : { body: payload }) })
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

/** SQL 夹具：以迁移账号直连冒烟库。只用于产品接口造不出来的数据，每处都写明为什么 */
function sqlFixture(what: string, sql: string): void {
  const r = spawnSync('docker', ['exec', '-i', PG_CONTAINER, 'psql', '-X', '-q', '-v', 'ON_ERROR_STOP=1', '-U', 'aegis', '-d', 'aegis'], { input: sql, encoding: 'utf8' })
  if (r.status !== 0) throw new Error(`SQL 夹具「${what}」失败：${r.stderr}`)
}

/** 演示渠道的签名回调：HMAC-SHA256(主密钥, event_id|order_id|amount|currency)，与 demo 适配器同一口径 */
async function demoWebhook(orderId: string, amount: number, currency: string): Promise<Json> {
  const eventId = `evt-smoke-${randomUUID()}`
  const signature = createHmac('sha256', MASTER_KEY).update(`${eventId}|${orderId}|${amount}|${currency}`).digest('hex')
  return call(PUB, '/v1/webhooks/payments/demo', {
    idem: `wh-${eventId}`,
    headers: { 'X-Aegis-Signature': signature },
    body: { event_id: eventId, event_type: 'payment.succeeded', payment_id: `pay-${eventId}`, order_id: orderId, amount, currency, fee: 0 },
  })
}

// ============================================================================
//  后台登录。登录签发的令牌自带 15 分钟 reauth 窗口，整条链几分钟内跑完，用不上重认证
// ============================================================================

step('管理员登录')
const admin = str(await call(ADM, '/v1/auth/login', { body: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD } }), 'access_token')

// ============================================================================
//  池与套餐先行：上线接口发现「所在池没绑套餐」会带 warnings，所以先把池绑在发布版本上
//  （用户买的是哪个版本就看哪个版本的池；没划进池的节点不服务任何用户，R104、R105）
// ============================================================================

step('节点池')
const poolId = str(await call(ADM, '/v1/node-pools', { token: admin, body: { name: 'Smoke Pool', code: 'smoke-pool' }, expect: [200, 201] }), 'id')

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
//  新服务器 + 新节点一步上线（R108 / R113）：后台建服务器与节点 → 节点抽屉签发接入令牌 →
//  节点端两段式接入（Ed25519 签名，与 pdnd/panel/enrollment.go 同一套规范串）→ POST activate。
//  不再逐级调旧 status 接口。运行令牌由「节点」自己生成，面板只存哈希，UniProxy 用它认证
// ============================================================================

step('服务器（草稿）')
const serverId = str(
  await call(ADM, '/v1/servers', {
    token: admin,
    body: { name: 'smoke-srv-1', hostname: 'smoke.example.test', public_ipv4: '203.0.113.10', capacity_nodes: 4 },
    expect: 201,
  }),
  'id',
)

step('节点（草稿，划进池）')
const nodeType = 'shadowsocks'
const nodeName = 'smoke-node-1'
const nodeId = str(
  await call(ADM, '/v1/nodes', {
    token: admin,
    idem: true,
    body: {
      name: nodeName,
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
  }),
  'id',
)

step('签发接入令牌（节点抽屉「身份」页同一个接口）')
// 令牌哈希里带节点名，接入时 node_name 必须一字不差；按节点签发而不是按服务器——服务器那条会拿服务器名当节点名
const bootstrapToken = str(
  await call(ADM, '/v1/nodes/bootstrap-token', { token: admin, idem: true, body: { node_name: nodeName, ttl_minutes: 30, server_id: serverId }, expect: 201 }),
  'token',
)

step('节点端两段式接入：begin → commit')
const { publicKey, privateKey } = generateKeyPairSync('ed25519')
// 公钥是原始 32 字节的标准 base64（不是 SPKI）
const publicKeyRaw = publicKey.export({ format: 'der', type: 'spki' }).subarray(-32).toString('base64')
const sha256b64 = (text: string) => createHash('sha256').update(text, 'utf8').digest('base64')
const signB64 = (canonical: string) => sign(null, Buffer.from(canonical, 'utf8'), privateKey).toString('base64')
// 运行令牌：节点生成，32 字节 base64url 无填充（与 pdnd 同形），面板只收 sha256
const runtimeToken = randomBytes(32).toString('base64url')
const requestId = randomUUID()
const beginPath = '/v1/nodes/enrollments'
const beginBody = JSON.stringify({
  token: bootstrapToken,
  node_name: nodeName,
  request_id: requestId,
  public_key: publicKeyRaw,
  runtime_token_sha256: sha256b64(runtimeToken),
  agent_version: 'smoke',
  hostname: 'smoke-host',
})
const begin = await call(NODE, beginPath, {
  rawBody: beginBody,
  headers: { 'X-Enrollment-Signature': signB64(['PANDORA-NODE-ENROLL-BEGIN-V1', 'POST', beginPath, requestId, sha256b64(beginBody)].join('\n')) },
  expect: 201,
})
const enrollmentId = str(begin, 'enrollment_id')
if (str(begin, 'node_id') !== nodeId) throw new Error(`接入落到了别的节点上：${JSON.stringify(begin)}`)
const serial = num(begin, 'serial')

const commitPath = `/v1/nodes/enrollments/${enrollmentId}/commit`
// 四个摘要在非生产且未配置发布物钉值时只校验格式（64 位小写十六进制）
const commitBody = JSON.stringify({
  agent_version: 'smoke',
  architecture: 'amd64',
  binary_sha256: '1'.repeat(64),
  config_sha256: '2'.repeat(64),
  unit_sha256: '3'.repeat(64),
  preflight_sha256: '4'.repeat(64),
})
// 时间戳精确到秒的 RFC3339（toISOString 带毫秒，会被拒）；nonce 16 字节 base64url 无填充，一次一用
const ts = new Date().toISOString().replace(/\.\d{3}Z$/, 'Z')
const nonce = randomBytes(16).toString('base64url')
const commit = await call(NODE, commitPath, {
  rawBody: commitBody,
  headers: {
    'X-Node-Id': nodeId,
    'X-Node-Serial': String(serial),
    'X-Node-Ts': ts,
    'X-Node-Nonce': nonce,
    'X-Node-Sig': signB64(['PANDORA-NODE-ENROLLMENT-V1', 'POST', commitPath, enrollmentId, nodeId, String(serial), ts, nonce, sha256b64(commitBody)].join('\n')),
  },
})
if (commit.state !== 'committed') throw new Error(`接入提交后状态不是 committed：${JSON.stringify(commit)}`)

step('一步上线：POST activate')
// 接入推了两次生命周期（draft → bootstrapping → attesting），row_version 要重读
const listed = ((await call(ADM, '/v1/nodes?limit=1000', { token: admin })).nodes as Json[]).find((n) => n.id === nodeId)
if (!listed) throw new Error('节点列表里找不到刚接入的节点')
const activated = await call(ADM, `/v1/nodes/${nodeId}/activate`, { token: admin, idem: true, body: { row_version: num(listed, 'row_version') } })
if (activated.status !== 'active' || activated.serving_status !== 'active') throw new Error(`上线后状态不对：${JSON.stringify(activated).slice(0, 300)}`)
// warnings 缺省表示没有提示（R113）；出现就说明池或套餐绑定没接上，节点不会服务任何人
if (activated.warnings !== undefined) throw new Error(`上线带了提示：${JSON.stringify(activated.warnings)}`)

step('UniProxy 心跳（运行令牌认证）')
const uni = (path: string) => `/api/v1/server/UniProxy/${path}?node_id=${nodeId}&node_type=${nodeType}`
await call(NODE, uni('status'), {
  token: runtimeToken,
  body: { cpu: 12.5, mem: { total: 1073741824, used: 536870912 }, swap: { total: 0, used: 0 }, disk: { total: 10737418240, used: 2147483648 } },
})

// ============================================================================
//  演示支付渠道（SQL 夹具）：后台没有新建渠道的接口，只能照 deploy/seed-demo.sql 那一句插入
//  （那份种子还会建一套 USD 目录，这里只取渠道；币种加上 CNY 以便付 CNY 套餐）
// ============================================================================

step('演示支付渠道（SQL 夹具，照 seed-demo.sql）')
sqlFixture(
  '演示支付渠道',
  `BEGIN;
SET LOCAL app.tenant_id = '${TENANT}';
INSERT INTO payment_providers (tenant_id, code, adapter, display_name, supported_currencies, enabled, accepting_new)
VALUES ('${TENANT}', 'demo', 'demo_hmac', '演示支付渠道', ARRAY['CNY','USD']::app.currency_code[], true, true)
ON CONFLICT (tenant_id, code) DO UPDATE SET enabled = true, accepting_new = true;
COMMIT;`,
)

// ============================================================================
//  插件钩子：必须先于事件存在（Emit 与业务写入同一事务入队）。接收端是本机回环上的独立进程，
//  devMode（AEGIS_ENV 非 production）下面板本来就放行回环地址——不改 Go、不放宽任何校验。
//  投递由 aegis-public 的扫描器异步发出（启动 20 秒后第一次，之后每 60 秒），这里不等
// ============================================================================

step('插件钩子：拉起本机接收端，建钩子订阅 ticket.created')
const receiver = spawn(process.execPath, [join(import.meta.dirname, 'hook-receiver.ts'), stateDir, String(HOOK_PORT)], { detached: true, stdio: 'ignore' })
receiver.unref()
// 进程号记进状态目录，run-smoke-stack.sh down 一并收掉
appendFileSync(join(stateDir, 'pids'), `${receiver.pid}\n`)
for (let i = 0; ; i++) {
  const ready = await fetch(`http://127.0.0.1:${HOOK_PORT}/ready`).then((r) => r.ok, () => false)
  if (ready) break
  if (i > 50) throw new Error('插件投递接收端 10 秒内没起来')
  await new Promise((r) => setTimeout(r, 200))
}
const hookCode = 'smoke-hook'
await call(ADM, '/v1/plugin-hooks', {
  token: admin,
  idem: true,
  body: {
    code: hookCode,
    name: 'Smoke Hook',
    description: '',
    enabled: true,
    events: ['ticket.created'],
    endpoint_url: `http://127.0.0.1:${HOOK_PORT}/hook`,
    secret: '',
    timeout_ms: 0,
    max_attempts: 0,
  },
})

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
const onSale = (catalog.plans as Json[] | undefined)?.find((p) => p.id === planId)
if (!onSale) throw new Error(`门户套餐目录里没有刚发布的套餐：${JSON.stringify(catalog).slice(0, 300)}`)
const priceId = str((onSale.prices as Json[])[0] ?? {}, 'id')
const order = await call(PUB, '/v1/orders', { token: user, idem: true, body: { plan_id: planId, price_id: priceId, use_balance: 0 }, expect: [200, 201] })
const orderId = str(order, 'order_id')
const amount = num(order, 'payable_amount')
const currency = str(order, 'currency')

step('演示渠道支付：发起支付意图，再送签名回调')
await call(PUB, `/v1/orders/${orderId}/pay`, { token: user, body: { provider: 'demo' }, expect: [200, 201] })
const subscriptionId = str(await demoWebhook(orderId, amount, currency), 'subscription_id')

step('第二张订单留在待支付')
const pendingOrderId = str(
  await call(PUB, '/v1/orders', { token: user, idem: true, body: { plan_id: planId, price_id: priceId, use_balance: 0 }, expect: [200, 201] }),
  'order_id',
)

step('挂账：对已付清的订单再送一笔新的回调（新 event_id / payment_id），落进 excess_capture')
// 挂账只由支付回调产生（已付或已取消的订单又收到钱），没有后台新建接口；这是最便宜的真实路径
await demoWebhook(orderId, amount, currency)

// ============================================================================
//  节点上报：取用户列表 → 流量 → 在线 IP。流量排行、按日用量、在线设备只由节点上报产生，
//  都是同步写入，不等后台任务。uid 是订阅的 node_uid，只能从节点拿到的用户列表里取
// ============================================================================

step('节点取用户列表，上报流量与在线 IP')
const userList = await call(NODE, uni('user'), { token: runtimeToken })
// 响应可能被包一层 data，也可能不包：两种都认
const nodeUsers = ((userList.users ?? (userList.data as Json | undefined)?.users) as Json[] | undefined) ?? []
if (nodeUsers.length === 0) throw new Error(`上线的节点取不到任何用户：${JSON.stringify(userList).slice(0, 300)}`)
const nodeUid = String(nodeUsers[0]!.id)
await call(NODE, uni('push'), { token: runtimeToken, body: { [nodeUid]: [1048576, 4194304] } })
await call(NODE, uni('alive'), { token: runtimeToken, body: { [nodeUid]: ['198.51.100.7'] } })

// ============================================================================
//  支持、内容与营销
// ============================================================================

step('门户工单（触发插件钩子 ticket.created）与后台回复')
const ticket = await call(PUB, '/v1/support/tickets', {
  token: user,
  idem: true,
  body: { subject: '无法连接节点', category: 'technical', body: '今天开始所有节点都连不上，请帮忙看看。' },
  expect: [200, 201],
})
const ticketId = str(ticket, 'id')
await call(ADM, `/v1/tickets/${ticketId}/reply`, { token: admin, idem: true, body: { body: '已收到，正在排查节点。' }, expect: [200, 201] })

step('工单快捷回复与用户组')
await call(ADM, '/v1/ticket-macros', { token: admin, body: { title: '已收到', body: '已收到，我们正在处理。', sort_order: 0 } })
await call(ADM, '/v1/user-groups', { token: admin, body: { code: 'smoke-grp', name: 'Smoke Group', description: '' } })

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

step('优惠券，并用它下一张单（兑换记录在下单时落定，不必付款）')
const couponId = str(
  await call(ADM, '/v1/coupons', { token: admin, body: { code: 'SMOKE10', discount_type: 'percent', discount_value: 1000, currency: 'CNY' }, expect: [200, 201] }),
  'id',
)
const couponOrderId = str(
  await call(PUB, '/v1/orders', { token: user, idem: true, body: { plan_id: planId, price_id: priceId, coupon_code: 'SMOKE10', use_balance: 0 }, expect: [200, 201] }),
  'order_id',
)

step('流量包：后台上架，门户用余额买一个（应付为 0，同步履约，我的流量包不空）')
const pack = await call(ADM, '/v1/traffic-packs', {
  token: admin,
  idem: true,
  body: { name: 'Smoke 10GB', traffic_bytes: 10737418240, currency: 'CNY', unit_amount: 500, recommended: false, sort_order: 0 },
  expect: [200, 201],
})
const packId = str(pack, (pack.pack as Json | undefined) ? 'pack.id' : 'id')
await call(PUB, '/v1/me/traffic-pack-orders', { token: user, idem: true, body: { pack_id: packId, use_balance: 500, coupon_code: '' }, expect: [200, 201] })

step('收入调整与手动流量重置')
await call(ADM, '/v1/revenue/adjustments', { token: admin, idem: true, body: { currency: 'CNY', amount: 1000, reason: 'smoke adjustment', effective_on: '' }, expect: [200, 201] })
await call(ADM, `/v1/users/${userId}/traffic-reset`, { token: admin, idem: true, body: { note: 'smoke manual reset' }, expect: [200, 201] })

// ============================================================================
//  提现申请（SQL 夹具）。产品路径走不通冒烟：佣金要被邀请人付款后由 aegis-admin 的
//  SettleMatured 解冻（启动时一次、之后每小时），且推荐人与买家同 IP 时佣金被标为待复核、
//  没有接口能解除——冒烟里所有请求都来自 127.0.0.1。按 withdrawals 的守卫触发器只能插 requested
// ============================================================================

step('提现申请（SQL 夹具）')
sqlFixture(
  '提现申请',
  `BEGIN;
SET LOCAL app.tenant_id = '${TENANT}';
SET LOCAL app.actor_id = '${userId}';
INSERT INTO withdrawals (tenant_id, user_id, currency, amount, status)
VALUES ('${TENANT}', '${userId}', 'CNY', 100, 'requested');
COMMIT;`,
)

// ============================================================================
//  收尾：确认各列表真的不空，再把 id 与门户账号交给 *.smoke.ts
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
  [ADM, '/v1/ticket-macros', admin],
  [ADM, '/v1/user-groups', admin],
  [ADM, '/v1/announcements', admin],
  [ADM, '/v1/content-pages', admin],
  [ADM, '/v1/gift-cards', admin],
  [ADM, '/v1/coupons', admin],
  [ADM, `/v1/coupons/${couponId}/redemptions`, admin],
  [ADM, '/v1/traffic-packs', admin],
  [ADM, '/v1/revenue/adjustments', admin],
  [ADM, '/v1/traffic-resets', admin],
  [ADM, '/v1/late-payments', admin],
  [ADM, '/v1/withdrawals', admin],
  [ADM, '/v1/plugin-hooks', admin],
  [ADM, `/v1/plugin-hooks/${hookCode}/deliveries`, admin],
  [ADM, '/v1/dashboard/traffic/nodes?range=24h&limit=5', admin],
  [ADM, '/v1/dashboard/traffic/users?range=24h&limit=5', admin],
  [PUB, '/v1/plans', user],
  [PUB, '/v1/orders', user],
  [PUB, '/v1/me/subscriptions', user],
  [PUB, `/v1/me/subscriptions/${subscriptionId}/nodes`, user],
  [PUB, '/v1/traffic-packs', user],
  [PUB, '/v1/me/traffic-packs', user],
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
      node_type: nodeType,
      // 节点自己生成的运行令牌：冒烟以节点身份取用户列表要用；状态目录 0600，只活在 runner 上
      node_runtime_token: runtimeToken,
      plan_id: planId,
      price_id: priceId,
      order_id: orderId,
      pending_order_id: pendingOrderId,
      coupon_order_id: couponOrderId,
      subscription_id: subscriptionId,
      ticket_id: ticketId,
      announcement_id: announcementId,
      page_slug: pageSlug,
      gift_card_id: giftCardId,
      coupon_id: couponId,
      pack_id: packId,
      hook_code: hookCode,
    },
    null,
    2,
  ),
  { mode: 0o600 },
)
console.log(`==> 造数据完成：${join(stateDir, 'seed.json')}`)
