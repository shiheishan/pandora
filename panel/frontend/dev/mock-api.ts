/**
 * [INPUT]: 依赖 vite 的 Plugin 与 Connect 中间件类型，依赖 node:crypto 的 randomUUID，依赖 node:http 的请求响应，依赖 ./mock/types 的上下文契约与路由匹配，依赖 ./mock/admin 与 ./mock/portal 的模块登记表，依赖 ./mock/admin/security 的 admin.writes 状态与开关切换通知，依赖 ./mock/quick-login 的令牌表
 * [OUTPUT]: 对外提供 mockApi(app) 插件、MOCK_ACCOUNTS 演示账号
 * [POS]: panel/frontend 的开发期假后端外壳，只在 vite serve 且未设 PANDORA_API 时挂上，永不进产物：持有账号、会话、rat 与幂等表（与 Go 中间件一致：只重放 2xx，非 2xx 同 key 同请求重新执行，换请求 409），自己只答外壳接口（登录 / 退出 / me / reauth / 改密码 / SSE，门户再加注册、快捷登录消费、站点开关、外观）并守 admin.writes 只读门（与 middleware.AdminWritesGate 同一张豁免表，关闭时其余非 GET 回 503），其余按入口依次询问 mock/admin 或 mock/portal 的模块处理器
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomUUID } from 'node:crypto'
import type { IncomingMessage, ServerResponse } from 'node:http'
import type { Plugin } from 'vite'
import { ADMIN_MODULES } from './mock/admin/index.ts'
import { adminWritesEnabled, onSwitchChanged } from './mock/admin/security.ts'
import { PORTAL_MODULES } from './mock/portal/index.ts'
import { consumeQuickLogin } from './mock/quick-login.ts'
import { findRoute, type AnonContext, type Json, type MockApp, type MockContext, type MockRaw, type MockResult, type MockUser } from './mock/types.ts'

// ---------------------------------------------------------------------------
// 形状以 panel/docs/redesign/api-contract.md 为准。这里只放外壳接口与共用设施：
// 会话与 rat、reauth 窗口、幂等表、错误信封；模块接口写在 mock/<入口>/<模块>.ts，
// 由各页面的会话各自维护。先外壳、后模块，未匹配的 v1/ 一律 404 信封。
// POST /__mock/expire-reauth 让所有会话的 rat 立即过期，不必干等 15 分钟。
// ---------------------------------------------------------------------------
export const MOCK_ACCOUNTS = {
  admin: { email: 'admin@pandora.dev', password: 'pandora-dev-pass' },
  /** 只读管理员：用来实测侧栏、⌘K 与页头标签按权限隐藏 */
  viewer: { email: 'viewer@pandora.dev', password: 'pandora-dev-pass' },
  portal: { email: 'user@pandora.dev', password: 'pandora-dev-pass' },
} as const

const REAUTH_WINDOW_MS = 15 * 60 * 1000
const TTL_SECONDS = 2592000

interface Session {
  userId: string
  rat: number
  /** 建会话的时刻与 User-Agent：门户「账号安全」列其它会话时用 */
  created: number
  userAgent: string
}

/**
 * 幂等表的一格，仿 middleware/idempotency.go（契约 R85）：in_flight 在途；succeeded 只收 2xx，原样重放；
 * failed 收一切非 2xx（含 5xx 与处理器抛错），同 key 同请求再来时重新执行。指纹一旦记下就不变，
 * 无论哪种状态，同 key 换请求都回 409 idempotency_key_reuse。「已绑定业务资源的 failed 回 409」
 * 这一支假后端不模拟（模块处理器没有资源绑定的概念）
 */
interface Replay {
  fingerprint: string
  state: 'in_flight' | 'succeeded' | 'failed'
  result: MockResult | null
}

/** middleware.adminWriteExempt：只读模式下仍放行 GET 类、POST v1/switches/*、v1/auth/*、v1/me/password */
function adminWriteExempt(method: string, path: string): boolean {
  if (method === 'GET' || method === 'HEAD' || method === 'OPTIONS') return true
  return (method === 'POST' && path.startsWith('/v1/switches/')) || path.startsWith('/v1/auth/') || path === '/v1/me/password'
}

function send(res: ServerResponse, status: number, body?: unknown) {
  res.statusCode = status
  if (body === undefined) {
    res.end()
    return
  }
  res.setHeader('Content-Type', 'application/json; charset=utf-8')
  res.end(JSON.stringify(body))
}

function sendRaw(res: ServerResponse, status: number, raw: MockRaw, replay = false) {
  res.statusCode = status
  res.setHeader('Content-Type', raw.contentType)
  res.setHeader('Cache-Control', 'no-store')
  if (!replay) for (const [name, value] of Object.entries(raw.headers ?? {})) res.setHeader(name, value)
  res.end(raw.text)
}

function fail(res: ServerResponse, status: number, code: string, message: string, fields?: Record<string, string>) {
  send(res, status, { error: { code, message, ...(fields ? { fields } : {}), request_id: randomUUID().slice(0, 8) } })
}

async function readText(req: IncomingMessage): Promise<string> {
  const chunks: Buffer[] = []
  for await (const chunk of req) chunks.push(chunk as Buffer)
  return Buffer.concat(chunks).toString('utf8')
}

function parseObject(text: string): Json | null {
  if (text === '') return {}
  try {
    const value: unknown = JSON.parse(text)
    return value && typeof value === 'object' && !Array.isArray(value) ? (value as Json) : null
  } catch {
    return null
  }
}

const str = (v: unknown) => (typeof v === 'string' ? v : '')

function initialUsers(app: MockApp): MockUser[] {
  if (app === 'portal') return [{ ...MOCK_ACCOUNTS.portal, userId: randomUUID(), displayName: '张伟', permissions: [], roles: [] }]
  return [
    { ...MOCK_ACCOUNTS.admin, userId: randomUUID(), displayName: '林舟', permissions: ADMIN_PERMISSIONS, roles: [{ code: 'ops', name: '运维' }] },
    { ...MOCK_ACCOUNTS.viewer, userId: randomUUID(), displayName: '只读', permissions: VIEWER_PERMISSIONS, roles: [{ code: 'viewer', name: '只读' }] },
  ]
}

export function mockApi(app: MockApp): Plugin {
  const users = new Map<string, MockUser>(initialUsers(app).map((u) => [u.email, u]))
  const sessions = new Map<string, Session>()
  const registrations = new Map<string, { email: string; code: string }>()
  const replays = new Map<string, Replay>()
  const modules = app === 'admin' ? ADMIN_MODULES : PORTAL_MODULES

  const issue = (userId: string, req: IncomingMessage, rat = Date.now()) => {
    const token = `mock-${randomUUID()}`
    sessions.set(token, { userId, rat, created: Date.now(), userAgent: String(req.headers['user-agent'] ?? '') })
    return token
  }
  const bearer = (req: IncomingMessage) => {
    const header = req.headers.authorization ?? ''
    const token = header.startsWith('Bearer ') ? header.slice(7) : ''
    const session = sessions.get(token)
    return session ? { token, session } : null
  }
  const userById = (id: string) => [...users.values()].find((u) => u.userId === id)

  async function handle(req: IncomingMessage, res: ServerResponse, url: URL) {
    const method = req.method ?? 'GET'
    const path = url.pathname
    const route = `${method} ${path}`
    let raw: Promise<string> | null = null
    const text = () => (raw ??= readText(req))
    const base: Omit<AnonContext, 'params'> = {
      app,
      req,
      res,
      method,
      path,
      query: url.searchParams,
      body: async () => parseObject(await text()),
      send: (status, body) => send(res, status, body),
      sendRaw: (status, raw) => sendRaw(res, status, raw),
      fail: (status, code, message, fields) => fail(res, status, code, message, fields),
    }

    // --- 匿名接口：外壳 ------------------------------------------------------
    if (route === 'POST /v1/auth/login') {
      const body = await base.body()
      if (!body) return fail(res, 400, 'bad_request', '请求体不是合法的 JSON')
      const user = users.get(str(body.email).trim().toLowerCase())
      if (!user || user.password !== str(body.password)) return fail(res, 401, 'unauthorized', '邮箱或密码不正确')
      const token = issue(user.userId, req)
      return app === 'admin'
        ? send(res, 200, { access_token: token, token_type: 'Bearer', expires_in: TTL_SECONDS, user_id: user.userId, permissions: user.permissions })
        : send(res, 200, { access_token: token, refresh_token: randomUUID(), token_type: 'Bearer', expires_in: TTL_SECONDS, user_id: user.userId })
    }
    if (app === 'portal' && route === 'GET /v1/site-config') {
      return send(res, 200, { registration_mode: 'invite_only', email_verification: true })
    }
    if (app === 'portal' && route === 'GET /v1/appearance') {
      return send(res, 200, {
        theme: { id: randomUUID(), code: 'paper-white', name: '默认 · 纸白', is_builtin: true, is_active: true, tokens: { light: {}, dark: {} }, branding: { site_name: 'Pandora' }, custom_css: '' },
        slots: { 'portal.login.notice': '<p>国庆活动：全场 8 折，优惠码 AUTUMN26</p>' },
      })
    }
    if (app === 'portal' && route === 'POST /v1/auth/register/start') {
      const body = await base.body()
      if (!body) return fail(res, 400, 'bad_request', '请求体不是合法的 JSON')
      const email = str(body.email).trim().toLowerCase()
      if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email)) return fail(res, 422, 'validation_failed', '请求参数校验未通过', { email: '邮箱格式不正确' })
      if (str(body.invite_code).toUpperCase() !== 'PANDORA') return fail(res, 403, 'forbidden', '注册当前不可用或邀请码无效')
      const token = randomUUID()
      registrations.set(token, { email, code: '123456' })
      return send(res, 200, {
        registration_token: token,
        verification_required: true,
        expires_at: new Date(Date.now() + 600_000).toISOString(),
        message: '验证码已发送',
        dev_code: '123456',
      })
    }
    if (app === 'portal' && route === 'POST /v1/auth/register/complete') {
      const body = await base.body()
      if (!body) return fail(res, 400, 'bad_request', '请求体不是合法的 JSON')
      const reg = registrations.get(str(body.registration_token))
      if (!reg || reg.code !== str(body.code)) return fail(res, 403, 'forbidden', '注册当前不可用或邀请码无效')
      const password = str(body.password)
      if (password.length < 8) return fail(res, 422, 'validation_failed', '请求参数校验未通过', { password: '密码至少需要 8 个字符' })
      if (!/[a-z]/i.test(password) || !/\d/.test(password)) return fail(res, 422, 'validation_failed', '请求参数校验未通过', { password: '密码必须同时包含字母和数字' })
      registrations.delete(str(body.registration_token))
      const user: MockUser = { email: reg.email, password, userId: randomUUID(), displayName: null, permissions: [], roles: [] }
      users.set(reg.email, user)
      return send(res, 201, { user_id: user.userId, email: user.email })
    }
    if (app === 'portal' && route === 'POST /v1/auth/quick-login') {
      const body = await base.body()
      const userId = consumeQuickLogin(str(body?.token))
      if (!userId) return fail(res, 401, 'unauthorized', '快捷登录链接无效或已过期')
      return send(res, 200, { access_token: issue(userId, req), refresh_token: randomUUID(), expires_in: TTL_SECONDS })
    }
    if (route === 'POST /__mock/expire-reauth') {
      sessions.forEach((s) => (s.rat = 0))
      return send(res, 204)
    }

    // --- admin.writes 只读门：Go 挂在整棵 /v1 上、先于认证 ----------------------
    if (app === 'admin' && !adminWriteExempt(method, path) && !adminWritesEnabled()) return fail(res, 503, 'service_unavailable', '管理端只读模式')

    // --- 匿名接口：模块 ------------------------------------------------------
    const anon = findRoute(
      modules.map((m) => m.anonymous),
      method,
      path,
    )
    if (anon) return anon.handler({ ...base, params: anon.params })

    // --- 以下需登录 ----------------------------------------------------------
    const auth = bearer(req)
    const user = auth ? userById(auth.session.userId) : undefined
    if (!auth || !user) return fail(res, 401, 'unauthorized', '需要登录')
    const reauthed = Date.now() - auth.session.rat < REAUTH_WINDOW_MS

    if (route === 'POST /v1/auth/logout') {
      sessions.delete(auth.token)
      return send(res, 204)
    }
    if (route === 'GET /v1/events') return stream(req, res)

    if (app === 'admin') {
      if (route === 'GET /v1/me') {
        return send(res, 200, {
          user_id: user.userId,
          kind: 'user',
          permissions: user.permissions,
          reauthed,
          email: user.email,
          display_name: user.displayName,
          roles: user.roles,
        })
      }
      if (route === 'POST /v1/auth/reauth') {
        const body = await base.body()
        if (!body) return fail(res, 400, 'bad_request', '请求体不是合法的 JSON')
        if (str(body.password) === '') return fail(res, 422, 'validation_failed', '请求参数校验未通过', { password: '请输入当前密码' })
        if (str(body.password) !== user.password) return fail(res, 401, 'unauthorized', '密码不正确')
        // 与后端一致：同一会话换发一枚 rat 刷新的令牌，旧令牌照样有效到会话结束
        return send(res, 200, { access_token: issue(user.userId, req), token_type: 'Bearer', expires_in: TTL_SECONDS })
      }
      if (route === 'POST /v1/me/password') {
        const body = await base.body()
        if (!body) return fail(res, 400, 'bad_request', '请求体不是合法的 JSON')
        const oldPw = str(body.old_password)
        const newPw = str(body.new_password)
        if (!oldPw || !newPw) return fail(res, 422, 'validation_failed', '请求参数校验未通过', { old_password: '当前密码必填', new_password: '新密码必填' })
        if (oldPw !== user.password) return fail(res, 401, 'unauthorized', '当前密码不正确')
        if (newPw === oldPw) return fail(res, 400, 'bad_request', '新密码不能与当前密码相同')
        if (newPw.length < 12) return fail(res, 422, 'validation_failed', '请求参数校验未通过', { password: '密码至少需要 12 个字符' })
        user.password = newPw
        for (const [token, s] of sessions) if (s.userId === user.userId) sessions.delete(token)
        return send(res, 200, { ok: true, reauthenticate: true })
      }
    } else if (route === 'GET /v1/me') {
      return send(res, 200, {
        user_id: user.userId,
        email: user.email,
        display_name: user.displayName,
        status: 'active',
        created_at: '2026-05-04T11:40:00Z',
        permissions: null,
      })
    }

    // --- 需登录接口：模块 ----------------------------------------------------
    const found = findRoute(
      modules.map((m) => m.routes),
      method,
      path,
    )
    if (!found) return fail(res, 404, 'not_found', '资源不存在或无权访问')
    const ctx: MockContext = {
      ...base,
      params: found.params,
      user,
      reauthed,
      otherSessions: () =>
        [...sessions]
          .filter(([token, s]) => s.userId === user.userId && token !== auth.token)
          .map(([token, s]) => ({ token, created: s.created, userAgent: s.userAgent })),
      revokeSession: (token) => sessions.get(token)?.userId === user.userId && sessions.delete(token),
      requirePermission: (code) => {
        if (user.permissions.includes(code)) return true
        fail(res, 404, 'not_found', '资源不存在或无权访问')
        return false
      },
      requireReauth: () => {
        if (reauthed) return true
        fail(res, 403, 'reauth_required', '此操作需要重新验证身份')
        return false
      },
      idempotent: async (scope, run) => {
        const key = req.headers['idempotency-key']
        if (typeof key !== 'string' || !/^[\x21-\x7e]{1,255}$/.test(key)) return fail(res, 400, 'bad_request', '缺少或非法的 Idempotency-Key')
        const slot = `${user.userId}\n${scope}\n${key}`
        const fingerprint = `${method} ${url.pathname}${url.search}\n${await text()}`
        const seen = replays.get(slot)
        if (seen) {
          if (seen.fingerprint !== fingerprint) return fail(res, 409, 'idempotency_key_reuse', '幂等键已用于另一个请求')
          if (seen.state === 'in_flight') return fail(res, 409, 'conflict', '同一请求正在处理')
          if (seen.state === 'succeeded' && seen.result) return seen.result.raw ? sendRaw(res, seen.result.status, seen.result.raw, true) : send(res, seen.result.status, seen.result.body)
          // failed：接管这一格，重新执行
        }
        replays.set(slot, { fingerprint, state: 'in_flight', result: null })
        try {
          const result = await run()
          const ok = result.status >= 200 && result.status < 300
          replays.set(slot, ok ? { fingerprint, state: 'succeeded', result } : { fingerprint, state: 'failed', result: null })
          if (result.raw) sendRaw(res, result.status, result.raw)
          else send(res, result.status, result.body)
        } catch (err) {
          replays.set(slot, { fingerprint, state: 'failed', result: null })
          throw err
        }
      },
    }
    return found.handler(ctx)
  }

  // 与后端同帧格式：首帧 retry，事件 id / event / data，25 秒注释心跳；这里每 15 秒随机推一条表变更，
  // 后台另在降级开关切换后立即推 switches.changed
  function stream(req: IncomingMessage, res: ServerResponse) {
    res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store', Connection: 'keep-alive' })
    res.write('retry: 5000\n\n')
    const topics = app === 'admin' ? ADMIN_TOPICS : PORTAL_TOPICS
    let seq = 0
    const unsubscribe = app === 'admin' ? onSwitchChanged((payload) => res.write(`id: ${++seq}\nevent: switches.changed\ndata: ${JSON.stringify(payload)}\n\n`)) : () => undefined
    const tick = setInterval(() => {
      const [topic, table] = topics[Math.floor(Math.random() * topics.length)]!
      const op = ['INSERT', 'UPDATE', 'UPDATE', 'DELETE'][Math.floor(Math.random() * 4)]
      res.write(`id: ${++seq}\nevent: ${topic}\ndata: ${JSON.stringify({ table, op, id: randomUUID() })}\n\n`)
    }, 15_000)
    const ping = setInterval(() => res.write(': ping\n\n'), 25_000)
    req.on('close', () => {
      unsubscribe()
      clearInterval(tick)
      clearInterval(ping)
    })
  }

  return {
    name: 'pandora-mock-api',
    apply: 'serve',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const url = new URL(req.url ?? '/', 'http://mock.local')
        if (!url.pathname.startsWith('/v1/') && !url.pathname.startsWith('/__mock/')) return next()
        handle(req, res, url).catch((err: unknown) => {
          server.config.logger.error(`[mock-api] ${String(err)}`)
          if (!res.headersSent) fail(res, 500, 'internal_error', '服务暂时不可用，请稍后重试')
        })
      })
      const accounts = app === 'admin' ? [MOCK_ACCOUNTS.admin, MOCK_ACCOUNTS.viewer] : [MOCK_ACCOUNTS.portal]
      server.config.logger.info(`  mock api (${app}): ${accounts.map((a) => a.email).join(' / ')}，口令 ${MOCK_ACCOUNTS.admin.password}`)
    },
  }
}

// ---------------------------------------------------------------------------
// 管理员拿契约第 3 节出现过的全部权限码；只读账号只有几项读权限，
// 看得到仪表盘、工单、用户（除流量重置）、套餐、订单、节点、邮件模板，
// 看不到营销、内容与外观、安全与运维，以及各模块里缺权限的标签。
// ---------------------------------------------------------------------------
const ADMIN_PERMISSIONS: readonly string[] = [
  'billing.adjustment.write',
  'billing.ledger.read',
  'billing.order.read',
  'billing.order.write',
  'billing.payment.read',
  'billing.provider.write',
  'catalog.publish',
  'catalog.read',
  'catalog.write',
  'iam.user.read',
  'iam.user.write',
  'marketing.commission.read',
  'marketing.commission.write',
  'marketing.coupon.read',
  'marketing.coupon.write',
  'marketing.giftcard.read',
  'marketing.giftcard.write',
  'marketing.withdrawal.approve',
  'metering.read',
  'metering.reset.read',
  'metering.reset.write',
  'node.config.publish',
  'node.identity.revoke',
  'node.lifecycle',
  'node.provision',
  'node.read',
  'node.write',
  'ops.announcement.write',
  'ops.content.write',
  'ops.dashboard.read',
  'ops.export',
  'ops.notification.read',
  'ops.notification.write',
  'ops.ticket.read',
  'ops.ticket.write',
  'platform.appearance.read',
  'platform.appearance.write',
  'platform.plugin.read',
  'platform.plugin.write',
  'platform.settings.write',
  'security.audit.read',
  'security.risk.review',
]

const VIEWER_PERMISSIONS: readonly string[] = ['ops.ticket.read', 'iam.user.read', 'catalog.read', 'billing.order.read', 'node.read', 'ops.notification.read']

const ADMIN_TOPICS: ReadonlyArray<readonly [string, string]> = [
  ['orders.changed', 'orders'],
  ['tickets.changed', 'tickets'],
  ['nodes.changed', 'nodes'],
  ['subscriptions.changed', 'subscriptions'],
  ['plans.changed', 'prices'],
]

const PORTAL_TOPICS: ReadonlyArray<readonly [string, string]> = [
  ['orders.changed', 'orders'],
  ['announcements.changed', 'announcements'],
]
