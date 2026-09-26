/**
 * [INPUT]: 依赖 node:http 的 createServer，依赖 ../dev/mock-api 的 mockApi
 * [OUTPUT]: 对外提供 serve、close、loginAs、bearer、mockFetch 与 MockAccount 类型
 * [POS]: tests 下各假后端测试文件共用的辅助：把 mockApi 的中间件挂到真实的本地 HTTP 服务上（未匹配的非 API 路径回 418 代表「交给 vite」），登录拿令牌，按「方法 + 路径 + 可选请求体 + 可选幂等键」发请求。每个测试文件各起各的服务，模块假数据按文件隔离
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'
import { mockApi } from '../dev/mock-api'

type Middleware = (req: IncomingMessage, res: ServerResponse, next: () => void) => void

export interface MockAccount {
  email: string
  password: string
}

/** 起一个挂着假后端的本地服务；vite 的 next() 以 418 代替 */
export async function serve(app: 'admin' | 'portal'): Promise<{ server: Server; base: string }> {
  let middleware: Middleware | undefined
  const plugin = mockApi(app)
  const fakeVite = {
    middlewares: { use: (fn: Middleware) => (middleware = fn) },
    config: { logger: { info: () => {}, error: () => {} } },
  }
  ;(plugin.configureServer as (server: unknown) => void)(fakeVite)
  const server = createServer((req, res) =>
    middleware!(req, res, () => {
      res.statusCode = 418
      res.end()
    }),
  )
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  return { server, base: `http://127.0.0.1:${(server.address() as AddressInfo).port}` }
}

export function close(server: Server): Promise<void> {
  return new Promise<void>((resolve) => server.close(() => resolve()))
}

/** POST v1/auth/login，回登录响应（不做断言，要断言的用例自己看 status） */
export async function loginAs(base: string, account: MockAccount): Promise<{ access_token: string; permissions: string[] }> {
  const res = await fetch(`${base}/v1/auth/login`, { method: 'POST', body: JSON.stringify(account) })
  return (await res.json()) as { access_token: string; permissions: string[] }
}

export function bearer(token: string): Record<string, string> {
  return { Authorization: `Bearer ${token}` }
}

/** body 为 undefined 时不带请求体；key 给了就带 Idempotency-Key */
export function mockFetch(base: string, headers: Record<string, string>, method: string, path: string, body?: unknown, key?: string): Promise<Response> {
  return fetch(`${base}${path}`, { method, headers: { ...headers, ...(key ? { 'Idempotency-Key': key } : {}) }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) })
}
