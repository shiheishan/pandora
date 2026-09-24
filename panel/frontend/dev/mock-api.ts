/**
 * [INPUT]: 依赖 vite 的 Plugin 与 Connect 中间件类型，依赖 node:crypto 的 randomUUID，依赖 node:http 的请求响应
 * [OUTPUT]: 对外提供 mockApi(app) 插件、MOCK_ACCOUNTS 演示账号
 * [POS]: panel/frontend 的开发期假后端，只在 vite serve 且未设 PANDORA_API 时挂上，永不进产物；按 api-contract.md 的外壳接口返回同形状数据，让没有 PostgreSQL 的本机也能在浏览器里走登录、退出、reauth 重放与 SSE
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomUUID } from 'node:crypto'
import type { IncomingMessage, ServerResponse } from 'node:http'
import type { Plugin } from 'vite'

// ---------------------------------------------------------------------------
// 只模拟外壳用到的接口，形状以 panel/docs/redesign/api-contract.md 为准：
// 登录 / 退出 / me / reauth / 改密码 / SSE，门户再加注册、快捷登录、站点开关、
// 外观与顶栏的余额、订阅、佣金、未读数。其余 v1/ 一律 404 信封。
// 另有一条挂了 reauth + 幂等的真实路由 POST v1/users/{id}/balance，用来在浏览器里
// 验证「弹框 → 换令牌 → 原键重放」；POST /__mock/expire-reauth 让当前会话的
// rat 立即过期，不必干等 15 分钟。
// ---------------------------------------------------------------------------
export const MOCK_ACCOUNTS = {
  admin: { email: 'admin@pandora.dev', password: 'pandora-dev-pass' },
  portal: { email: 'user@pandora.dev', password: 'pandora-dev-pass' },
} as const

const REAUTH_WINDOW_MS = 15 * 60 * 1000
const TTL_SECONDS = 2592000

interface Session {
  userId: string
  rat: number
}

type Json = Record<string, unknown>

function send(res: ServerResponse, status: number, body?: unknown) {
  if (body === undefined) {
    res.statusCode = status
    res.end()
    return
  }
  res.statusCode = status
  res.setHeader('Content-Type', 'application/json; charset=utf-8')
  res.end(JSON.stringify(body))
}

function fail(res: ServerResponse, status: number, code: string, message: string, fields?: Record<string, string>) {
  send(res, status, { error: { code, message, ...(fields ? { fields } : {}), request_id: randomUUID().slice(0, 8) } })
}

async function readJson(req: IncomingMessage): Promise<Json | null> {
  const chunks: Buffer[] = []
  for await (const chunk of req) chunks.push(chunk as Buffer)
  const text = Buffer.concat(chunks).toString('utf8')
  if (text === '') return {}
  try {
    const value: unknown = JSON.parse(text)
    return value && typeof value === 'object' && !Array.isArray(value) ? (value as Json) : null
  } catch {
    return null
  }
}

const str = (v: unknown) => (typeof v === 'string' ? v : '')

export function mockApi(app: 'admin' | 'portal'): Plugin {
  interface User {
    email: string
    password: string
    userId: string
  }
  const account: User = { ...MOCK_ACCOUNTS[app], userId: randomUUID() }
  const users = new Map<string, User>([[account.email, account]])
  const sessions = new Map<string, Session>()
  const registrations = new Map<string, { email: string; code: string }>()
  const quickTokens = new Map<string, { userId: string; expires: number }>()
  const replays = new Map<string, { status: number; body: unknown }>()
  let balanceCents = 265000

  const issue = (userId: string, rat = Date.now()) => {
    const token = `mock-${randomUUID()}`
    sessions.set(token, { userId, rat })
    return token
  }
  const bearer = (req: IncomingMessage) => {
    const header = req.headers.authorization ?? ''
    const token = header.startsWith('Bearer ') ? header.slice(7) : ''
    const session = sessions.get(token)
    return session ? { token, session } : null
  }
  const userById = (id: string) => [...users.values()].find((u) => u.userId === id)

  async function handle(req: IncomingMessage, res: ServerResponse, path: string) {
    const method = req.method ?? 'GET'
    const route = `${method} ${path}`

    // --- 匿名接口 ------------------------------------------------------------
    if (route === 'POST /v1/auth/login') {
      const body = await readJson(req)
      if (!body) return fail(res, 400, 'bad_request', '请求体不是合法的 JSON')
      const user = users.get(str(body.email).trim().toLowerCase())
      if (!user || user.password !== str(body.password)) return fail(res, 401, 'unauthorized', '邮箱或密码不正确')
      const token = issue(user.userId)
      return app === 'admin'
        ? send(res, 200, { access_token: token, token_type: 'Bearer', expires_in: TTL_SECONDS, user_id: user.userId, permissions: ADMIN_PERMISSIONS })
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
      const body = await readJson(req)
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
      const body = await readJson(req)
      if (!body) return fail(res, 400, 'bad_request', '请求体不是合法的 JSON')
      const reg = registrations.get(str(body.registration_token))
      if (!reg || reg.code !== str(body.code)) return fail(res, 403, 'forbidden', '注册当前不可用或邀请码无效')
      const password = str(body.password)
      if (password.length < 8) return fail(res, 422, 'validation_failed', '请求参数校验未通过', { password: '密码至少需要 8 个字符' })
      if (!/[a-z]/i.test(password) || !/\d/.test(password)) return fail(res, 422, 'validation_failed', '请求参数校验未通过', { password: '密码必须同时包含字母和数字' })
      registrations.delete(str(body.registration_token))
      const user: User = { email: reg.email, password, userId: randomUUID() }
      users.set(reg.email, user)
      return send(res, 201, { user_id: user.userId, email: user.email })
    }
    if (app === 'portal' && route === 'POST /v1/auth/quick-login') {
      const body = await readJson(req)
      const entry = quickTokens.get(str(body?.token))
      quickTokens.delete(str(body?.token))
      if (!entry || entry.expires < Date.now()) return fail(res, 401, 'unauthorized', '快捷登录链接无效或已过期')
      return send(res, 200, { access_token: issue(entry.userId), refresh_token: randomUUID(), expires_in: TTL_SECONDS })
    }
    if (route === 'POST /__mock/expire-reauth') {
      sessions.forEach((s) => (s.rat = 0))
      return send(res, 204)
    }

    // --- 以下需登录 ----------------------------------------------------------
    const auth = bearer(req)
    if (!auth) return fail(res, 401, 'unauthorized', '需要登录')
    const user = userById(auth.session.userId)
    if (!user) return fail(res, 401, 'unauthorized', '需要登录')
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
          permissions: ADMIN_PERMISSIONS,
          reauthed,
          email: user.email,
          display_name: '林舟',
          roles: [{ code: 'ops', name: '运维' }],
        })
      }
      if (route === 'POST /v1/auth/reauth') {
        const body = await readJson(req)
        if (!body) return fail(res, 400, 'bad_request', '请求体不是合法的 JSON')
        if (str(body.password) === '') return fail(res, 422, 'validation_failed', '请求参数校验未通过', { password: '请输入当前密码' })
        if (str(body.password) !== user.password) return fail(res, 401, 'unauthorized', '密码不正确')
        // 与后端一致：同一会话换发一枚 rat 刷新的令牌，旧令牌照样有效到会话结束
        return send(res, 200, { access_token: issue(user.userId), token_type: 'Bearer', expires_in: TTL_SECONDS })
      }
      if (route === 'POST /v1/me/password') {
        const body = await readJson(req)
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
      const balance = /^POST \/v1\/users\/([^/]+)\/balance$/.exec(route)
      if (balance) {
        // 与后端同序：权限 → reauth → 幂等，reauth 拒绝不消耗幂等键
        if (!reauthed) return fail(res, 403, 'reauth_required', '此操作需要重新验证身份')
        const key = req.headers['idempotency-key']
        if (typeof key !== 'string' || key === '') return fail(res, 400, 'bad_request', '缺少 Idempotency-Key')
        const body = await readJson(req)
        const replay = replays.get(key)
        if (replay) return send(res, replay.status, replay.body)
        const amount = typeof body?.amount === 'number' ? body.amount : 0
        balanceCents += amount
        const result = { ok: true, user_id: balance[1], balance: balanceCents, idempotency_key: key }
        replays.set(key, { status: 200, body: result })
        return send(res, 200, result)
      }
    } else {
      if (route === 'GET /v1/me') {
        return send(res, 200, {
          user_id: user.userId,
          email: user.email,
          display_name: user.email === MOCK_ACCOUNTS.portal.email ? '张伟' : null,
          status: 'active',
          created_at: '2026-05-04T11:40:00Z',
          permissions: null,
        })
      }
      if (route === 'GET /v1/me/balance') return send(res, 200, { balance: 2650, currency: 'CNY', history: [] })
      if (route === 'GET /v1/me/subscriptions') {
        return send(res, 200, {
          subscriptions: [
            {
              id: randomUUID(),
              plan_id: randomUUID(),
              price_id: '',
              plan_name: '专业版',
              plan_version: 3,
              status: 'active',
              current_period_start: '2026-09-04T00:00:00Z',
              current_period_end: '2026-11-04T00:00:00Z',
              currency: 'CNY',
              amount: 5900,
              quotas: [],
            },
          ],
        })
      }
      if (route === 'GET /v1/me/commission') {
        return send(res, 200, {
          summary: { currency: 'CNY', pending: 0, available: 8640, withdrawing: 0, settled: 0, invitees: 4, orders: 2, rate_percent: 20, min_withdraw: 5000 },
          entries: [],
          withdrawals: [],
        })
      }
      if (route === 'GET /v1/me/notifications') return send(res, 200, { notifications: [], unread: 2 })
      if (route === 'POST /v1/me/quick-login') {
        const token = randomUUID().replace(/-/g, '')
        quickTokens.set(token, { userId: user.userId, expires: Date.now() + 60_000 })
        return send(res, 200, { token, expires_at: new Date(Date.now() + 60_000).toISOString(), expires_in: 60 })
      }
    }
    return fail(res, 404, 'not_found', '资源不存在或无权访问')
  }

  // 与后端同帧格式：首帧 retry，事件 id / event / data，25 秒注释心跳；这里每 15 秒随机推一条表变更
  function stream(req: IncomingMessage, res: ServerResponse) {
    res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store', Connection: 'keep-alive' })
    res.write('retry: 5000\n\n')
    const topics = app === 'admin' ? ADMIN_TOPICS : PORTAL_TOPICS
    let seq = 0
    const tick = setInterval(() => {
      const [topic, table] = topics[Math.floor(Math.random() * topics.length)]!
      const op = ['INSERT', 'UPDATE', 'UPDATE', 'DELETE'][Math.floor(Math.random() * 4)]
      res.write(`id: ${++seq}\nevent: ${topic}\ndata: ${JSON.stringify({ table, op, id: randomUUID() })}\n\n`)
    }, 15_000)
    const ping = setInterval(() => res.write(': ping\n\n'), 25_000)
    req.on('close', () => {
      clearInterval(tick)
      clearInterval(ping)
    })
  }

  return {
    name: 'pandora-mock-api',
    apply: 'serve',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const path = (req.url ?? '').split('?')[0] ?? ''
        if (!path.startsWith('/v1/') && !path.startsWith('/__mock/')) return next()
        handle(req, res, path).catch((err: unknown) => {
          server.config.logger.error(`[mock-api] ${String(err)}`)
          if (!res.headersSent) fail(res, 500, 'internal_error', '服务暂时不可用，请稍后重试')
        })
      })
      server.config.logger.info(`  mock api (${app}): ${MOCK_ACCOUNTS[app].email} / ${MOCK_ACCOUNTS[app].password}`)
    },
  }
}

const ADMIN_PERMISSIONS = [
  'ops.notification.read',
  'ops.ticket.read',
  'iam.user.read',
  'iam.user.write',
  'catalog.read',
  'billing.order.read',
  'node.read',
]

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
