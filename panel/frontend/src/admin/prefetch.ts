import { parseHash } from '../core/router'
import { resolveRoute, type ModuleKey } from './modules'

// ---------------------------------------------------------------------------
// 页面分包预取：外框要等 GET v1/me 回来、有了权限才渲染页面，页面的 lazy 分包这时才开始下，
// 刷新或打开深链就是「me → 分包」两个串行往返。入口一执行就按当前 hash 把对应分包
// import() 出来，和 me 并行；之后 screens/index.ts 的 lazy(() => import(...)) 拿到的是同一个
// 模块（浏览器按 URL 缓存模块），不会再下一遍。
// 这里与 screens/index.ts 各写一份 import()：那份是页面表本身，这份只管提前下载；
// Record<ModuleKey, …> 保证新增模块时两边都得补上。
// 只预取、不渲染，不绕过任何权限判断：没权限的页面下了也只会显示「无权限或不存在」。
// ---------------------------------------------------------------------------
export const SCREEN_CHUNKS: Readonly<Record<ModuleKey, () => Promise<unknown>>> = {
  dash: () => import('./screens/dash'),
  tickets: () => import('./screens/tickets'),
  users: () => import('./screens/users'),
  plans: () => import('./screens/plans'),
  billing: () => import('./screens/billing'),
  marketing: () => import('./screens/marketing'),
  nodes: () => import('./screens/nodes'),
  content: () => import('./screens/content'),
  system: () => import('./screens/system'),
  security: () => import('./screens/security'),
}

/** 按 hash 预取当前页面的分包，返回预取的模块。失败不打扰：真正渲染时 lazy 会再取一次并走错误边界。 */
export function prefetchScreenForHash(hash: string, chunks: Readonly<Record<ModuleKey, () => Promise<unknown>>> = SCREEN_CHUNKS): ModuleKey {
  const { module } = resolveRoute(parseHash(hash).path)
  chunks[module]().catch(() => undefined)
  return module
}
