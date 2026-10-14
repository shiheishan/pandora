import { spawn, spawnSync } from 'node:child_process'
import { createCipheriv, createHash, hkdfSync, randomBytes, randomUUID } from 'node:crypto'
import { appendFileSync, mkdirSync, openSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

// ============================================================================
//  输入
// ============================================================================

const stateDirArg = process.argv[2]
if (!stateDirArg) {
  console.error('用法: node seed.ts <状态目录>')
  process.exit(2)
}
// 函数里也要用：模块顶层的收窄不会带进函数体
const stateDir: string = stateDirArg

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
const stackEnv = readEnvFile(join(stateDir, 'stack.env'))
const PUB = need(smokeEnv, 'SMOKE_PUBLIC_BASE')
const ADM = need(smokeEnv, 'SMOKE_ADMIN_BASE')
const ADMIN_EMAIL = need(smokeEnv, 'SMOKE_ADMIN_EMAIL')
const ADMIN_PASSWORD = need(smokeEnv, 'SMOKE_ADMIN_PASSWORD')
const PG_CONTAINER = need(smokeEnv, 'SMOKE_PG_CONTAINER')
const PG_DB = need(smokeEnv, 'SMOKE_PG_DB')
const SMOKE_NODESIM = need(smokeEnv, 'SMOKE_NODESIM')
const MASTER_KEY = Buffer.from(need(stackEnv, 'PANDORA_SECRETS_MASTER_KEY'), 'base64')
// 插件投递接收端的端口：只在 runner 上、只听回环
const HOOK_PORT = 18089

// 易支付渠道：商户号与密钥现场生成，只活在本进程内存，不写进 seed.json
const PROVIDER = 'smokepay'
const EPAY_MERCHANT = `smk${randomBytes(3).toString('hex')}`
const EPAY_KEY = randomBytes(16).toString('hex')

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

// 后台请求之间留 300ms，免得一口气打满按路由的限流
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

/** SQL 夹具：以容器里的超级用户 postgres 直连冒烟库（绕开只追加表的触发器与应用角色的权限）。只用于产品接口造不出来的数据，每处都写明为什么 */
function sqlFixture(what: string, sql: string): void {
  const r = spawnSync('docker', ['exec', '-i', PG_CONTAINER, 'psql', '-X', '-q', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', PG_DB], { input: sql, encoding: 'utf8' })
  if (r.status !== 0) throw new Error(`SQL 夹具「${what}」失败：${r.stderr}`)
}

/** 易支付 MD5 签名：非空参数按名升序拼 k=v&，末尾接密钥（与 panel/tests/epay_e2e.sh 同一口径） */
function epaySign(params: Record<string, string>, key: string): string {
  const kv = Object.entries(params).filter(([k, v]) => v && k !== 'sign' && k !== 'sign_type')
  kv.sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
  return createHash('md5')
    .update(kv.map(([k, v]) => `${k}=${v}`).join('&') + key)
    .digest('hex')
}

/** 易支付异步通知：GET 真实回调地址，回执须为 success */
async function epayNotify(outTradeNo: string, money: string): Promise<void> {
  const params: Record<string, string> = {
    pid: EPAY_MERCHANT,
    trade_no: `SMK${randomUUID().replace(/-/g, '').slice(0, 16)}`,
    out_trade_no: outTradeNo,
    type: 'alipay',
    name: 'smoke',
    money,
    trade_status: 'TRADE_SUCCESS',
  }
  params.sign = epaySign(params, EPAY_KEY)
  params.sign_type = 'MD5'
  const res = await fetch(`${PUB}/v1/webhooks/payments/${PROVIDER}?${new URLSearchParams(params)}`)
  const text = (await res.text()).trim()
  if (text !== 'success') throw new Error(`易支付回调回执不是 success：${text.slice(0, 300)}`)
}

/** 发起支付并从 redirect_url 取商户单号与金额，再送签名回调 */
async function payOrder(token: string, orderId: string): Promise<{ outTradeNo: string; money: string }> {
  const pay = await call(PUB, `/v1/orders/${orderId}/pay`, { token, body: { provider: PROVIDER }, expect: [200, 201] })
  const url = new URL(str(pay, 'redirect_url'))
  const outTradeNo = url.searchParams.get('out_trade_no')
  const money = url.searchParams.get('money')
  if (!outTradeNo || !money) throw new Error(`redirect_url 缺少 out_trade_no 或 money：${str(pay, 'redirect_url')}`)
  await epayNotify(outTradeNo, money)
  return { outTradeNo, money }
}

async function subscriptionOf(token: string, planId: string): Promise<string> {
  const subs = ((await call(PUB, '/v1/me/subscriptions', { token })).subscriptions as Json[] | undefined) ?? []
  const row = subs.find((s) => s.plan_id === planId)
  if (!row) throw new Error(`订阅列表里没有 plan_id=${planId}：${JSON.stringify(subs).slice(0, 300)}`)
  return str(row, 'id')
}

interface SimNodeStatus {
  user_count: number
}

interface SimServerStatus {
  enrolled: boolean
  connected: boolean
  traffic_reports_acked: number
  online_reports_sent: number
  nodes: Record<string, SimNodeStatus | undefined>
}

interface SimSnapshot {
  servers: Record<string, SimServerStatus | undefined>
}

function simStatus(): SimSnapshot | null {
  try {
    return JSON.parse(readFileSync(join(stateDir, 'nodesim', 'status.json'), 'utf8')) as SimSnapshot
  } catch {
    return null
  }
}

function tailLog(path: string, lines: number): string {
  try {
    const all = readFileSync(path, 'utf8').split('\n')
    return all.slice(-lines).join('\n')
  } catch {
    return '(日志读不到)'
  }
}

async function waitSim(desc: string, ok: (s: SimSnapshot) => boolean, timeoutMs: number): Promise<SimSnapshot> {
  const deadline = Date.now() + timeoutMs
  const statusPath = join(stateDir, 'nodesim', 'status.json')
  const logPath = join(stateDir, 'logs', 'nodesim.log')
  while (Date.now() < deadline) {
    const s = simStatus()
    if (s && ok(s)) return s
    await new Promise((r) => setTimeout(r, 1000))
  }
  let statusText = '(读不到)'
  try {
    statusText = readFileSync(statusPath, 'utf8')
  } catch {
    /* ignore */
  }
  throw new Error(`${desc} 超时（${timeoutMs}ms）。status.json：${statusText.slice(0, 800)}\n\nnodesim.log 末 30 行：\n${tailLog(logPath, 30)}`)
}

// ============================================================================
//  后台登录。口令登录本身算一次重认证（15 分钟窗口记在会话里），整条链几分钟内跑完
// ============================================================================

step('管理员登录')
const admin = str(await call(ADM, '/v1/auth/login', { body: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD } }), 'access_token')

// 冒烟按人民币站点走：礼品卡余额按站点币种入账（缺省 USD），不设就抵扣不了下面的 CNY 流量包。
// 站点币种没有后台入口（旧栈同样只能改库），用 SQL 夹具设
sqlFixture('站点币种 CNY', `INSERT INTO settings (key, value) VALUES ('site.currency', '"CNY"'::jsonb) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();`)

// ============================================================================
//  池与套餐草稿先行。两条规则互相咬合：上线接口发现「所在池没绑任何套餐」会带 warnings，
//  而发布版本要求绑定池里已有可服务节点——所以先用向导建草稿版本并绑池（草稿绑定就算绑了），
//  节点上线后再发布这个版本（用户买的是哪个版本就看哪个版本的池；无池节点不服务任何人，R104、R105）
// ============================================================================

step('节点池')
const poolId = str(await call(ADM, '/v1/node-pools', { token: admin, body: { name: 'Smoke Pool', code: 'smoke-pool' }, expect: [200, 201] }), 'id')

step('套餐向导：建套餐、绑池、定价（草稿，暂不发布）')
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
    publish: false,
  },
  expect: 201,
})
const planId = str(plan, 'plan.id')
const versionId = str(plan, 'version_id')

// ============================================================================
//  服务器 → 节点 → 接入令牌 → nodesim（真实 pdnd 客户端）→ activate → 发布套餐版本
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
const nodeId = str(
  await call(ADM, '/v1/nodes', {
    token: admin,
    idem: true,
    body: {
      name: 'smoke-node-1',
      server_id: serverId,
      pool_id: poolId,
      node_type: 'shadowsocks',
      server_host: 'edge.example.test',
      server_port: 8388,
      traffic_rate: 1,
      display_name: 'Smoke 01',
      protocol_config: { cipher: 'aes-256-gcm' },
    },
    expect: 201,
  }),
  'id',
)

step('服务器接入令牌')
const boot = await call(ADM, `/v1/servers/${serverId}/bootstrap-token`, { token: admin, idem: true, body: {}, expect: 201 })
const token = str(boot, 'token')
const installCommand = str(boot, 'install_command')
const panelMatch = /--panel\s+'([^']+)'/.exec(installCommand)
const caMatch = /--ca-sha256\s+'([^']+)'/.exec(installCommand)
if (!panelMatch || !caMatch) throw new Error(`install_command 解析不到 panel 或 ca-sha256：${installCommand.slice(0, 400)}`)
const panel = panelMatch[1]!
const ca = caMatch[1]!

step('拉起 nodesim（gRPC 接入，不起内核）')
const simDir = join(stateDir, 'nodesim')
mkdirSync(join(simDir, 'state'), { recursive: true })
mkdirSync(join(stateDir, 'logs'), { recursive: true })
const tokensFile = join(simDir, 'tokens')
writeFileSync(tokensFile, `s1 ${token}\n`, { mode: 0o600 })
const nodesimLog = join(stateDir, 'logs', 'nodesim.log')
const logFd = openSync(nodesimLog, 'a')
const sim = spawn(
  SMOKE_NODESIM,
  [
    '--panel',
    panel,
    '--ca-sha256',
    ca,
    '--tokens-file',
    tokensFile,
    '--state-dir',
    join(simDir, 'state'),
    '--status-file',
    join(simDir, 'status.json'),
    '--report-interval',
    '5s',
  ],
  { detached: true, stdio: ['ignore', logFd, logFd] },
)
sim.unref()
appendFileSync(join(stateDir, 'pids'), `${sim.pid}\n`)

step('等 nodesim 接入完成（enrolled + connected）')
await waitSim('nodesim 接入', (s) => {
  const srv = s.servers.s1
  return Boolean(srv?.enrolled && srv?.connected)
}, 60_000)

step('一步上线：POST activate')
const listed = ((await call(ADM, `/v1/nodes?id=${nodeId}&include_retired=1`, { token: admin })).nodes as Json[]).find((n) => n.id === nodeId)
if (!listed) throw new Error('节点列表里找不到刚建的节点')
const activated = await call(ADM, `/v1/nodes/${nodeId}/activate`, { token: admin, idem: true, body: { row_version: num(listed, 'row_version') } })
if (activated.status !== 'active' || activated.serving_status !== 'active') throw new Error(`上线后状态不对：${JSON.stringify(activated).slice(0, 300)}`)
if (activated.warnings !== undefined) throw new Error(`上线带了提示：${JSON.stringify(activated.warnings)}`)

step('发布套餐版本（绑定池里现在有可服务节点了）')
// 两个乐观锁都取自详情，与后台「版本」页的发布对话框同一个请求
const detail = (await call(ADM, `/v1/plans/${planId}`, { token: admin })).plan as Json
const version = (detail.versions as Json[]).find((v) => v.id === versionId)
if (!version) throw new Error(`套餐详情里没有刚建的版本：${JSON.stringify(detail).slice(0, 300)}`)
await call(ADM, `/v1/plans/${planId}/versions/${versionId}/publish`, {
  token: admin,
  idem: true,
  body: { expected_plan_row_version: num(detail, 'row_version'), expected_version_row_version: num(version, 'row_version') },
})

// ============================================================================
//  易支付渠道：后台接口建 epay，收银台指向回环上无人听的端口，只用来拼跳转地址；
//  allow_private_host 只在 env=development 放行
// ============================================================================

step('易支付渠道 smokepay')
await call(ADM, '/v1/payment-providers', {
  token: admin,
  idem: true,
  body: {
    code: PROVIDER,
    adapter: 'epay',
    display_name: '冒烟易支付',
    base_url: 'http://127.0.0.1:9',
    submit_path: '/submit.php',
    api_path: '/api.php',
    methods: ['alipay'],
    default_method: 'alipay',
    allow_private_host: true,
    min_amount: 100,
    merchant_id: EPAY_MERCHANT,
    key: EPAY_KEY,
  },
  expect: [200, 201],
})
await call(ADM, `/v1/payment-providers/${PROVIDER}/toggle`, { token: admin, body: { enabled: true, accepting_new: true } })

// ============================================================================
//  插件钩子：必须先于事件存在（Emit 与业务写入同一事务入队）。接收端是本机回环上的独立进程，
//  env=development 下面板放行回环地址——不改 Go、不放宽任何校验。
//  投递由 worker 的 plugin:dispatch 发出（建工单提交后入队，另每分钟巡检一次），这里不等
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
const inviteePassword = `Smoke-Invitee-${randomUUID()}`
const inviteeId = await register(inviteeEmail, inviteePassword, inviteCode)

// ============================================================================
//  订单：一张经易支付付清（开出订阅），一张留在待支付
// ============================================================================

step('门户下单')
const catalog = await call(PUB, '/v1/plans', { token: user })
const onSale = (catalog.plans as Json[] | undefined)?.find((p) => p.id === planId)
if (!onSale) throw new Error(`门户套餐目录里没有刚发布的套餐：${JSON.stringify(catalog).slice(0, 300)}`)
const priceId = str((onSale.prices as Json[])[0] ?? {}, 'id')
const order = await call(PUB, '/v1/orders', { token: user, idem: true, body: { plan_id: planId, price_id: priceId, use_balance: 0 }, expect: [200, 201] })
const orderId = str(order, 'order_id')

step('易支付付清第一张单')
const paid = await payOrder(user, orderId)
const subscriptionId = await subscriptionOf(user, planId)

step('第二张订单留在待支付（同套餐只续不新开：在刚开出的订阅上建一张续费单）')
const pendingOrderId = str(
  await call(PUB, `/v1/me/subscriptions/${subscriptionId}/renew`, { token: user, idem: true, body: { price_id: priceId, use_balance: 0 }, expect: [200, 201] }),
  'order_id',
)

step('被邀请人下单并付清：推荐人的佣金明细不空')
// 佣金只在被邀请人付款时记一笔；同 IP 会被标待复核、不会解冻，但明细照样有这一行
const invitee = await portalLogin(inviteeEmail, inviteePassword)
const inviteeOrder = await call(PUB, '/v1/orders', { token: invitee, idem: true, body: { plan_id: planId, price_id: priceId, use_balance: 0 }, expect: [200, 201] })
await payOrder(invitee, str(inviteeOrder, 'order_id'))
const inviteeSubscriptionId = await subscriptionOf(invitee, planId)

step('挂账：对已付清的订单再送一笔新的回调（新 trade_no），落进 excess_capture')
// 挂账只由支付回调产生（已付或已取消的订单又收到钱），没有后台新建接口
await epayNotify(paid.outTradeNo, paid.money)

// ============================================================================
//  节点上报：名单经会话下发，nodesim 每 5 秒为名单里的用户报一批流量与在线 IP，面板入账后回 ack；
//  流量排行、按日用量、在线设备都在入账时同步写入
// ============================================================================

step('等 nodesim 名单与流量、在线上报')
await waitSim(
  'nodesim 名单与上报',
  (s) => {
    const srv = s.servers.s1
    const node = srv?.nodes?.[nodeId]
    return Boolean(
      node && node.user_count >= 2 && (srv?.traffic_reports_acked ?? 0) >= 1 && (srv?.online_reports_sent ?? 0) >= 1,
    )
  },
  120_000,
)

// ============================================================================
//  路由：全局与单节点各一条规则（出站用内置的 block / direct），请求体与节点页「发布」同形
// ============================================================================

step('全局路由一条规则、单节点路由一条规则')
const globalRouting = await call(ADM, '/v1/nodes/routing', { token: admin })
await call(ADM, '/v1/nodes/routing', {
  method: 'PUT',
  token: admin,
  idem: true,
  body: {
    expected_revision: str(globalRouting, 'revision'),
    outbounds: [],
    routes: [{ priority: 10, matcher: { domain: ['ads.example.test'] }, outbound_tag: 'block', enabled: true, note: 'smoke' }],
  },
})
const nodeRouting = await call(ADM, `/v1/nodes/${nodeId}/routing`, { token: admin })
await call(ADM, `/v1/nodes/${nodeId}/routing`, {
  method: 'PUT',
  token: admin,
  body: {
    row_version: num(nodeRouting, 'row_version'),
    outbounds: [],
    routes: [{ priority: 10, matcher: { domain: ['cdn.example.test'] }, outbound_tag: 'direct', enabled: true, note: 'smoke' }],
  },
})

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

step('优惠券，并用它下一张单（兑换记录在下单时落定，不必付款；被邀请人已有这个套餐，用续费单）')
const couponId = str(
  await call(ADM, '/v1/coupons', { token: admin, body: { code: 'SMOKE10', discount_type: 'percent', discount_value: 1000, currency: 'CNY' }, expect: [200, 201] }),
  'id',
)
const couponOrderId = str(
  await call(PUB, `/v1/me/subscriptions/${inviteeSubscriptionId}/renew`, { token: invitee, idem: true, body: { price_id: priceId, coupon_code: 'SMOKE10', use_balance: 0 }, expect: [200, 201] }),
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
// 流量包加到一份在用的套餐上（购买模型统一 Q5：subscription_id 必填）
const packOrder = await call(PUB, '/v1/me/traffic-pack-orders', { token: user, idem: true, body: { pack_id: packId, subscription_id: subscriptionId, use_balance: 500, coupon_code: '' }, expect: [200, 201] })
// 应付为 0 的流量包单当场履约：响应里就该是 fulfilled，否则下面「我的流量包」必然为空
if (packOrder.status !== 'fulfilled') throw new Error(`流量包单没有当场履约：${JSON.stringify(packOrder).slice(0, 400)}`)

step('收入调整与手动流量重置')
await call(ADM, '/v1/revenue/adjustments', { token: admin, idem: true, body: { currency: 'CNY', amount: 1000, reason: 'smoke adjustment', effective_on: '' }, expect: [200, 201] })
// 手动重置按订阅行操作（购买模型统一：一个人可以有多份）
await call(ADM, `/v1/subscriptions/${subscriptionId}/traffic-reset`, { token: admin, idem: true, body: { note: 'smoke manual reset' }, expect: [200, 201] })

// ============================================================================
//  提现申请（SQL 夹具）。产品路径走不通冒烟：佣金只由 worker 每小时的 commission:settle 在冻结期满后解冻，
//  推荐人与买家同 IP 的佣金被标待复核；冒烟等不了。收款信息须封存：
//  [版本 1][key id 1][12 字节 nonce][密文 + 16 字节 tag]，HKDF-SHA256 空盐，AAD 为 withdrawal.payout:<用户 id>
// ============================================================================

step('提现申请（SQL 夹具）')
const sealKey = Buffer.from(hkdfSync('sha256', MASTER_KEY, Buffer.alloc(0), 'pandora/seal/v1', 32))
const sealNonce = randomBytes(12)
const sealCipher = createCipheriv('aes-256-gcm', sealKey, sealNonce)
sealCipher.setAAD(Buffer.from(`withdrawal.payout:${userId}`, 'utf8'))
const sealCt = Buffer.concat([sealCipher.update('支付宝 smoke@example.test', 'utf8'), sealCipher.final(), sealCipher.getAuthTag()])
const sealed = Buffer.concat([Buffer.from([1, 1]), sealNonce, sealCt]).toString('hex')
sqlFixture(
  '提现申请',
  `INSERT INTO withdrawals (user_id, currency, amount, payout_sealed, status) VALUES ('${userId}', 'CNY', 1.00, decode('${sealed}', 'hex'), 'requested');`,
)

// ============================================================================
//  收尾：确认各列表真的不空，再把 id 与门户账号交给 *.smoke.ts
// ============================================================================

/** 响应本身是数组，或指定字段 / 顶层第一个数组字段的长度 */
function listLength(o: unknown, key?: string): number {
  if (Array.isArray(o)) return o.length
  if (key) return Array.isArray((o as Json)[key]) ? ((o as Json)[key] as unknown[]).length : -1
  for (const v of Object.values(o as Json)) if (Array.isArray(v)) return v.length
  return -1
}

step('核对列表不空')
// 第四项：列表字段不是第一个数组时写明（路由响应里 outbounds 排在 routes 前面）
const lists: Array<[string, string, string, string?]> = [
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
  [ADM, '/v1/nodes/routing', admin, 'routes'],
  [ADM, `/v1/nodes/${nodeId}/routing`, admin, 'routes'],
  [PUB, '/v1/plans', user],
  [PUB, '/v1/orders', user],
  [PUB, '/v1/me/subscriptions', user],
  [PUB, `/v1/me/subscriptions/${subscriptionId}/nodes`, user],
  [PUB, '/v1/traffic-packs', user],
  [PUB, '/v1/me/traffic-packs', user],
  [PUB, '/v1/me/commission', user],
  [PUB, '/v1/support/tickets', user],
  [PUB, '/v1/me/announcements', user],
  [PUB, '/v1/content/pages', user],
]
const empty: string[] = []
for (const [base, path, token, key] of lists) {
  const n = listLength(await call(base, path, { token }), key)
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
      coupon_order_id: couponOrderId,
      subscription_id: subscriptionId,
      ticket_id: ticketId,
      announcement_id: announcementId,
      page_slug: pageSlug,
      gift_card_id: giftCardId,
      coupon_id: couponId,
      pack_id: packId,
      hook_code: hookCode,
      provider_code: PROVIDER,
    },
    null,
    2,
  ),
  { mode: 0o600 },
)
console.log(`==> 造数据完成：${join(stateDir, 'seed.json')}`)
