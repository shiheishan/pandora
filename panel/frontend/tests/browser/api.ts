import { spawnSync } from 'node:child_process'
import { mkdirSync, rmSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { createHash, randomUUID } from 'node:crypto'
import { ADM, ADMIN_EMAIL, ADMIN_PASSWORD, OUT, PG_CONTAINER, PG_DB, PUB } from './env.ts'

// ============================================================================
//  测试自己的接口调用：只用来「造人、造前提」（注册、后台调余额、赠送开一份、发礼品卡），
//  以及扮演支付渠道送签名回调。要验证的路径一律在浏览器里点，不在这里调。
// ============================================================================

export type Json = Record<string, unknown>

// ----------------------------------------------------------------------------
//  来源地址：冒烟栈没有 nginx，网关直接信 X-Real-IP（.claude/rules/panel-e2e.md）。
//  浏览器按机器速度点页面，门户每 IP 每分钟 120 次、后台 240 次的限流会把同一个地址打满，
//  所以每个用户、每一步各用一个虚构地址（198.18.0.0/15 基准测试网段）。相邻两个地址落在不同的 /24，
//  按网段的那一维（每 /24 每分钟 960 次）也不会被同一个 worker 打满
// ----------------------------------------------------------------------------
let ipSeq = 0
const worker = Number(process.env.TEST_PARALLEL_INDEX ?? '0')
export function freshIp(): string {
  ipSeq += 1
  const n = worker * 4099 + ipSeq
  return `198.${18 + (Math.floor(n / (256 * 254)) % 2)}.${n % 256}.${(Math.floor(n / 256) % 254) + 1}`
}

interface Call {
  method?: string
  token?: string
  body?: unknown
  expect?: number | number[]
  ip?: string
}

// 后台接口之间留 300ms，与 tests/smoke/seed.ts 同一节奏（每个 worker 一个后台调用者）
let lastAdminCall = 0
async function pace(base: string): Promise<void> {
  if (base !== ADM) return
  const wait = lastAdminCall + 300 - Date.now()
  if (wait > 0) await new Promise((r) => setTimeout(r, wait))
  lastAdminCall = Date.now()
}

export class HttpError extends Error {
  constructor(
    readonly status: number,
    readonly body: Json,
    message: string,
  ) {
    super(message)
  }
}

export async function call(base: string, path: string, c: Call = {}): Promise<Json> {
  await pace(base)
  const method = c.method ?? (c.body === undefined ? 'GET' : 'POST')
  const headers: Record<string, string> = { 'X-Real-IP': c.ip ?? freshIp() }
  if (c.body !== undefined) headers['Content-Type'] = 'application/json'
  if (c.token) headers.Authorization = `Bearer ${c.token}`
  // 写接口一律带一把新的幂等键（没挂幂等中间件的接口忽略它）
  if (method !== 'GET' && c.token) headers['Idempotency-Key'] = randomUUID()
  const res = await fetch(base + path, { method, headers, ...(c.body === undefined ? {} : { body: JSON.stringify(c.body) }) })
  const text = await res.text()
  const json = parseJson(text)
  const expect = Array.isArray(c.expect) ? c.expect : [c.expect ?? 200]
  if (!expect.includes(res.status)) throw new HttpError(res.status, json, `${method} ${path} 返回 ${res.status}（期望 ${expect.join('/')}）：${text.slice(0, 400)}`)
  return json
}

function parseJson(text: string): Json {
  try {
    return text ? (JSON.parse(text) as Json) : {}
  } catch {
    return { raw: text.slice(0, 300) }
  }
}

export function str(o: unknown, path: string): string {
  let v: unknown = o
  for (const k of path.split('.')) v = (v as Json | undefined)?.[k]
  if (typeof v !== 'string' || v === '') throw new Error(`响应缺少字符串字段 ${path}：${JSON.stringify(o).slice(0, 300)}`)
  return v
}

// ----------------------------------------------------------------------------
//  写审计的请求跨 worker 排队（产品问题，见任务报告）：审计链序号在 SERIALIZABLE 事务里按事务快照取链尾，
//  快照早于审计锁，两笔并发的写有一笔会撞 audit_events_chain_seq_key 回 500。浏览器测试并行跑时
//  会随机撞上，所以造前提的后台写、收银台回调、页面上会建单或改账的那一下点击都经这把锁串行。
//  锁是状态目录里的一个目录（mkdir 原子），30 秒没释放视为上一个进程崩了
// ----------------------------------------------------------------------------
const LOCK = join(OUT, '.audit-write.lock')
let held = 0

export async function exclusive<T>(fn: () => Promise<T>): Promise<T> {
  if (held > 0) return fn()
  for (;;) {
    try {
      mkdirSync(LOCK)
      break
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code !== 'EEXIST') throw e
      try {
        if (Date.now() - statSync(LOCK).mtimeMs > 30_000) rmSync(LOCK, { recursive: true, force: true })
      } catch {
        // 别的进程刚好释放了
      }
      await new Promise((r) => setTimeout(r, 50))
    }
  }
  held += 1
  try {
    return await fn()
  } finally {
    held -= 1
    rmSync(LOCK, { recursive: true, force: true })
  }
}

// ----------------------------------------------------------------------------
//  后台：每个 worker 登录一次。登录签发的令牌自带 15 分钟重认证窗口；
//  窗口过了（403 reauth_required）就用口令重认证一次再试
// ----------------------------------------------------------------------------
let adminToken: string | null = null

async function adminLogin(): Promise<string> {
  adminToken = str(await call(ADM, '/v1/auth/login', { body: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD } }), 'access_token')
  return adminToken
}

export async function admin(path: string, c: Omit<Call, 'token'> = {}): Promise<Json> {
  const write = (c.method ?? (c.body === undefined ? 'GET' : 'POST')) !== 'GET'
  return write ? exclusive(() => adminCall(path, c)) : adminCall(path, c)
}

async function adminCall(path: string, c: Omit<Call, 'token'>): Promise<Json> {
  const token = adminToken ?? (await adminLogin())
  try {
    return await call(ADM, path, { ...c, token })
  } catch (e) {
    if (!(e instanceof HttpError)) throw e
    if (e.status === 401) return call(ADM, path, { ...c, token: await adminLogin() })
    if (e.status === 403 && (e.body.error as Json | undefined)?.code === 'reauth_required') {
      adminToken = str(await call(ADM, '/v1/auth/reauth', { token, body: { password: ADMIN_PASSWORD } }), 'access_token')
      return call(ADM, path, { ...c, token: adminToken })
    }
    throw e
  }
}

// ----------------------------------------------------------------------------
//  门户用户：经真实注册接口现场生成（新库默认不开邮箱验证；开了就用 dev_code）。
//  口令只在本进程内存与浏览器表单里出现，不落盘
// ----------------------------------------------------------------------------
export interface User {
  email: string
  password: string
  id: string
}

let userSeq = 0
const runTag = randomUUID().slice(0, 8)

export async function newUser(tag: string): Promise<User> {
  userSeq += 1
  const email = `w9b-${tag.toLowerCase()}-${runTag}-${worker}-${userSeq}@example.test`
  const password = `W9b-${randomUUID()}`
  const ip = freshIp()
  const start = await call(PUB, '/v1/auth/register/start', { body: { email }, ip })
  const code = typeof start.dev_code === 'string' ? start.dev_code : ''
  const done = await call(PUB, '/v1/auth/register/complete', { body: { registration_token: str(start, 'registration_token'), code, password }, expect: [200, 201], ip })
  return { email, password, id: str(done, 'user_id') }
}

/** 门户接口令牌：只用于读回核对（例如取订单 id 做 SQL 夹具），不替页面做动作 */
export async function portalToken(u: User): Promise<string> {
  return str(await call(PUB, '/v1/auth/login', { body: { email: u.email, password: u.password } }), 'access_token')
}

export async function portalGet(token: string, path: string): Promise<Json> {
  return call(PUB, path, { token })
}

// ----------------------------------------------------------------------------
//  后台造前提：调余额、赠送开一份、加流量包
// ----------------------------------------------------------------------------
export async function setBalance(u: User, cents: number, reason: string): Promise<void> {
  await admin(`/v1/users/${u.id}/balance`, { body: { amount: cents, currency: 'CNY', reason: `w9browser ${reason}` } })
}

export async function grant(u: User, plan: { id: string; prices: string[] }, target: Json = { kind: 'new' }, price = 0): Promise<Json> {
  return admin('/v1/orders/manual', {
    body: { user_id: u.id, plan_id: plan.id, price_id: plan.prices[price], reason: 'w9browser 造前提：赠送开一份', settlement: 'grant', target },
    expect: [200, 201],
  })
}

export async function subscriptionsOf(u: User): Promise<Json[]> {
  const token = await portalToken(u)
  return ((await portalGet(token, '/v1/me/subscriptions')).subscriptions as Json[] | undefined) ?? []
}

// ----------------------------------------------------------------------------
//  演示收银台：按易支付规则签名（非空参数按名升序拼 k=v&，末尾接密钥，MD5 小写），
//  与 panel/tests/epay_e2e.sh 同一口径，打真实的 /v1/webhooks/payments/{code}
// ----------------------------------------------------------------------------
export function epaySign(params: Record<string, string>, key: string): string {
  const kv = Object.entries(params).filter(([k, v]) => v && k !== 'sign' && k !== 'sign_type')
  kv.sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
  return createHash('md5')
    .update(kv.map(([k, v]) => `${k}=${v}`).join('&') + key)
    .digest('hex')
}

export async function epayNotify(code: string, merchant: string, key: string, outTradeNo: string, money: string): Promise<string> {
  const params: Record<string, string> = {
    pid: merchant,
    trade_no: `W9BT${randomUUID().replace(/-/g, '').slice(0, 16)}`,
    out_trade_no: outTradeNo,
    type: 'alipay',
    name: 'w9browser',
    money,
    trade_status: 'TRADE_SUCCESS',
  }
  params.sign = epaySign(params, key)
  params.sign_type = 'MD5'
  return exclusive(async () => {
    const res = await fetch(`${PUB}/v1/webhooks/payments/${code}?${new URLSearchParams(params)}`, { headers: { 'X-Real-IP': freshIp() } })
    return (await res.text()).trim()
  })
}

// ----------------------------------------------------------------------------
//  SQL 夹具：以迁移账号直连冒烟库。只用于产品接口造不出、或要等时间流逝的数据，
//  每处调用都写明为什么（与 tests/smoke/seed.ts 的 sqlFixture 同一做法）
// ----------------------------------------------------------------------------
export function sql(what: string, statement: string): string {
  const r = spawnSync('docker', ['exec', '-i', PG_CONTAINER, 'psql', '-X', '-q', '-At', '-v', 'ON_ERROR_STOP=1', '-U', 'aegis', '-d', PG_DB], { input: statement, encoding: 'utf8' })
  if (r.status !== 0) throw new Error(`SQL 夹具「${what}」失败：${r.stderr || r.error?.message}`)
  return r.stdout.trim()
}

/** 只认 uuid 形状再拼进 SQL：夹具里的 id 都来自接口响应 */
export function uuid(v: string): string {
  if (!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(v)) throw new Error(`不是 uuid：${v}`)
  return v
}

/**
 * 把一张待支付单的付款期限拨到一分钟前（订单仍是待支付）。orders 的护栏触发器不许改 expires_at，
 * 这里在一次性库上用 session_replication_role = replica 跳过触发器，与 risk_e2e.sh 挪 fetched_at 同一做法；
 * 只改这一列、不动预留：释放任务按预留的到期找单，不会在测试中途把它关掉
 */
export function expireOrderSql(orderId: string): string {
  return `BEGIN;
SET LOCAL session_replication_role = replica;
UPDATE orders SET expires_at = now() - interval '1 minute' WHERE id = '${uuid(orderId)}' AND status = 'pending_payment';
COMMIT;`
}
