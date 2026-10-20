import { randomUUID } from 'node:crypto'
import type { IncomingMessage, ServerResponse } from 'node:http'
import type { Plugin } from 'vite'
import { shareAppearance } from './mock/admin/content.ts'
import { ADMIN_MODULES } from './mock/admin/index.ts'
import { readSharedSiteName, readSharedTheme } from './mock/appearance-share.ts'
import { pickAdminEvent } from './mock/admin/events.ts'
import { adminWritesEnabled, onSwitchChanged } from './mock/admin/security.ts'
import { MOCK_SITE } from './mock/portal/fixtures.ts'
import { PORTAL_MODULES } from './mock/portal/index.ts'
import { consumeQuickLogin } from './mock/quick-login.ts'
import { allowMailLinkSend, consumeMailLink, issueMailLink, lastMailLink } from './mock/mail-link.ts'
import { findRoute, matchPattern, type AnonContext, type Json, type MockApp, type MockContext, type MockRaw, type MockResult, type MockUser } from './mock/types.ts'

// ---------------------------------------------------------------------------
// 形状以 panel/internal/module/*/handler 的 Go 处理器为准。这里只放外壳接口与共用设施：
// 会话与 rat、reauth 窗口、幂等表、错误信封；模块接口写在 mock/<入口>/<模块>.ts，
// 一个模块一个文件、各自维护。先外壳、后模块，未匹配的 v1/ 一律 404 信封。
// POST /__mock/expire-reauth 让所有会话的 rat 立即过期，不必干等 15 分钟。
// ---------------------------------------------------------------------------
export const MOCK_ACCOUNTS = {
  admin: { email: 'admin@example.test', password: 'pandora-dev-pass' },
  portal: { email: 'user@example.test', password: 'pandora-dev-pass' },
} as const

const REAUTH_WINDOW_MS = 15 * 60 * 1000
/** 与 Go auth/service/store.go 的 refreshGrace 一致 */
const REFRESH_GRACE_MS = 30 * 1000
/** access 令牌有效期（秒），与架构 8.3 一致 */
const ACCESS_EXPIRES_IN = 15 * 60
const TTL_SECONDS = 2592000

const RT_COOKIE: Record<MockApp, string> = { portal: 'pandora_rt', admin: 'pandora_admin_rt' }

/** 一条登录会话的 refresh 链：照 Go 记当前一枚、上一枚与轮换时间 */
interface RefreshFamily {
  userId: string
  app: MockApp
  cur: string
  prev: string | null
  rotatedAt: number
}

function parseCookieHeader(header: string | undefined): Record<string, string> {
  const out: Record<string, string> = {}
  if (!header) return out
  for (const part of header.split(';')) {
    const eq = part.indexOf('=')
    if (eq < 0) continue
    const name = part.slice(0, eq).trim()
    const value = part.slice(eq + 1).trim()
    if (name) out[name] = value
  }
  return out
}

function setRefreshCookie(res: ServerResponse, app: MockApp, value: string, maxAge?: number) {
  const name = RT_COOKIE[app]
  const attrs = ['HttpOnly', 'SameSite=Strict', 'Path=/v1/auth']
  if (maxAge === 0) attrs.push('Max-Age=0')
  else attrs.push(`Max-Age=${maxAge ?? TTL_SECONDS}`)
  res.setHeader('Set-Cookie', `${name}=${value}; ${attrs.join('; ')}`)
}

function clearRefreshCookie(res: ServerResponse, app: MockApp) {
  setRefreshCookie(res, app, '', 0)
}

// 后台与门户的登录响应同形：后台不分权限，不带权限码
function tokenPayload(token: string, user: MockUser) {
  return { access_token: token, token_type: 'Bearer' as const, expires_in: ACCESS_EXPIRES_IN, user_id: user.userId }
}

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
  /** 2xx 以不可重放完成时不存响应体，同 key 同请求再来回 409 conflict */
  unreplayable?: boolean
}

/**
 * 与 Go settings/handler/middleware.go 的 adminWriteAllowlist 逐条相同（路由模板，去掉 /v1 前缀），
 * 守卫是 Go 的 TestAdminWriteAllowlistMatchesMock。GET 类一律放行。
 */
const ADMIN_WRITE_ALLOWLIST = [
  'POST /auth/login',
  'POST /auth/refresh',
  'POST /auth/logout',
  'POST /auth/reauth',
  'POST /me/password',
  'POST /switches/:code',
  'POST /mail/templates/preview',
  'POST /subscription-templates/:format/preview',
] as const

function adminWriteExempt(method: string, path: string): boolean {
  if (method === 'GET' || method === 'HEAD' || method === 'OPTIONS') return true
  const route = path.startsWith('/v1/') ? path.slice('/v1'.length) : path
  return ADMIN_WRITE_ALLOWLIST.some((pattern) => matchPattern(pattern, method, route) !== null)
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

/** 可选 captcha_token：有则必须是字符串且不超过 4096 字符 */
function captchaFieldError(body: Json): Record<string, string> | null {
  if (!('captcha_token' in body)) return null
  const v = body.captcha_token
  if (typeof v === 'string' && v.length <= 4096) return null
  return { captcha_token: '人机验证参数无效' }
}

function initialUsers(app: MockApp): MockUser[] {
  if (app === 'portal') return [{ ...MOCK_ACCOUNTS.portal, userId: randomUUID(), displayName: '张伟' }]
  return [{ ...MOCK_ACCOUNTS.admin, userId: randomUUID(), displayName: '林舟' }]
}

export function mockApi(app: MockApp): Plugin {
  const users = new Map<string, MockUser>(initialUsers(app).map((u) => [u.email, u]))
  const sessions = new Map<string, Session>()
  const registrations = new Map<string, { email: string; code: string }>()
  const resets = new Map<string, { code: string; attempts: number }>()
  const replays = new Map<string, Replay>()
  const refreshFamilies = new Set<RefreshFamily>()
  const modules = app === 'admin' ? ADMIN_MODULES : PORTAL_MODULES

  const findFamily = (value: string): { family: RefreshFamily; which: 'cur' | 'prev' } | null => {
    for (const family of refreshFamilies) {
      if (family.cur === value) return { family, which: 'cur' }
      if (family.prev === value) return { family, which: 'prev' }
    }
    return null
  }
  const dropFamiliesOf = (userId: string) => {
    for (const f of [...refreshFamilies]) {
      if (f.userId === userId) refreshFamilies.delete(f)
    }
  }
  const bindRefresh = (res: ServerResponse, userId: string) => {
    const value = randomUUID()
    const family: RefreshFamily = { userId, app, cur: value, prev: null, rotatedAt: Date.now() }
    refreshFamilies.add(family)
    setRefreshCookie(res, app, value)
    return value
  }
  // 后台假后端启动即按内存初值重写共享的生效主题与站点名：上次会话激活过的主题、改过的站点名不会残留到门户
  if (app === 'admin') shareAppearance()

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
      bindRefresh(res, user.userId)
      return send(res, 200, tokenPayload(token, user))
    }
    if (route === 'POST /v1/auth/refresh') {
      const cookies = parseCookieHeader(req.headers.cookie)
      const rt = cookies[RT_COOKIE[app]]
      if (!rt) return fail(res, 401, 'unauthorized', '需要登录')
      const found = findFamily(rt)
      if (!found || found.family.app !== app) {
        clearRefreshCookie(res, app)
        return fail(res, 401, 'unauthorized', '需要登录')
      }
      const { family, which } = found
      const refreshedUser = userById(family.userId)
      if (!refreshedUser) {
        refreshFamilies.delete(family)
        clearRefreshCookie(res, app)
        return fail(res, 401, 'unauthorized', '需要登录')
      }
      const okBody = (access: string) => ({ access_token: access, token_type: 'Bearer' as const, expires_in: ACCESS_EXPIRES_IN })
      if (which === 'cur') {
        family.prev = family.cur
        family.cur = randomUUID()
        family.rotatedAt = Date.now()
        setRefreshCookie(res, app, family.cur)
        return send(res, 200, okBody(issue(refreshedUser.userId, req)))
      }
      // 上一枚在宽限内：照 Go 不改状态，把当前那一枚原样再发一遍（Go 用 HMAC 从上一枚重算出它，这里直接取）
      if (Date.now() - family.rotatedAt <= REFRESH_GRACE_MS) {
        setRefreshCookie(res, app, family.cur)
        return send(res, 200, okBody(issue(refreshedUser.userId, req)))
      }
      // 照 Go 判为重放、吊销整条会话；假后端的 access 不挂会话，已发的 access 照旧到期，这点与 Go 不同、不影响前端流程
      refreshFamilies.delete(family)
      clearRefreshCookie(res, app)
      return fail(res, 401, 'unauthorized', '需要登录')
    }
    if (app === 'portal' && route === 'GET /v1/site-config') {
      return send(res, 200, { registration_mode: 'invite_only', email_verification: true, password_reset: true })
    }
    // 演示环境不连第三方人机验证；真实环境由后台设置决定 mail_link_login / captcha / email_suffixes
    if (app === 'portal' && route === 'GET /v1/signup-config') {
      return send(res, 200, { captcha: null, email_suffixes: [], mail_link_login: true })
    }
    if (app === 'portal' && route === 'GET /v1/appearance') {
      // 生效主题取后台假后端经 appearance-share 写下的那份（后台激活、门户刷新即生效）；没开过后台时用内置默认
      const shared = readSharedTheme()
      return send(res, 200, {
        site_name: readSharedSiteName() ?? MOCK_SITE,
        theme: shared ? shared.theme : { id: randomUUID(), code: 'paper', name: '默认 · 森林绿', is_builtin: true, is_active: true, tokens: { light: {}, dark: {} }, branding: {} },
        slots: { 'portal.login.notice': '<p>国庆活动：全场 8 折，优惠码 AUTUMN26</p>' },
      })
    }
    if (app === 'portal' && route === 'POST /v1/auth/register/start') {
      const body = await base.body()
      if (!body) return fail(res, 400, 'bad_request', '请求体不是合法的 JSON')
      const capErr = captchaFieldError(body)
      if (capErr) return fail(res, 422, 'validation_failed', '请求参数校验未通过', capErr)
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
      const user: MockUser = { email: reg.email, password, userId: randomUUID(), displayName: null }
      users.set(reg.email, user)
      return send(res, 201, { user_id: user.userId, email: user.email })
    }
    // 找回密码（identity.StartPasswordReset / CompletePasswordReset）：邮箱存不存在回同一个响应；
    // 演示验证码固定 123456，只在邮箱存在时出现在 dev_code 里（与 Go 开发模式一致）
    if (app === 'portal' && route === 'POST /v1/auth/password-reset/start') {
      const body = await base.body()
      if (!body) return fail(res, 400, 'bad_request', '请求体不是合法的 JSON')
      const capErr = captchaFieldError(body)
      if (capErr) return fail(res, 422, 'validation_failed', '请求参数校验未通过', capErr)
      const email = str(body.email).trim().toLowerCase()
      if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email)) return fail(res, 422, 'validation_failed', '请求参数校验未通过', { email: '邮箱格式不正确' })
      const known = users.has(email)
      if (known) resets.set(email, { code: '123456', attempts: 0 })
      return send(res, 200, {
        expires_at: new Date(Date.now() + 600_000).toISOString(),
        message: '若该邮箱已注册，验证码已发送',
        ...(known ? { dev_code: '123456' } : {}),
      })
    }
    if (app === 'portal' && route === 'POST /v1/auth/password-reset/complete') {
      const body = await base.body()
      if (!body) return fail(res, 400, 'bad_request', '请求体不是合法的 JSON')
      const email = str(body.email).trim().toLowerCase()
      const invalid = () => fail(res, 422, 'validation_failed', '请求参数校验未通过', { code: '验证码错误或已失效，请重新获取' })
      if (!/^\d{6}$/.test(str(body.code))) return invalid()
      const password = str(body.new_password)
      if (password.length < 8) return fail(res, 422, 'validation_failed', '请求参数校验未通过', { password: '密码至少需要 8 个字符' })
      if (!/[a-z]/i.test(password) || !/\d/.test(password)) return fail(res, 422, 'validation_failed', '请求参数校验未通过', { password: '密码必须同时包含字母和数字' })
      const pending = resets.get(email)
      const user = users.get(email)
      if (!pending || !user || pending.attempts >= 5) return invalid()
      if (pending.code !== str(body.code)) {
        pending.attempts++
        return invalid()
      }
      resets.delete(email)
      user.password = password
      // 用户定：重置后吊销该账号的全部登录
      for (const [token, s] of sessions) if (s.userId === user.userId) sessions.delete(token)
      dropFamiliesOf(user.userId)
      return send(res, 200, { ok: true })
    }
    if (app === 'portal' && route === 'POST /v1/auth/quick-login') {
      const body = await base.body()
      const userId = consumeQuickLogin(str(body?.token))
      if (!userId) return fail(res, 401, 'unauthorized', '快捷登录链接无效或已过期')
      const token = issue(userId, req)
      bindRefresh(res, userId)
      return send(res, 200, { access_token: token, token_type: 'Bearer', expires_in: ACCESS_EXPIRES_IN })
    }
    if (app === 'portal' && route === 'POST /v1/auth/mail-link/start') {
      const body = await base.body()
      if (!body) return fail(res, 400, 'bad_request', '请求体不是合法的 JSON')
      // 与找回第 1 步同口径：开了人机验证时带 captcha_token（演示环境不连第三方，只校验类型）
      const capErr = captchaFieldError(body)
      if (capErr) return fail(res, 422, 'validation_failed', '请求参数校验未通过', capErr)
      const email = str(body.email).trim().toLowerCase()
      if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email)) return fail(res, 422, 'validation_failed', '请求参数校验未通过', { email: '邮箱格式不正确' })
      if (!allowMailLinkSend(email)) return fail(res, 429, 'rate_limited', '请求过于频繁，请稍后再试')
      const user = users.get(email)
      if (user) {
        const token = issueMailLink(user.userId)
        console.info('[mock] 邮件登录链接：#/mail-login/' + token)
      }
      return send(res, 200, { ok: true, message: '如果这个邮箱已注册，登录链接已发出，15 分钟内有效' })
    }
    if (app === 'portal' && route === 'POST /v1/auth/mail-link/login') {
      const body = await base.body()
      const userId = consumeMailLink(str(body?.token))
      if (!userId) return fail(res, 401, 'unauthorized', '登录链接无效或已过期，请重新获取')
      const token = issue(userId, req)
      bindRefresh(res, userId)
      return send(res, 200, { access_token: token, token_type: 'Bearer', expires_in: ACCESS_EXPIRES_IN })
    }
    if (app === 'portal' && route === 'POST /v1/invite/visit') {
      return send(res, 200, { ok: true })
    }
    if (route === 'POST /__mock/expire-reauth') {
      sessions.forEach((s) => (s.rat = 0))
      return send(res, 204)
    }
    // 只有假后端有：把所有会话的上一次轮换拨到宽限之外，用来测「旧 cookie 超过 30 秒再用 = 重放」
    if (route === 'POST /__mock/expire-refresh-grace') {
      refreshFamilies.forEach((f) => (f.rotatedAt = 0))
      return send(res, 204)
    }
    // 只有假后端有：让测试与演示拿到刚发的登录链接
    if (app === 'portal' && route === 'GET /__mock/mail-link') {
      return send(res, 200, { token: lastMailLink() })
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

    // 登出不要求 access 有效（照 Go：凭 cookie 吊销会话；带着有效 Bearer 也一并作废；两样都没有也回 204）
    if (route === 'POST /v1/auth/logout') {
      const current = bearer(req)
      if (current) sessions.delete(current.token)
      const cookies = parseCookieHeader(req.headers.cookie)
      const rt = cookies[RT_COOKIE[app]]
      if (rt) {
        const found = findFamily(rt)
        if (found) refreshFamilies.delete(found.family)
      }
      clearRefreshCookie(res, app)
      return send(res, 204)
    }

    // --- 以下需登录 ----------------------------------------------------------
    const auth = bearer(req)
    const user = auth ? userById(auth.session.userId) : undefined
    if (!auth || !user) return fail(res, 401, 'unauthorized', '需要登录')
    const reauthed = Date.now() - auth.session.rat < REAUTH_WINDOW_MS

    if (route === 'GET /v1/events') return stream(req, res)

    if (app === 'admin') {
      if (route === 'GET /v1/me') {
        return send(res, 200, { user_id: user.userId, email: user.email, display_name: user.displayName, reauthed })
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
        dropFamiliesOf(user.userId)
        return send(res, 200, { ok: true, reauthenticate: true })
      }
    } else if (route === 'GET /v1/me') {
      return send(res, 200, {
        user_id: user.userId,
        email: user.email,
        display_name: user.displayName,
        status: 'active',
        created_at: '2026-05-04T11:40:00Z',
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
          if (seen.state === 'succeeded' && seen.unreplayable) return fail(res, 409, 'conflict', '同一请求正在处理或需要人工核对，请稍后重试')
          if (seen.state === 'succeeded' && seen.result) return seen.result.raw ? sendRaw(res, seen.result.status, seen.result.raw, true) : send(res, seen.result.status, seen.result.body)
          // failed：接管这一格，重新执行
        }
        replays.set(slot, { fingerprint, state: 'in_flight', result: null })
        try {
          const result = await run()
          const ok = result.status >= 200 && result.status < 300
          replays.set(
            slot,
            ok
              ? result.unreplayable
                ? { fingerprint, state: 'succeeded', result: null, unreplayable: true }
                : { fingerprint, state: 'succeeded', result }
              : { fingerprint, state: 'failed', result: null },
          )
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

  // 与后端同帧格式：首帧 retry，事件 id / event / data，25 秒注释心跳；这里每 15 秒随机推一条表变更（后台取种子里真有的行，见 mock/admin/events.ts），
  // 后台另在降级开关切换后立即推 switches.changed
  function stream(req: IncomingMessage, res: ServerResponse) {
    res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store', Connection: 'keep-alive' })
    res.write('retry: 5000\n\n')
    let seq = 0
    const unsubscribe = app === 'admin' ? onSwitchChanged((payload) => res.write(`id: ${++seq}\nevent: switches.changed\ndata: ${JSON.stringify(payload)}\n\n`)) : () => undefined
    const tick = setInterval(() => {
      const { topic, payload } = app === 'admin' ? pickAdminEvent() : pickPortalEvent()
      res.write(`id: ${++seq}\nevent: ${topic}\ndata: ${JSON.stringify(payload)}\n\n`)
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
      const account = app === 'admin' ? MOCK_ACCOUNTS.admin : MOCK_ACCOUNTS.portal
      server.config.logger.info(`  mock api (${app}): ${account.email}，口令 ${account.password}`)
    },
  }
}

const PORTAL_TOPICS: ReadonlyArray<readonly [string, string]> = [
  ['orders.changed', 'orders'],
  ['announcements.changed', 'announcements'],
]

function pickPortalEvent() {
  const [topic, table] = PORTAL_TOPICS[Math.floor(Math.random() * PORTAL_TOPICS.length)]!
  const op = ['INSERT', 'UPDATE', 'UPDATE', 'DELETE'][Math.floor(Math.random() * 4)]!
  return { topic, payload: { table, op, id: randomUUID() } }
}
