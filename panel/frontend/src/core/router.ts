/**
 * [INPUT]: 依赖 react 的 useMemo / useSyncExternalStore，依赖浏览器 window.location 与 hashchange 事件
 * [OUTPUT]: 对外提供 HashLocation、parseHash、href、navigate、matchPath、subscribeHash、useHashLocation
 * [POS]: core 的 hash 路由原语，两个入口的外框（第 ⑥ 步）与页面据它切页、拼链接；不含路由表，路由表属于各入口
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMemo, useSyncExternalStore } from 'react'

// ---------------------------------------------------------------------------
// 为什么是 hash：网关只下发 / 与 /assets/*，其它路径一律 404、不回退入口；
// public 网关根下还有订阅通配 /{prefix}/{token}，任何两段式路径都会撞上它。
// 所以页面地址全部在 # 之后：#/orders?status=paid、#/quick-login/<token>。
// 真相在 location.hash 上，不另存 React 状态。
// ---------------------------------------------------------------------------
export interface HashLocation {
  /** 以 / 开头、无尾斜杠（根除外）的路径，如 /users/abc。 */
  path: string
  query: URLSearchParams
}

function normalize(path: string): string {
  const withSlash = path.startsWith('/') ? path : `/${path}`
  return withSlash.length > 1 ? withSlash.replace(/\/+$/, '') || '/' : withSlash
}

export function parseHash(hash: string): HashLocation {
  const raw = hash.startsWith('#') ? hash.slice(1) : hash
  const q = raw.indexOf('?')
  const path = q < 0 ? raw : raw.slice(0, q)
  return { path: normalize(path), query: new URLSearchParams(q < 0 ? '' : raw.slice(q + 1)) }
}

export type RouteQuery = Record<string, string | number | boolean | null | undefined>

/** 拼 <a href>：href('/users/abc', { tab: 'orders' }) → '#/users/abc?tab=orders'。 */
export function href(path: string, query?: RouteQuery): string {
  const params = new URLSearchParams()
  for (const [name, value] of Object.entries(query ?? {})) {
    if (value !== undefined && value !== null && value !== '') params.set(name, String(value))
  }
  const search = params.toString()
  return `#${normalize(path)}${search ? `?${search}` : ''}`
}

/** 切页；replace=true 不留历史记录（登录后跳转、筛选条件回写地址）。两种方式都会触发 hashchange。 */
export function navigate(path: string, options: { query?: RouteQuery; replace?: boolean } = {}): void {
  const target = href(path, options.query)
  if (options.replace) window.location.replace(target)
  else window.location.hash = target
}

/**
 * 模式匹配：'/users/:id' 对 '/users/abc' 得 { id: 'abc' }；段数不同或字面段不等得 null。
 * 参数值已做 URI 解码，解码失败（畸形 %）按不匹配处理。
 */
export function matchPath(pattern: string, path: string): Record<string, string> | null {
  const want = normalize(pattern).split('/')
  const got = normalize(path).split('/')
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

export function subscribeHash(notify: () => void): () => void {
  window.addEventListener('hashchange', notify)
  return () => window.removeEventListener('hashchange', notify)
}

const readHash = () => window.location.hash

export function useHashLocation(): HashLocation {
  const hash = useSyncExternalStore(subscribeHash, readHash, () => '')
  return useMemo(() => parseHash(hash), [hash])
}
