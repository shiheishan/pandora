/**
 * [INPUT]: 依赖 zod 的 ZodType 校验响应，依赖 ./token 的 TokenStore 读写 Bearer，依赖浏览器 fetch / crypto.getRandomValues / document.baseURI（均可注入）
 * [OUTPUT]: 对外提供 ApiError、isApiError、SERVER_ERROR_CODES 与错误码类型、resolveApiUrl、newIdempotencyKey、createApiClient 与 ApiClient（request/get/post/put/delete/reauth/openStream）
 * [POS]: core 的唯一 HTTP 出口，页面与 hooks 只经它访问两个网关；sse.ts 经 openStream 建流，query.ts 按它抛出的 ApiError 决定重试
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { ZodType } from 'zod'
import { z } from 'zod'
import type { TokenStore } from './token'

// CSP 没有 unsafe-eval：zod 默认会用 new Function 探测能否 JIT，异常虽被吞掉，
// 浏览器仍会报一条 securitypolicyviolation。关掉 JIT 连探测一起省去。
// 这是全局配置，api.ts 随入口首屏加载，早于任何一次 parse。
z.config({ jitless: true })

// ---------------------------------------------------------------------------
// 错误：服务端信封 {"error":{code,message,fields?,request_id?}} 解析成 ApiError。
// 码是 platform/httpx 的封闭列表（含第 ⑤ 步新增的 reauth_required）；另有两个
// 只在前端产生的码：network_error（请求没到服务器）与 invalid_response（回来的
// 不是约定的形状，含 nginx 错误页与 zod 校验失败）。页面按 code 分支、按 fields 标红。
// ---------------------------------------------------------------------------
export const SERVER_ERROR_CODES = [
  'bad_request',
  'unauthorized',
  'forbidden',
  'not_found',
  'conflict',
  'validation_failed',
  'rate_limited',
  'idempotency_key_reuse',
  'service_unavailable',
  'internal_error',
  'reauth_required',
] as const
export type ServerErrorCode = (typeof SERVER_ERROR_CODES)[number]
export type ApiErrorCode = ServerErrorCode | 'network_error' | 'invalid_response'

export class ApiError extends Error {
  /** HTTP 状态；请求没到服务器时为 0。 */
  readonly status: number
  readonly code: ApiErrorCode
  /** 表单校验错误，键不一定等于请求字段名（以契约条目为准）。 */
  readonly fields: Readonly<Record<string, string>>
  readonly requestId?: string

  constructor(init: {
    status: number
    code: ApiErrorCode
    message: string
    fields?: Record<string, string>
    requestId?: string
    cause?: unknown
  }) {
    super(init.message, init.cause === undefined ? undefined : { cause: init.cause })
    this.name = 'ApiError'
    this.status = init.status
    this.code = init.code
    this.fields = init.fields ?? {}
    this.requestId = init.requestId
  }
}

export function isApiError(error: unknown, code?: ApiErrorCode): error is ApiError {
  return error instanceof ApiError && (code === undefined || error.code === code)
}

const envelopeSchema = z.object({
  error: z.object({
    code: z.string(),
    message: z.string(),
    fields: z.record(z.string(), z.string()).optional(),
    request_id: z.string().optional(),
  }),
})

const knownCodes: ReadonlySet<string> = new Set(SERVER_ERROR_CODES)

// 没有信封（nginx 错误页、网关前的代理）时按状态推一个码，保证 401/404 照常生效
function codeForStatus(status: number): ApiErrorCode {
  switch (status) {
    case 400:
      return 'bad_request'
    case 401:
      return 'unauthorized'
    case 403:
      return 'forbidden'
    case 404:
      return 'not_found'
    case 409:
      return 'conflict'
    case 422:
      return 'validation_failed'
    case 429:
      return 'rate_limited'
    case 502:
    case 503:
    case 504:
      return 'service_unavailable'
    default:
      return status >= 500 ? 'internal_error' : 'invalid_response'
  }
}

const FALLBACK_MESSAGE: Partial<Record<ApiErrorCode, string>> = {
  unauthorized: '登录已失效，请重新登录',
  not_found: '资源不存在或无权访问',
  rate_limited: '操作太频繁，请稍后再试',
  service_unavailable: '服务暂时不可用，请稍后重试',
  internal_error: '服务暂时不可用，请稍后重试',
}

async function readError(res: Response): Promise<ApiError> {
  let text = ''
  try {
    text = await res.text()
  } catch {
    // 读不到正文就只凭状态
  }
  let parsed: z.infer<typeof envelopeSchema> | null = null
  try {
    const result = envelopeSchema.safeParse(JSON.parse(text))
    if (result.success) parsed = result.data
  } catch {
    // 不是 JSON
  }
  const fallback = codeForStatus(res.status)
  if (!parsed) {
    return new ApiError({
      status: res.status,
      code: fallback,
      message: FALLBACK_MESSAGE[fallback] ?? `请求失败（HTTP ${res.status}）`,
    })
  }
  const { code, message, fields, request_id } = parsed.error
  return new ApiError({
    status: res.status,
    // 封闭列表之外的码不该出现；出现了保留服务端文案，码按状态归类
    code: knownCodes.has(code) ? (code as ServerErrorCode) : fallback,
    message,
    fields,
    requestId: request_id,
  })
}

// ---------------------------------------------------------------------------
// 路径：一律相对 v1/…，相对入口页解析。后台在 nginx 高熵前缀后面、前缀被剥掉
// 再转发，写死 /v1 会打到门户网关；所以这里拒绝任何不以 v1/ 开头的路径。
// hash 不参与相对解析，#/users/… 下发请求照样落在前缀之下。
// ---------------------------------------------------------------------------
export type QueryParams = Record<string, string | number | boolean | null | undefined>

export function resolveApiUrl(path: string, base: string, query?: QueryParams): URL {
  if (!path.startsWith('v1/')) {
    throw new Error(`API path must be relative and start with "v1/": ${path}`)
  }
  const url = new URL(path, base)
  // 插值进路径的片段带了 ../ 也不许逃出入口目录（例如从后台前缀跳到门户网关）
  const root = new URL('.', base)
  if (url.origin !== root.origin || !url.pathname.startsWith(`${root.pathname}v1/`)) {
    throw new Error(`API path must be relative and stay under the entry directory: ${path}`)
  }
  for (const [name, value] of Object.entries(query ?? {})) {
    if (value !== undefined && value !== null) url.searchParams.set(name, String(value))
  }
  return url
}

// ---------------------------------------------------------------------------
// 幂等键：UUID v4。不用 crypto.randomUUID，它只在安全上下文（HTTPS / localhost）
// 存在，面板在明文 HTTP 下调试时会直接抛错；getRandomValues 没有这个限制。
// ---------------------------------------------------------------------------
export function newIdempotencyKey(
  random: (bytes: Uint8Array<ArrayBuffer>) => Uint8Array = (b) => crypto.getRandomValues(b),
): string {
  const b = random(new Uint8Array(16))
  b[6] = (b[6]! & 0x0f) | 0x40
  b[8] = (b[8]! & 0x3f) | 0x80
  const hex = Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

// ---------------------------------------------------------------------------
// 客户端
// ---------------------------------------------------------------------------
export type Method = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'

export interface RequestOptions {
  method?: Method
  query?: QueryParams
  /** JSON 请求体；后端 DisallowUnknownFields，字段须与契约逐字一致。部分 DELETE 必须传 {}。 */
  body?: unknown
  /**
   * 挂了 Idempotency 中间件的路由必须带。true = 本次调用生成一个；传字符串 = 调用方
   * 跨多次调用复用同一意图的键（例如表单失败后「重试」）。本次调用内的网络重试与
   * reauth 重放一律复用同一个键。
   */
  idempotencyKey?: true | string
  /** 默认 true。登录等匿名接口传 false：不带 Bearer，401 也不清令牌。 */
  auth?: boolean
  /**
   * 口令校验接口（admin 改密码 / reauth，门户改密码）：口令错也回 401，
   * 这里的 401 交给表单内联显示，不触发全局登出。
   */
  passwordCheck?: boolean
  signal?: AbortSignal
}

type MethodOptions = Omit<RequestOptions, 'method'>

export interface ApiClientOptions {
  tokens: TokenStore
  /**
   * 后台收到 403 reauth_required 时调用：界面弹「重新验证身份」，由对话框调
   * client.reauth(password)（口令错在框内显示），成功 resolve true、用户取消 resolve false。
   * 门户没有 reauth，不传。并发的多个请求共用同一次弹框。
   */
  requestReauth?: () => Promise<boolean>
  /** 会话失效（非口令接口的 401）时，在令牌已清空之后调用。 */
  onUnauthorized?: (error: ApiError) => void
  /** 相对路径的解析基准，默认 document.baseURI（即入口页地址，含后台前缀）。 */
  baseUrl?: () => string
  fetch?: typeof fetch
  /** 网络失败的重试间隔（毫秒），条数即最大重试次数；只对 GET 与带幂等键的请求生效。 */
  retryDelays?: readonly number[]
}

export interface ApiClient {
  request<S extends ZodType>(path: string, schema: S, options?: RequestOptions): Promise<z.output<S>>
  get<S extends ZodType>(path: string, schema: S, options?: MethodOptions): Promise<z.output<S>>
  post<S extends ZodType>(path: string, schema: S, options?: MethodOptions): Promise<z.output<S>>
  put<S extends ZodType>(path: string, schema: S, options?: MethodOptions): Promise<z.output<S>>
  delete<S extends ZodType>(path: string, schema: S, options?: MethodOptions): Promise<z.output<S>>
  /** admin POST v1/auth/reauth：成功后用新令牌替换本地令牌；口令错抛 401 ApiError，不登出。 */
  reauth(password: string, signal?: AbortSignal): Promise<void>
  /** 以 Bearer 打开一个 text/event-stream 响应，非 2xx 抛 ApiError（401 照常清令牌）。 */
  openStream(path: string, signal?: AbortSignal): Promise<Response>
}

/** 无响应体（204）的接口用它作 schema。 */
export const noContent = z.undefined()

const reauthResponseSchema = z.object({
  access_token: z.string().min(1),
  token_type: z.literal('Bearer'),
  expires_in: z.number(),
})

function isAbort(error: unknown, signal?: AbortSignal): boolean {
  return signal?.aborted === true || (error instanceof DOMException && error.name === 'AbortError')
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) return reject(signal.reason)
    const timer = setTimeout(done, ms)
    function done() {
      signal?.removeEventListener('abort', abort)
      resolve()
    }
    function abort() {
      clearTimeout(timer)
      reject(signal!.reason)
    }
    signal?.addEventListener('abort', abort, { once: true })
  })
}

export function createApiClient(options: ApiClientOptions): ApiClient {
  const { tokens } = options
  const doFetch = options.fetch ?? ((input, init) => fetch(input, init))
  const baseUrl = options.baseUrl ?? (() => document.baseURI)
  const retryDelays = options.retryDelays ?? [400, 1200]

  // 同一时刻只弹一次重新验证：并发被拦下的请求都等这一次的结果
  let pendingReauth: Promise<boolean> | null = null
  function awaitReauth(): Promise<boolean> {
    const ask = options.requestReauth
    if (!ask) return Promise.resolve(false)
    pendingReauth ??= Promise.resolve()
      .then(ask)
      .catch(() => false)
      .finally(() => {
        pendingReauth = null
      })
    return pendingReauth
  }

  function sessionLost(error: ApiError, usedToken: string | null) {
    // 只清发请求时用的那枚：期间用户已重新登录的话，新令牌不能被旧请求的 401 清掉
    if (usedToken !== null && tokens.get() === usedToken) {
      tokens.clear()
      options.onUnauthorized?.(error)
    }
  }

  async function send(url: URL, method: Method, opts: RequestOptions, key: string | undefined, accept: string) {
    const headers = new Headers({ Accept: accept })
    const token = opts.auth === false ? null : tokens.get()
    if (token) headers.set('Authorization', `Bearer ${token}`)
    if (key !== undefined) headers.set('Idempotency-Key', key)
    let body: string | undefined
    if (opts.body !== undefined) {
      headers.set('Content-Type', 'application/json')
      body = JSON.stringify(opts.body)
    }
    const res = await doFetch(url, { method, headers, body, signal: opts.signal, credentials: 'omit', cache: 'no-store' })
    return { res, token }
  }

  async function parse<S extends ZodType>(res: Response, schema: S): Promise<z.output<S>> {
    let value: unknown
    try {
      const text = await res.text()
      value = text === '' ? undefined : JSON.parse(text)
    } catch (cause) {
      throw new ApiError({ status: res.status, code: 'invalid_response', message: '服务器返回的数据无法解析', cause })
    }
    const result = schema.safeParse(value)
    if (!result.success) {
      throw new ApiError({
        status: res.status,
        code: 'invalid_response',
        message: '服务器返回的数据与约定不符',
        cause: result.error,
      })
    }
    return result.data
  }

  async function exchange(path: string, opts: RequestOptions, accept: string): Promise<Response> {
    const method = opts.method ?? 'GET'
    const url = resolveApiUrl(path, baseUrl(), opts.query)
    const key = opts.idempotencyKey === true ? newIdempotencyKey() : opts.idempotencyKey
    // 网络失败时服务器可能已经处理过：只有 GET 与带幂等键的写请求可以安全重发
    const replayable = method === 'GET' || key !== undefined
    let networkRetries = 0
    let replayedAfterReauth = false

    for (;;) {
      let sent: Awaited<ReturnType<typeof send>>
      try {
        sent = await send(url, method, opts, key, accept)
      } catch (cause) {
        if (isAbort(cause, opts.signal)) throw cause
        const delay = retryDelays[networkRetries]
        if (replayable && delay !== undefined) {
          networkRetries++
          await sleep(delay, opts.signal)
          continue
        }
        throw new ApiError({ status: 0, code: 'network_error', message: '网络连接失败，请检查网络后重试', cause })
      }

      const { res, token } = sent
      if (res.ok) return res

      const error = await readError(res)
      if (error.status === 401 && opts.auth !== false && !opts.passwordCheck) {
        sessionLost(error, token)
      }
      // reauth_required 在 Idempotency 之前拦下，处理器没执行、幂等键没消耗，
      // 所以不论有没有键都可以重放；有键时用同一个键。只重放一次。
      if (error.code === 'reauth_required' && !replayedAfterReauth) {
        replayedAfterReauth = true
        // 发出后别处已经换过令牌（另一个请求刚完成重新验证）：直接用新令牌重放
        const refreshed = tokens.get() !== token && tokens.get() !== null
        if (refreshed || (await awaitReauth())) continue
      }
      throw error
    }
  }

  const client: ApiClient = {
    async request(path, schema, opts = {}) {
      return parse(await exchange(path, opts, 'application/json'), schema)
    },
    get: (path, schema, opts) => client.request(path, schema, { ...opts, method: 'GET' }),
    post: (path, schema, opts) => client.request(path, schema, { ...opts, method: 'POST' }),
    put: (path, schema, opts) => client.request(path, schema, { ...opts, method: 'PUT' }),
    delete: (path, schema, opts) => client.request(path, schema, { ...opts, method: 'DELETE' }),
    async reauth(password, signal) {
      const res = await client.post('v1/auth/reauth', reauthResponseSchema, {
        body: { password },
        passwordCheck: true,
        signal,
      })
      tokens.set(res.access_token)
    },
    openStream: (path, signal) => exchange(path, { method: 'GET', signal }, 'text/event-stream'),
  }
  return client
}
