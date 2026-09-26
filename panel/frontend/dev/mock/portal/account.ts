/**
 * [INPUT]: 依赖 node:crypto 的 createHash / randomInt，依赖 ../types 的 MockModule / MockContext，依赖 ../quick-login 的 issueQuickLogin，依赖 ./fixtures 的 gate / portalState / scenario / PortalState，依赖 ./billing 的 isUuid / readStrict
 * [OUTPUT]: 对外提供 account 模块的假接口 MockModule
 * [POS]: dev/mock/portal 的「账号安全（门户-10）」假接口，归门户前端；形状照 api-contract.md（修订 R15、R28、R62、R114 的 last_seen_at 与按它排序）与 Go identity / notify / notifications.go。会话：当前会话与外壳里本账号的其它会话（ctx.otherSessions，如快捷登录新建的）都由 Bearer 令牌推出稳定 id，另有两条种子会话；下线外壳会话经 ctx.revokeSession 让那枚令牌立即失效，下线种子会话只从本模块的列表删除；改密校验与后端同序（空 422 两字段、旧密码错 401、新旧相同 400、规则 422 fields.password），成功改掉外壳账号的口令并清掉其余会话。Telegram：default 未绑定、multi 已绑定、legacy 站点未启用；获取绑定码 8 秒后视为用户已在 Telegram 发送 /start CODE，下一次读状态即已绑定。通知偏好 3 类 × 2 渠道，交易类锁定。签发快捷登录令牌在这里（绑定当前会话，重新生成作废旧令牌），消费端 POST v1/auth/quick-login 属外壳，两边经 quick-login.ts 共用令牌表
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { createHash, randomInt } from 'node:crypto'
import { issueQuickLogin } from '../quick-login.ts'
import type { MockContext, MockModule } from '../types.ts'
import { isUuid, readStrict } from './billing.ts'
import { gate, portalState, scenario, type PortalState } from './fixtures.ts'

interface SessionFixture {
  id: string
  user_agent: string
  created_at: string
  /** R62 / R114：认证中间件刷新的最近活跃（同一会话 5 分钟最多写一次）；没有单独记录时等于登录时间 */
  last_seen_at: string
}

interface AccountState {
  /** 当前会话以外的门户会话 */
  others: SessionFixture[]
  /** 令牌第一次出现的时刻，当作当前会话的登录时间 */
  firstSeen: Map<string, string>
  telegram: { enabled: boolean; username: string | null; pending: { code: string; issued: number; expires: number } | null }
  prefs: Map<string, boolean>
}

const BOT = 'pandora_notify_bot'
const BIND_TTL_MS = 10 * 60_000
/** 假装用户在 Telegram 里发送 /start CODE 用的时间 */
const BIND_AFTER_MS = 8_000
const DAY_MS = 86_400_000

const CATALOG: ReadonlyArray<{ category: string; channel: string; locked: boolean }> = [
  { category: 'transactional', channel: 'email', locked: true },
  { category: 'transactional', channel: 'telegram', locked: true },
  { category: 'service', channel: 'email', locked: false },
  { category: 'service', channel: 'telegram', locked: false },
  { category: 'marketing', channel: 'email', locked: false },
  { category: 'marketing', channel: 'telegram', locked: false },
]

// 挂在 PortalState 上（WeakMap），切场景重建时跟着重建
const states = new WeakMap<PortalState, AccountState>()

function stateOf(userId: string): AccountState {
  const portal = portalState(userId)
  let state = states.get(portal)
  if (!state) {
    state = build()
    states.set(portal, state)
  }
  return state
}

function build(): AccountState {
  const ago = (ms: number) => new Date(Date.now() - ms).toISOString()
  const s = scenario()
  return {
    others:
      s === 'empty'
        ? []
        : [
            {
              id: '3b1f6c2e-5d7a-4c11-9e2b-7a0d4f8c6e21',
              user_agent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1',
              created_at: ago(2 * 3_600_000),
              last_seen_at: ago(20 * 60_000),
            },
            {
              id: '8c4e2a91-0f3b-4d6a-b7e5-1c9d2f4a8b30',
              user_agent: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36 Edg/129.0.0.0',
              created_at: ago(3 * DAY_MS),
              last_seen_at: ago(DAY_MS),
            },
          ],
    firstSeen: new Map(),
    telegram: { enabled: s !== 'legacy', username: s === 'multi' ? 'zhangwei_tg' : null, pending: null },
    prefs: new Map([['marketing\ntelegram', false]]),
  }
}

function bearer(ctx: MockContext): string {
  const header = ctx.req.headers.authorization ?? ''
  return header.startsWith('Bearer ') ? header.slice(7) : ''
}

/** 令牌 → 稳定的会话 id（UUID 形状），同一个令牌每次得到同一个 */
function sessionIdOf(token: string): string {
  const h = createHash('sha256').update(token).digest('hex')
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-4${h.slice(13, 16)}-a${h.slice(17, 20)}-${h.slice(20, 32)}`
}

/** 外壳会话表里本账号的其它会话，形状同种子会话，另带令牌供吊销 */
function shellOthers(ctx: MockContext): Array<SessionFixture & { token: string }> {
  return ctx.otherSessions().map((s) => ({ id: sessionIdOf(s.token), token: s.token, user_agent: s.userAgent, created_at: new Date(s.created).toISOString(), last_seen_at: new Date(s.created).toISOString() }))
}

function currentSession(ctx: MockContext, state: AccountState): SessionFixture {
  const token = bearer(ctx)
  let created = state.firstSeen.get(token)
  if (!created) {
    created = new Date().toISOString()
    state.firstSeen.set(token, created)
  }
  // 当前会话正在发请求，认证中间件刚刷新过它（5 分钟节流，这里取整到 5 分钟内的「现在」即可）
  return { id: sessionIdOf(token), user_agent: String(ctx.req.headers['user-agent'] ?? ''), created_at: created, last_seen_at: new Date().toISOString() }
}

// Go SessionInfo：country 无人写入（omitempty 缺席），last_seen_at 由认证中间件刷新（R62 / R114）
function sessionView(s: SessionFixture, current: boolean) {
  return {
    id: s.id,
    current,
    user_agent: s.user_agent,
    created_at: s.created_at,
    last_seen_at: s.last_seen_at,
    expires_at: new Date(Date.parse(s.created_at) + 30 * DAY_MS).toISOString(),
  }
}

/** 与 crypto.ValidatePassword 同序 */
function passwordRule(pw: string): string | null {
  if ([...pw].length < 8) return '密码至少需要 8 个字符'
  if (Buffer.byteLength(pw) > 256) return '密码过长'
  if (!/\p{L}/u.test(pw) || !/\p{Nd}/u.test(pw)) return '密码必须同时包含字母和数字'
  return null
}

function newBindCode(): string {
  const alphabet = 'ABCDEFGHJKMNPQRSTUVWXYZ23456789'
  return Array.from({ length: 8 }, () => alphabet[randomInt(alphabet.length)]).join('')
}

/** 读状态前结算：绑定码发出 8 秒后视为已在 Telegram 发送，过期的码作废 */
function settleTelegram(state: AccountState) {
  const p = state.telegram.pending
  if (!p) return
  const now = Date.now()
  if (now >= p.expires) state.telegram.pending = null
  else if (now - p.issued >= BIND_AFTER_MS && state.telegram.enabled) {
    state.telegram.username = 'zhangwei_tg'
    state.telegram.pending = null
  }
}

export const account: MockModule = {
  routes: {
    'POST /v1/me/quick-login': (ctx) => {
      const { token, expires } = issueQuickLogin(ctx.user.userId, bearer(ctx))
      ctx.send(200, { token, expires_at: new Date(expires).toISOString(), expires_in: 60 })
    },

    // --- 会话（修订 R15：只有门户会话） ------------------------------------
    'GET /v1/me/sessions': async (ctx) => {
      if (!(await gate(ctx))) return
      const state = stateOf(ctx.user.userId)
      const current = currentSession(ctx, state)
      const list = [sessionView(current, true), ...shellOthers(ctx).map((s) => sessionView(s, false)), ...state.others.map((s) => sessionView(s, false))]
      // 与 identity.ListActiveSessions 同序：按最近活跃倒序
      list.sort((a, b) => b.last_seen_at.localeCompare(a.last_seen_at))
      ctx.send(200, { sessions: list.slice(0, 50) })
    },
    'DELETE /v1/me/sessions/:id': (ctx) => {
      const state = stateOf(ctx.user.userId)
      const id = ctx.params.id!
      if (!isUuid(id)) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      if (id === currentSession(ctx, state).id) return ctx.fail(422, 'validation_failed', '这是你当前正在使用的会话，请使用「退出登录」')
      const shell = shellOthers(ctx).find((s) => s.id === id)
      if (shell) {
        ctx.revokeSession(shell.token)
        return ctx.send(200, { revoked: true })
      }
      const at = state.others.findIndex((s) => s.id === id)
      if (at < 0) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      state.others.splice(at, 1)
      ctx.send(200, { revoked: true })
    },

    // --- 改密码（修订 R62：保留当前会话，其余吊销） -------------------------
    'POST /v1/me/password': async (ctx) => {
      const body = await readStrict(ctx, ['old_password', 'new_password'])
      if (!body) return
      const oldPw = typeof body.old_password === 'string' ? body.old_password : ''
      const newPw = typeof body.new_password === 'string' ? body.new_password : ''
      if (!oldPw || !newPw) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { old_password: '必填', new_password: '必填' })
      if (oldPw !== ctx.user.password) return ctx.fail(401, 'unauthorized', '当前密码不正确')
      if (newPw === oldPw) return ctx.fail(400, 'bad_request', '新密码不能与当前密码相同')
      const rule = passwordRule(newPw)
      if (rule) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { password: rule })
      ctx.user.password = newPw
      stateOf(ctx.user.userId).others = []
      ctx.send(200, { ok: true })
    },

    // --- Telegram ------------------------------------------------------------
    'GET /v1/me/telegram': async (ctx) => {
      if (!(await gate(ctx))) return
      const state = stateOf(ctx.user.userId)
      settleTelegram(state)
      const t = state.telegram
      ctx.send(200, { bound: t.username !== null, ...(t.username ? { username: t.username } : {}), ...(t.enabled ? { bot_username: BOT } : {}), enabled: t.enabled })
    },
    'POST /v1/me/telegram/bind-code': (ctx) => {
      const state = stateOf(ctx.user.userId)
      settleTelegram(state)
      const t = state.telegram
      if (!t.enabled) return ctx.fail(422, 'validation_failed', '站点还没有启用 Telegram 通知')
      if (t.username !== null) return ctx.fail(409, 'conflict', '你已经绑定过 Telegram 了')
      const now = Date.now()
      // 再次获取时旧码作废
      t.pending = { code: newBindCode(), issued: now, expires: now + BIND_TTL_MS }
      ctx.send(200, { code: t.pending.code, bot_username: BOT, expires_at: new Date(t.pending.expires).toISOString() })
    },
    'DELETE /v1/me/telegram': (ctx) => {
      const state = stateOf(ctx.user.userId)
      settleTelegram(state)
      if (state.telegram.username === null) return ctx.fail(404, 'not_found', '你还没有绑定 Telegram')
      state.telegram.username = null
      ctx.send(200, { unbound: true })
    },

    // --- 通知偏好（修订 R28） --------------------------------------------------
    'GET /v1/me/notification-preferences': async (ctx) => {
      if (!(await gate(ctx))) return
      const prefs = stateOf(ctx.user.userId).prefs
      ctx.send(200, {
        preferences: CATALOG.map((c) => ({ ...c, enabled: c.locked ? true : (prefs.get(`${c.category}\n${c.channel}`) ?? true) })),
      })
    },
    'PUT /v1/me/notification-preferences': async (ctx) => {
      const body = await readStrict(ctx, ['category', 'channel', 'enabled'])
      if (!body) return
      if (!CATALOG.some((c) => c.category === body.category && c.channel === body.channel)) {
        return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { preference: '不支持的通知类别或渠道' })
      }
      if (body.category === 'transactional' && body.enabled !== true) return ctx.fail(422, 'validation_failed', '交易类通知无法关闭')
      stateOf(ctx.user.userId).prefs.set(`${String(body.category)}\n${String(body.channel)}`, body.enabled === true)
      ctx.send(200, { ok: true })
    },
  },
}
