import type { IncomingMessage, ServerResponse } from 'node:http'

export type Json = Record<string, unknown>
export type MockApp = 'admin' | 'portal'

export interface MockUser {
  email: string
  password: string
  userId: string
  displayName: string | null
}

/** 非 JSON 响应体（CSV 导出等） */
export interface MockRaw {
  contentType: string
  text: string
  /** 只在第一次响应里带的头（如 Content-Disposition）；幂等重放与后端一致，只回 Content-Type 与 Cache-Control */
  headers?: Readonly<Record<string, string>>
}

/** 可以原样回放的响应：幂等重放时状态码与响应体都与第一次一致；raw 与 body 二选一 */
export interface MockResult {
  status: number
  body?: unknown
  raw?: MockRaw
  /** 2xx 时以「不可重放」完成（与后端 CompleteUnreplayable 一致）：响应里有一次性秘密，同 key 同请求再来回 409 conflict，不重放 */
  unreplayable?: boolean
}

/** 匿名接口的上下文 */
export interface AnonContext {
  app: MockApp
  req: IncomingMessage
  res: ServerResponse
  method: string
  /** 不含查询串，形如 /v1/users/abc */
  path: string
  query: URLSearchParams
  /** 路由模式里 :name 段的值，已 URI 解码 */
  params: Readonly<Record<string, string>>
  /** 请求体按 JSON 对象解析：空体为 {}，非对象或非法 JSON 为 null（应回 400）；多次调用读同一份 */
  body(): Promise<Json | null>
  send(status: number, body?: unknown): void
  /** 非 JSON 响应，带 Cache-Control: no-store */
  sendRaw(status: number, raw: MockRaw): void
  /** 错误信封 { error: { code, message, fields?, request_id } }，形状与 platform/httpx 一致 */
  fail(status: number, code: string, message: string, fields?: Record<string, string>): void
}

/**
 * 需登录接口的上下文。后台不分权限（登录了就是管理员），admin 写接口按后端中间件顺序调用两个守卫：
 *   if (!ctx.requireReauth()) return
 *   await ctx.idempotent('scope', () => ({ status: 200, body }))
 * 守卫返回 false 时已经写好了响应，处理器直接 return。
 */
export interface MockContext extends AnonContext {
  user: MockUser
  /** 当前会话 15 分钟内验证过口令（GET v1/me 的 reauthed） */
  reauthed: boolean
  /** 外壳会话表里本账号的其它会话（不含当前令牌）：门户账号安全把它们与种子会话一起列出 */
  otherSessions(): ReadonlyArray<{ token: string; created: number; userAgent: string }>
  /** 吊销本账号的一个外壳会话，令牌立即失效（之后请求 401）；不是本账号的令牌返回 false */
  revokeSession(token: string): boolean
  /** RequireRecentReauth：超出窗口回 403 reauth_required 并返回 false；不消耗幂等键 */
  requireReauth(): boolean
  /**
   * Idempotency（契约 R85，与 Go 中间件一致）：缺 Idempotency-Key 回 400；同 key 同请求——上次是 2xx
   * 原样重放，run 返回 unreplayable: true 的 2xx 之后同 key 同请求回 409 conflict；上次非 2xx（含 5xx）
   * 重新执行 run，在途回 409 conflict；同 key 换请求（方法 + 路径 + 查询串 + 请求体）不论上次结果都回
   * 409 idempotency_key_reuse。scope 相同的路由共用一个键空间。
   */
  idempotent(scope: string, run: () => MockResult | Promise<MockResult>): Promise<void>
}

export type AnonRoute = (ctx: AnonContext) => void | Promise<void>
export type MockRoute = (ctx: MockContext) => void | Promise<void>

/**
 * 一个模块的假接口：键是「METHOD /v1/路径」，:name 段匹配一个非空路径段，如 'POST /v1/users/:id/balance'。
 * anonymous 不校验令牌；routes 需登录。未匹配的请求落到 404 not_found。
 */
export interface MockModule {
  anonymous?: Readonly<Record<string, AnonRoute>>
  routes?: Readonly<Record<string, MockRoute>>
}

/** 'POST /v1/users/:id' 对 'POST /v1/users/abc' 得 { id: 'abc' }；方法、段数或字面段不等得 null。 */
export function matchPattern(pattern: string, method: string, path: string): Record<string, string> | null {
  const space = pattern.indexOf(' ')
  if (pattern.slice(0, space) !== method) return null
  const want = pattern.slice(space + 1).split('/')
  const got = path.split('/')
  if (want.length !== got.length) return null
  const params: Record<string, string> = {}
  for (let i = 0; i < want.length; i++) {
    const w = want[i]!
    const g = got[i]!
    if (w.startsWith(':')) {
      if (g === '') return null
      try {
        params[w.slice(1)] = decodeURIComponent(g)
      } catch {
        return null
      }
    } else if (w !== g) {
      return null
    }
  }
  return params
}

/** 按模块登记顺序、模块内按键顺序找第一个匹配的处理器。 */
export function findRoute<H>(tables: ReadonlyArray<Readonly<Record<string, H>> | undefined>, method: string, path: string): { handler: H; params: Record<string, string> } | null {
  for (const table of tables) {
    for (const [pattern, handler] of Object.entries(table ?? {})) {
      const params = matchPattern(pattern, method, path)
      if (params) return { handler, params }
    }
  }
  return null
}
