import { z } from 'zod'

// ---------------------------------------------------------------------------
// 溯源总览：schema 与 identity/handler 的 trace.go 逐键一致（全部字段必有）
// 纯逻辑，不依赖 React；页面组件（TraceTab）从这里取 schema 与文案函数。
// ---------------------------------------------------------------------------

export const TRACE_WINDOWS = [
  ['24h', '24 小时'],
  ['7d', '7 天'],
  ['30d', '30 天'],
] as const
export type TraceWindow = (typeof TRACE_WINDOWS)[number][0]

export const TRACE_VIEWS = [
  ['ips', 'IP 访问排行'],
  ['subscribers', '订阅更新排行'],
  ['accounts', '账号综合排行'],
] as const
export type TraceView = (typeof TRACE_VIEWS)[number][0]

/** 订阅更新排行里由后端排的列（sort 参数）；「地区数」在本页内排 */
export const SUB_SORTS = ['fetches', 'ips', 'ua_kinds', 'failures', 'last'] as const
export type SubSort = (typeof SUB_SORTS)[number]
/** 账号综合排行的排序（sort 参数），缺省 linked_ips */
export const ACC_SORTS = ['linked_ips', 'logins', 'login_ips', 'fetch_ips', 'shared_ips', 'ua_kinds', 'datacenter_hits'] as const
export type AccSort = (typeof ACC_SORTS)[number]

// ---------------------------------------------------------------------------
// IP 访问排行
// ---------------------------------------------------------------------------
const traceRegionSchema = z.object({
  region: z.string(),
  ips: z.number().int(),
  hits: z.number().int(),
})

const traceOverviewSchema = z.object({
  distinct_ips: z.number().int(),
  /** 某一路超过统计上限时为 true：独立 IP 数是估算值 */
  distinct_ips_approx: z.boolean(),
  /** 参与归属地统计的 IP 数（两路头部合并）；datacenter_ips 是其中的机房 IP 数 */
  analyzed_ips: z.number().int(),
  datacenter_ips: z.number().int(),
  fetches: z.number().int(),
  fetch_failures: z.number().int(),
  logins: z.number().int(),
  login_failures: z.number().int(),
  top_regions: z.array(traceRegionSchema),
})
export type TraceOverview = z.output<typeof traceOverviewSchema>

const traceUserSchema = z.object({
  id: z.string(),
  email: z.string(),
  status: z.string(),
})

const traceIPSchema = z.object({
  /** 「显示」完整地址用的令牌（只对签发它的管理员有效，2 小时内）；明文解不开时为空串 */
  token: z.string(),
  ip_masked: z.string(),
  geo: z.string(),
  network_kind: z.string(),
  /** 次数 = 拉取 + 行为事件 */
  hits: z.number().int(),
  fetches: z.number().int(),
  events: z.number().int(),
  accounts: z.number().int(),
  /** 任一路的关联账号超过明细上限 20 时为 true，accounts 是下限 */
  accounts_capped: z.boolean(),
  ua_kinds: z.number().int(),
  last_seen: z.string(),
  users: z.array(traceUserSchema),
})
export type TraceIP = z.output<typeof traceIPSchema>

export const traceIPsResponse = z.object({
  window: z.string(),
  since: z.string(),
  overview: traceOverviewSchema,
  ips: z.array(traceIPSchema),
})
export type TraceIPs = z.output<typeof traceIPsResponse>

// ---------------------------------------------------------------------------
// 订阅更新排行
// ---------------------------------------------------------------------------
const traceSubscriberSchema = z.object({
  user_id: z.string(),
  email: z.string(),
  status: z.string(),
  fetches: z.number().int(),
  failures: z.number().int(),
  ips: z.number().int(),
  ua_kinds: z.number().int(),
  regions: z.number().int(),
  families: z.array(z.string()),
  last_fetch: z.string(),
})
export type TraceSubscriber = z.output<typeof traceSubscriberSchema>

export const traceSubscribersResponse = z.object({
  window: z.string(),
  since: z.string(),
  sort: z.string(),
  subscribers: z.array(traceSubscriberSchema),
})

// ---------------------------------------------------------------------------
// 账号综合排行
// ---------------------------------------------------------------------------
const traceAccountSchema = z.object({
  user_id: z.string(),
  email: z.string(),
  status: z.string(),
  linked_ips: z.number().int(),
  logins: z.number().int(),
  login_ips: z.number().int(),
  fetch_ips: z.number().int(),
  shared_ips: z.number().int(),
  ua_kinds: z.number().int(),
  datacenter_hits: z.number().int(),
  last_seen: z.string(),
})
export type TraceAccount = z.output<typeof traceAccountSchema>

export const traceAccountsResponse = z.object({
  window: z.string(),
  since: z.string(),
  sort: z.string(),
  accounts: z.array(traceAccountSchema),
})

// ---------------------------------------------------------------------------
// 显示完整 IP（每次调用都写入审计，所以不缓存）
// ---------------------------------------------------------------------------
export const traceRevealResponse = z.object({
  ip: z.string(),
  geo: z.string(),
  network_kind: z.string(),
})
export type TraceReveal = z.output<typeof traceRevealResponse>

// ---------------------------------------------------------------------------
// 文案与排序（纯函数）
// ---------------------------------------------------------------------------

/** 账号显示名：已注销显示「已注销 #<id 前 8 位>」，否则邮箱（与 users/model.ts 的 userLabel 同口径） */
export function traceUserLabel(u: { id: string; email: string; status: string }): string {
  return u.status === 'deleted' ? `已注销 #${u.id.slice(0, 8)}` : u.email
}

/** 独立 IP 数：估算时加「约 」前缀 */
export function distinctIpText(o: TraceOverview): string {
  const n = o.distinct_ips.toLocaleString('zh-CN')
  return o.distinct_ips_approx ? `约 ${n}` : n
}

/** 机房 IP 占比：analyzed_ips 为 0 时「—」，否则一位小数、去掉 .0 */
export function datacenterShare(o: TraceOverview): string {
  if (o.analyzed_ips === 0) return '—'
  const pct = Math.round((o.datacenter_ips / o.analyzed_ips) * 1000) / 10
  return Number.isInteger(pct) ? `${pct}%` : `${pct.toFixed(1)}%`
}

/** 关联账号：满了明细上限时写「≥N」 */
export function accountsText(ip: TraceIP): string {
  return ip.accounts_capped ? `≥${ip.accounts}` : `${ip.accounts}`
}

const NETWORK_LABELS: Readonly<Record<string, string>> = {
  datacenter: '机房',
  residential: '家宽',
  mobile: '移动网络',
  education: '教育网',
  private: '内网',
  loopback: '本机',
}

/** 网络类型标签；其余取不到名字的返回 ''（不显示） */
export function networkLabel(kind: string): string {
  return NETWORK_LABELS[kind] ?? ''
}

/** 订阅更新排行：按「地区数」在本页内排（降序，再按 fetches 降序），不改原数组 */
export function sortByRegions(rows: readonly TraceSubscriber[]): TraceSubscriber[] {
  return [...rows].sort((a, b) => b.regions - a.regions || b.fetches - a.fetches)
}

/** 列表的 window 查询参数：24h 时不带（后端缺省 24h），其余带；sort 非空时带上 */
export function traceQuery(window: TraceWindow, sort?: string): Record<string, string> {
  const query: Record<string, string> = {}
  if (window !== '24h') query.window = window
  if (sort) query.sort = sort
  return query
}
