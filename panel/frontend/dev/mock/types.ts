/**
 * [INPUT]: 依赖 node:http 的 IncomingMessage / ServerResponse 类型
 * [OUTPUT]: 对外提供 Json、MockApp、MockUser、MockRaw、MockResult、AnonContext、MockContext、AnonRoute、MockRoute、MockModule、matchPattern、findRoute
 * [POS]: dev/mock 的处理器契约：mock-api.ts 为每个请求造一份上下文，按入口依次询问 admin/ 或 portal/ 下的模块处理器；模块文件只依赖这里，不碰会话、令牌、幂等表的实现
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { IncomingMessage, ServerResponse } from 'node:http'

export type Json = Record<string, unknown>
export type MockApp = 'admin' | 'portal'

export interface MockUser {
  email: string
  password: string
  userId: string
  displayName: string | null
  /** admin 账号的权限码（GET v1/me 的 permissions）；portal 账号为空数组 */
  permissions: readonly string[]
  roles: ReadonlyArray<{ code: string; name: string }>
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
 * 需登录接口的上下文。admin 写接口按后端中间件顺序调用三个守卫：
 *   if (!ctx.requirePermission('x.y.write') || !ctx.requireReauth()) return
 *   await ctx.idempotent('scope', () => ({ status: 200, body }))
 * 守卫返回 false 时已经写好了响应，处理器直接 return。
 */
export interface MockContext extends AnonContext {
  user: MockUser
  /** 当前会话 15 分钟内验证过口令（GET v1/me 的 reauthed） */
  reauthed: boolean
  /** RequirePermission：缺权限回 404 not_found（与后端 NotFoundOrForbidden 一致）并返回 false */
  requirePermission(code: string): boolean
  /** RequireRecentReauth：超出窗口回 403 reauth_required 并返回 false；不消耗幂等键 */
  requireReauth(): boolean
  /**
   * Idempotency（契约 R85，与 Go 中间件一致）：缺 Idempotency-Key 回 400；同 key 同请求——上次是 2xx
   * 原样重放，上次非 2xx（含 5xx）重新执行 run，在途回 409 conflict；同 key 换请求（方法 + 路径 +
   * 查询串 + 请求体）不论上次结果都回 409 idempotency_key_reuse。scope 相同的路由共用一个键空间。
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
