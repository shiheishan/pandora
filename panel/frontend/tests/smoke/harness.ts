/**
 * [INPUT]: 依赖状态目录（环境变量 SMOKE_STATE）里 run-smoke-stack.sh 写的 smoke.env 与 seed.ts 写的 seed.json，依赖 ../../src/core/api 的 createApiClient / isApiError（页面用的同一个 HTTP 出口），依赖 zod 的 ZodError
 * [OUTPUT]: 对外提供 state（网关地址与种子 id）、adminClient / portalClient（已登录的页面 api 客户端）、Row 行类型与 checkRow（用页面 schema 解析真响应，失败时带 zod 问题与响应片段）、waitNonEmpty（等异步产生的数据）
 * [POS]: tests/smoke 的底座，admin.smoke.ts 与 portal.smoke.ts 只写接口表；解析走 createApiClient.get，与页面在浏览器里拿到响应后的处理一字不差
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { appendFileSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { ZodError, type ZodType } from 'zod'
import { beforeAll, describe, it } from 'vitest'
import { createApiClient, isApiError, type ApiClient, type QueryParams } from '../../src/core/api'
import type { TokenStore } from '../../src/core/token'

// ============================================================================
//  状态目录
// ============================================================================

const dir = process.env.SMOKE_STATE
if (!dir) throw new Error('SMOKE_STATE 未设置：先 run-smoke-stack.sh up 与 seed.ts')

function readEnvFile(path: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of readFileSync(path, 'utf8').split('\n')) {
    const m = /^([A-Z0-9_]+)=(.*)$/.exec(line)
    if (m) out[m[1]!] = m[2]!
  }
  return out
}

const env = readEnvFile(join(dir, 'smoke.env'))

export interface SeedState {
  portal: { email: string; password: string; user_id: string }
  invitee_id: string
  pool_id: string
  server_id: string
  node_id: string
  plan_id: string
  price_id: string
  order_id: string
  pending_order_id: string
  subscription_id: string
  ticket_id: string
  announcement_id: string
  page_slug: string
  gift_card_id: string
  coupon_id: string
}

export const state = {
  admin: env.SMOKE_ADMIN_BASE!,
  portal: env.SMOKE_PUBLIC_BASE!,
  adminEmail: env.SMOKE_ADMIN_EMAIL!,
  adminPassword: env.SMOKE_ADMIN_PASSWORD!,
  seed: JSON.parse(readFileSync(join(dir, 'seed.json'), 'utf8')) as SeedState,
}

// ============================================================================
//  客户端：页面的 createApiClient，令牌放内存，响应原文按 URL 留一份供报错引用
// ============================================================================

const lastBody = new Map<string, string>()

function memoryTokens(): TokenStore {
  let token: string | null = null
  return {
    get: () => token,
    set: (t) => (token = t),
    clear: () => (token = null),
    subscribe: () => () => {},
  }
}

// 后台 IP 限流每分钟 240 次写死在代码里：后台请求之间留 300ms（与 seed.ts 同一节奏）
let lastAdminCall = 0
async function paced(base: string): Promise<void> {
  if (base !== state.admin) return
  const wait = lastAdminCall + 300 - Date.now()
  if (wait > 0) await new Promise((r) => setTimeout(r, wait))
  lastAdminCall = Date.now()
}

async function login(base: string, email: string, password: string): Promise<string> {
  await paced(base)
  const res = await fetch(`${base}/v1/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email, password }),
  })
  if (res.status !== 200) throw new Error(`${base} 登录返回 ${res.status}：${(await res.text()).slice(0, 200)}`)
  return ((await res.json()) as { access_token: string }).access_token
}

// 每个身份登录一次；后台登录令牌自带 15 分钟 reauth 窗口，导出接口用得上
const tokens: Record<App, string> = {
  admin: await login(state.admin, state.adminEmail, state.adminPassword),
  portal: await login(state.portal, state.seed.portal.email, state.seed.portal.password),
}
export type App = 'admin' | 'portal'
const baseOf = (app: App): string => (app === 'admin' ? state.admin : state.portal)

/** 页面的客户端，令牌预先放进内存存储；fetch 包一层记下 JSON 响应原文（流式响应不读） */
function pageClient(app: App): ApiClient {
  const base = baseOf(app)
  const store = memoryTokens()
  store.set(tokens[app])
  return createApiClient({
    tokens: store,
    baseUrl: () => `${base}/`,
    retryDelays: [],
    fetch: async (input, init) => {
      await paced(base)
      const res = await fetch(input, init)
      if (res.headers.get('content-type')?.includes('json')) lastBody.set(String(input), await res.clone().text())
      return res
    },
  })
}

/** 不经 schema 取一个列表，只用来拿种子里没有的 id */
export async function rawGet(base: string, path: string): Promise<unknown> {
  await paced(base)
  const res = await fetch(`${base}/${path}`, { headers: { Authorization: `Bearer ${tokens[base === state.admin ? 'admin' : 'portal']}` } })
  return res.ok ? res.json() : {}
}

// ============================================================================
//  接口表的一行
// ============================================================================

export interface Row {
  /** 调用处，src 下的 file:line */
  at: string
  path: string
  query?: QueryParams
  /** 页面以 auth: false 调用（不带 Bearer） */
  auth?: false
  /** 页面在这个调用处用的 schema；raw / sse 行没有 */
  schema?: ZodType
  kind?: 'json' | 'raw' | 'sse'
  /** 给了就跳过，写原因 */
  skip?: string
  /** 数据由后台作业异步产生：轮询到它为真，超时就标跳过 */
  waitFor?: { ok: (data: unknown) => boolean; timeoutMs: number; why: string }
}

/** 按 zod 问题路径从原始响应里取片段，报告里「真实响应片段」一栏就用它 */
function excerpt(raw: string, error: ZodError): string {
  let body: unknown
  try {
    body = JSON.parse(raw)
  } catch {
    return `  (响应不是 JSON) ${raw.slice(0, 300)}`
  }
  return error.issues
    .slice(0, 8)
    .map((issue) => {
      const v = at(body, issue.path)
      const shown = v === undefined && issue.path.length > 0 ? `缺失（同级键：${Object.keys((at(body, issue.path.slice(0, -1)) as object | null) ?? {}).join(', ')}）` : JSON.stringify(v)?.slice(0, 200)
      return `  ${issue.path.join('.') || '(根)'}: ${issue.message} ← 实际 ${shown}`
    })
    .join('\n')
}

function at(body: unknown, path: readonly PropertyKey[]): unknown {
  let v: unknown = body
  for (const k of path) {
    if (v === null || typeof v !== 'object') return undefined
    v = (v as Record<PropertyKey, unknown>)[k]
  }
  return v
}

async function checkJson(api: ApiClient, app: App, row: Row): Promise<unknown> {
  try {
    return await api.get(row.path, row.schema!, { query: row.query, auth: row.auth })
  } catch (error) {
    if (isApiError(error) && error.cause instanceof ZodError) {
      const prefix = `${baseOf(app)}/${row.path}`
      const url = [...lastBody.keys()].reverse().find((u) => u.startsWith(prefix))
      throw new Error(`页面 schema 解析失败 GET ${row.path}（${row.at}）\n${excerpt(url ? lastBody.get(url)! : '', error.cause)}`, { cause: error })
    }
    if (isApiError(error)) throw new Error(`GET ${row.path}（${row.at}）返回 ${error.status} ${error.code}：${error.message}`, { cause: error })
    throw error
  }
}

async function checkRaw(api: ApiClient, row: Row): Promise<void> {
  const res = await api.requestRaw(row.path, { query: row.query })
  const type = res.headers.get('content-type') ?? ''
  await res.body?.cancel()
  if (!type.includes('text/csv')) throw new Error(`GET ${row.path}（${row.at}）内容类型是 ${type}，页面按 text/csv 下载`)
}

async function checkStream(api: ApiClient, row: Row): Promise<void> {
  const abort = new AbortController()
  const res = await api.openStream(row.path, abort.signal)
  const type = res.headers.get('content-type') ?? ''
  abort.abort()
  if (!type.includes('text/event-stream')) throw new Error(`GET ${row.path}（${row.at}）内容类型是 ${type}，页面按 text/event-stream 读`)
}

// ============================================================================
//  结果表：每行一条写进状态目录的 smoke-results.md，CI 贴进 job summary
// ============================================================================

const resultsFile = join(dir, 'smoke-results.md')

function record(app: App, row: Row, result: string, note = ''): void {
  const q = row.query ? `?${new URLSearchParams(Object.entries(row.query).map(([k, v]) => [k, String(v)])).toString()}` : ''
  const path = row.path.replace(/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/g, '{id}')
  appendFileSync(resultsFile, `| ${app} | \`${path}${q}\` | ${row.at} | ${result} | ${note.replace(/\n/g, '<br>').replace(/\|/g, '\\|')} |\n`)
}

/** 一个身份的整张表：每行一个用例，失败带 zod 问题与响应片段，跳过带原因 */
export function runTable(app: App, rows: Row[]): void {
  describe(`${app} GET 接口 × 页面 schema`, () => {
    let api: ApiClient
    beforeAll(() => {
      api = pageClient(app)
    })
    for (const row of rows) {
      const title = `${row.path.replace(/[0-9a-f-]{36}/g, '{id}')} ← ${row.at}`
      if (row.skip) {
        it.skip(`${title}（跳过：${row.skip}）`, () => {})
        record(app, row, '跳过', row.skip)
        continue
      }
      it(title, async (ctx) => {
        try {
          if (row.kind === 'raw') await checkRaw(api, row)
          else if (row.kind === 'sse') await checkStream(api, row)
          else if (row.waitFor) {
            const deadline = Date.now() + row.waitFor.timeoutMs
            while (!row.waitFor.ok(await checkJson(api, app, row))) {
              if (Date.now() > deadline) {
                record(app, row, '跳过', row.waitFor.why)
                ctx.skip()
                return
              }
              await new Promise((r) => setTimeout(r, 5000))
            }
          } else await checkJson(api, app, row)
        } catch (error) {
          record(app, row, '不一致', error instanceof Error ? error.message : String(error))
          throw error
        }
        record(app, row, row.kind === 'raw' ? '已验（CSV）' : row.kind === 'sse' ? '已验（事件流）' : '已验')
        // 要等异步数据的行，单条超时放到等待上限之外
      }, row.waitFor ? row.waitFor.timeoutMs + 30_000 : undefined)
    }
  })
}
