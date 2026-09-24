/**
 * [INPUT]: 依赖 vitest，依赖 zod，依赖 ./api 的 createApiClient 等全部导出，依赖 ./token 的 createTokenStore
 * [OUTPUT]: 对外提供 api.ts 的单元测试
 * [POS]: core/api 的验收测试：前缀下相对路径解析、Bearer、错误信封、zod 校验、幂等键在重试间复用、reauth 后以原键重放、401 登出与口令接口例外；用伪造的 fetch 记录每一次请求
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it, vi } from 'vitest'
import { z } from 'zod'
import type { ApiClient, ApiClientOptions } from './api'
import { ApiError, createApiClient, isApiError, newIdempotencyKey, noContent, resolveApiUrl } from './api'
import { createTokenStore } from './token'

// ---------------------------------------------------------------------------
// 伪造 fetch：按顺序消费应答脚本，并把每次请求的关键信息记下来
// ---------------------------------------------------------------------------
interface Call {
  url: string
  method: string
  authorization: string | null
  idempotencyKey: string | null
  accept: string | null
  body: unknown
}

type Reply = Response | Error | ((call: Call) => Response | Error)

function json(status: number, value: unknown): Response {
  return new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } })
}

function envelope(status: number, code: string, message = code, extra: Record<string, unknown> = {}): Response {
  return json(status, { error: { code, message, ...extra } })
}

const reauthRequired = () => envelope(403, 'reauth_required', '此操作需要重新验证身份')

function fakeFetch(replies: Reply[]) {
  const calls: Call[] = []
  const impl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const headers = new Headers(init?.headers)
    const call: Call = {
      url: String(input),
      method: init?.method ?? 'GET',
      authorization: headers.get('Authorization'),
      idempotencyKey: headers.get('Idempotency-Key'),
      accept: headers.get('Accept'),
      body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined,
    }
    calls.push(call)
    const next = replies.shift()
    if (next === undefined) throw new Error(`unexpected request ${call.method} ${call.url}`)
    const reply = typeof next === 'function' ? next(call) : next
    if (reply instanceof Error) throw reply
    return reply
  })
  return { calls, fetch: impl as unknown as typeof fetch }
}

const ADMIN_BASE = 'https://panel.example/__x7Kq9mZ2__/'

function setup(replies: Reply[], extra: Partial<ApiClientOptions> = {}, token: string | null = 'tok-1') {
  const tokens = createTokenStore('admin', { storage: null, events: null })
  if (token) tokens.set(token)
  const fake = fakeFetch(replies)
  const client = createApiClient({
    tokens,
    fetch: fake.fetch,
    baseUrl: () => ADMIN_BASE,
    retryDelays: [0, 0],
    ...extra,
  })
  return { client, tokens, calls: fake.calls }
}

const ok = z.object({ ok: z.literal(true) })
const UUID_V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/

async function rejection(promise: Promise<unknown>): Promise<ApiError> {
  try {
    await promise
  } catch (error) {
    if (error instanceof ApiError) return error
    throw error
  }
  throw new Error('expected the request to fail')
}

describe('relative paths under the admin prefix', () => {
  it('resolves v1/ paths against the entry page, keeping the nginx prefix and ignoring the hash', () => {
    expect(resolveApiUrl('v1/me', ADMIN_BASE).href).toBe('https://panel.example/__x7Kq9mZ2__/v1/me')
    expect(resolveApiUrl('v1/users/abc', 'https://panel.example/__x7Kq9mZ2__/#/users/abc?tab=orders').href).toBe(
      'https://panel.example/__x7Kq9mZ2__/v1/users/abc',
    )
    expect(resolveApiUrl('v1/plans', 'https://portal.example/').href).toBe('https://portal.example/v1/plans')
  })

  it('appends query params and skips null / undefined', () => {
    const url = resolveApiUrl('v1/users', ADMIN_BASE, { q: 'a b', limit: 20, active: true, cursor: undefined, group: null })
    expect(url.pathname).toBe('/__x7Kq9mZ2__/v1/users')
    expect(url.search).toBe('?q=a+b&limit=20&active=true')
  })

  it('refuses absolute, root-relative and parent paths that would escape the prefix', () => {
    for (const bad of ['/v1/me', 'https://evil.example/v1/me', '../v1/me', 'me', '//evil.example/v1', 'v1/../../v1/me', 'v1/%2e%2e/%2e%2e/x']) {
      expect(() => resolveApiUrl(bad, ADMIN_BASE)).toThrow(/relative/)
    }
  })

  it('the client sends to the prefixed URL with the Bearer token', async () => {
    const { client, calls } = setup([json(200, { ok: true })])
    await client.get('v1/me', ok)
    expect(calls[0]).toMatchObject({
      url: 'https://panel.example/__x7Kq9mZ2__/v1/me',
      method: 'GET',
      authorization: 'Bearer tok-1',
      accept: 'application/json',
      idempotencyKey: null,
    })
  })

  it('anonymous requests (login) carry no Authorization header', async () => {
    const { client, calls } = setup([json(200, { ok: true })])
    await client.post('v1/auth/login', ok, { auth: false, body: { email: 'a@b.c', password: 'x' } })
    expect(calls[0]!.authorization).toBeNull()
    expect(calls[0]!.body).toEqual({ email: 'a@b.c', password: 'x' })
  })
})

describe('responses and error envelope', () => {
  it('turns off zod JIT so the strict CSP never sees a new Function probe', () => {
    expect(z.config().jitless).toBe(true)
  })

  it('validates the body with zod and returns the parsed value', async () => {
    const { client } = setup([json(200, { user_id: 'u1', permissions: null, extra: 1 })])
    const me = await client.get('v1/me', z.object({ user_id: z.string(), permissions: z.array(z.string()).nullable() }))
    expect(me).toEqual({ user_id: 'u1', permissions: null })
  })

  it('a shape mismatch becomes invalid_response instead of leaking undefined into pages', async () => {
    const { client } = setup([json(200, { user_id: 42 })])
    const error = await rejection(client.get('v1/me', z.object({ user_id: z.string() })))
    expect(error.code).toBe('invalid_response')
    expect(error.cause).toBeDefined()
  })

  it('204 parses as undefined with noContent', async () => {
    const { client } = setup([new Response(null, { status: 204 })])
    await expect(client.post('v1/auth/logout', noContent)).resolves.toBeUndefined()
  })

  it('parses code, message, fields and request_id', async () => {
    const { client } = setup([
      envelope(422, 'validation_failed', '请求参数校验未通过', { fields: { password: '密码至少需要 8 个字符' }, request_id: 'req-9' }),
    ])
    const error = await rejection(client.post('v1/users/bulk/generate', ok, { body: {} }))
    expect(error).toMatchObject({ status: 422, code: 'validation_failed', message: '请求参数校验未通过', requestId: 'req-9' })
    expect(error.fields).toEqual({ password: '密码至少需要 8 个字符' })
  })

  it('a proxy error page without an envelope still maps by status', async () => {
    const { client } = setup([new Response('<html>502 Bad Gateway</html>', { status: 502 })])
    const error = await rejection(client.get('v1/overview', ok))
    expect(error).toMatchObject({ status: 502, code: 'service_unavailable' })
  })

  it('an unknown code keeps the server message and falls back to the status code', async () => {
    const { client } = setup([envelope(409, 'brand_new_code', '已被他人修改')])
    const error = await rejection(client.get('v1/overview', ok))
    expect(error).toMatchObject({ code: 'conflict', message: '已被他人修改' })
  })
})

describe('idempotency keys', () => {
  it('generates RFC 4122 v4 keys without crypto.randomUUID', () => {
    const key = newIdempotencyKey()
    expect(key).toMatch(UUID_V4)
    expect(newIdempotencyKey()).not.toBe(key)
    expect(newIdempotencyKey((b) => b.fill(0xff))).toBe('ffffffff-ffff-4fff-bfff-ffffffffffff')
  })

  it('reuses the same key across network retries of one call', async () => {
    const { client, calls } = setup([new TypeError('Failed to fetch'), new TypeError('Failed to fetch'), json(200, { ok: true })])
    await client.post('v1/users/u1/balance', ok, { idempotencyKey: true, body: { amount: 100 } })
    expect(calls).toHaveLength(3)
    const key = calls[0]!.idempotencyKey
    expect(key).toMatch(UUID_V4)
    expect(calls.map((c) => c.idempotencyKey)).toEqual([key, key, key])
    expect(calls.map((c) => c.body)).toEqual([{ amount: 100 }, { amount: 100 }, { amount: 100 }])
  })

  it('separate calls get separate keys; a caller-supplied key is sent as is', async () => {
    const { client, calls } = setup([json(200, { ok: true }), json(200, { ok: true }), json(200, { ok: true })])
    await client.post('v1/a', ok, { idempotencyKey: true })
    await client.post('v1/a', ok, { idempotencyKey: true })
    await client.post('v1/a', ok, { idempotencyKey: 'form-intent-1' })
    expect(calls[0]!.idempotencyKey).not.toBe(calls[1]!.idempotencyKey)
    expect(calls[2]!.idempotencyKey).toBe('form-intent-1')
  })

  it('never re-sends a write without a key after a network failure (it may already have been applied)', async () => {
    const { client, calls } = setup([new TypeError('Failed to fetch')])
    const error = await rejection(client.post('v1/auth/logout', noContent))
    expect(error).toMatchObject({ status: 0, code: 'network_error' })
    expect(calls).toHaveLength(1)
  })

  it('gives up after the configured retries', async () => {
    const { client, calls } = setup([new TypeError('x'), new TypeError('x'), new TypeError('x')])
    const error = await rejection(client.get('v1/overview', ok))
    expect(error.code).toBe('network_error')
    expect(calls).toHaveLength(3)
  })

  it('an abort is rethrown as is and never retried', async () => {
    const controller = new AbortController()
    const { client, calls } = setup([
      () => {
        controller.abort()
        return new DOMException('aborted', 'AbortError')
      },
    ])
    await expect(client.get('v1/overview', ok, { signal: controller.signal })).rejects.toMatchObject({ name: 'AbortError' })
    expect(calls).toHaveLength(1)
  })
})

describe('reauth_required', () => {
  function reauthSetup(replies: Reply[], answer: (client: ApiClient) => Promise<boolean>) {
    // 对话框要调 client.reauth，而 client 要在 requestReauth 之后才建好：经 env 延迟取
    const requestReauth = vi.fn(() => answer(env.client))
    const env = setup(replies, { requestReauth })
    return { ...env, requestReauth }
  }

  it('asks once, swaps in the new token, then replays with the same key and body', async () => {
    const env = reauthSetup(
      [reauthRequired(), json(200, { access_token: 'tok-2', token_type: 'Bearer', expires_in: 2592000 }), json(200, { ok: true })],
      async (client) => {
        await client.reauth('correct horse')
        return true
      },
    )
    await expect(
      env.client.post('v1/users/u1/status', ok, { idempotencyKey: true, body: { status: 'disabled' } }),
    ).resolves.toEqual({ ok: true })

    expect(env.requestReauth).toHaveBeenCalledTimes(1)
    const [first, reauth, replay] = env.calls
    expect(reauth).toMatchObject({ url: `${ADMIN_BASE}v1/auth/reauth`, method: 'POST', body: { password: 'correct horse' }, idempotencyKey: null })
    expect(first!.authorization).toBe('Bearer tok-1')
    expect(replay!.authorization).toBe('Bearer tok-2')
    expect(replay!.idempotencyKey).toMatch(UUID_V4)
    expect(replay!.idempotencyKey).toBe(first!.idempotencyKey)
    expect(replay!.body).toEqual(first!.body)
    expect(replay!.url).toBe(first!.url)
    expect(env.tokens.get()).toBe('tok-2')
  })

  it('replays requests without a key too: the gate runs before the handler', async () => {
    const env = reauthSetup([reauthRequired(), json(200, { ok: true })], async () => true)
    await env.client.get('v1/users/bulk/export', ok)
    expect(env.calls).toHaveLength(2)
  })

  it('a cancelled prompt surfaces the original reauth_required error and sends nothing more', async () => {
    const env = reauthSetup([reauthRequired()], async () => false)
    const error = await rejection(env.client.post('v1/users/u1/status', ok, { idempotencyKey: true, body: {} }))
    expect(error).toMatchObject({ status: 403, code: 'reauth_required' })
    expect(env.calls).toHaveLength(1)
    expect(env.tokens.get()).toBe('tok-1')
  })

  it('concurrent blocked requests share one prompt and all replay', async () => {
    let release!: () => void
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    const env = reauthSetup(
      [
        reauthRequired(),
        reauthRequired(),
        json(200, { access_token: 'tok-2', token_type: 'Bearer', expires_in: 1 }),
        json(200, { ok: true }),
        json(200, { ok: true }),
      ],
      async (client) => {
        await gate
        await client.reauth('pw')
        return true
      },
    )
    const a = env.client.post('v1/a', ok, { idempotencyKey: true, body: { n: 1 } })
    const b = env.client.post('v1/b', ok, { idempotencyKey: true, body: { n: 2 } })
    await vi.waitFor(() => expect(env.requestReauth).toHaveBeenCalledTimes(1))
    release()
    await expect(Promise.all([a, b])).resolves.toEqual([{ ok: true }, { ok: true }])
    expect(env.requestReauth).toHaveBeenCalledTimes(1)
    const replays = env.calls.slice(3)
    expect(replays.every((c) => c.authorization === 'Bearer tok-2')).toBe(true)
    const keyOf = (url: string) => env.calls.filter((c) => c.url.endsWith(url)).map((c) => c.idempotencyKey)
    expect(new Set(keyOf('v1/a')).size).toBe(1)
    expect(new Set(keyOf('v1/b')).size).toBe(1)
  })

  it('a request that comes back blocked after another one already re-verified replays without prompting', async () => {
    const env = setup(
      [
        (call) => {
          // 这条请求在途时，别处完成了重新验证、换了令牌
          env.tokens.set('tok-2')
          expect(call.authorization).toBe('Bearer tok-1')
          return reauthRequired()
        },
        json(200, { ok: true }),
      ],
      { requestReauth: vi.fn(async () => false) },
    )
    await env.client.post('v1/a', ok, { idempotencyKey: true })
    expect(env.calls[1]!.authorization).toBe('Bearer tok-2')
  })

  it('replays only once: a second reauth_required is thrown', async () => {
    const env = reauthSetup([reauthRequired(), reauthRequired()], async () => true)
    const error = await rejection(env.client.post('v1/a', ok, { idempotencyKey: true }))
    expect(error.code).toBe('reauth_required')
    expect(env.requestReauth).toHaveBeenCalledTimes(1)
  })

  it('a wrong password is a 401 for the dialog: no logout, token kept', async () => {
    const onUnauthorized = vi.fn()
    const env = setup([envelope(401, 'unauthorized', '密码不正确')], { onUnauthorized })
    const error = await rejection(env.client.reauth('wrong'))
    expect(error).toMatchObject({ status: 401, code: 'unauthorized', message: '密码不正确' })
    expect(env.tokens.get()).toBe('tok-1')
    expect(onUnauthorized).not.toHaveBeenCalled()
  })
})

describe('401 handling', () => {
  it('clears the token and reports once the session is gone', async () => {
    const onUnauthorized = vi.fn()
    const env = setup([envelope(401, 'unauthorized', '登录已失效')], { onUnauthorized })
    const error = await rejection(env.client.get('v1/me', ok))
    expect(isApiError(error, 'unauthorized')).toBe(true)
    expect(env.tokens.get()).toBeNull()
    expect(onUnauthorized).toHaveBeenCalledWith(error)
  })

  it('password-check endpoints keep the session on 401', async () => {
    const onUnauthorized = vi.fn()
    const env = setup([envelope(401, 'unauthorized', '当前密码不正确')], { onUnauthorized })
    await rejection(env.client.post('v1/me/password', ok, { passwordCheck: true, body: { old_password: 'a', new_password: 'b' } }))
    expect(env.tokens.get()).toBe('tok-1')
    expect(onUnauthorized).not.toHaveBeenCalled()
  })

  it('a late 401 for an old token does not wipe a newer login', async () => {
    const onUnauthorized = vi.fn()
    const env = setup(
      [
        () => {
          env.tokens.set('tok-new')
          return envelope(401, 'unauthorized')
        },
      ],
      { onUnauthorized },
    )
    await rejection(env.client.get('v1/me', ok))
    expect(env.tokens.get()).toBe('tok-new')
    expect(onUnauthorized).not.toHaveBeenCalled()
  })

  it('a 404 is not a logout (missing permission looks like not_found)', async () => {
    const env = setup([envelope(404, 'not_found', '资源不存在或无权访问')])
    const error = await rejection(env.client.get('v1/tickets', ok))
    expect(error.code).toBe('not_found')
    expect(env.tokens.get()).toBe('tok-1')
  })
})
