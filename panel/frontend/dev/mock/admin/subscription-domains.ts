import type { Json, MockContext, MockModule, MockResult } from '../types.ts'
import { GROUPS } from './users.ts'

// ---------------------------------------------------------------------------
// 订阅域名分组（GET/PUT /v1/subscription-domains），形状、错误码与文案照
// panel/internal/module/subscription 的 Go 实现写（见 .claude/briefs/sd-spec.md）。
// 整份替换：PUT 通过校验后重建全部组，新组 id 重新生成。
// ---------------------------------------------------------------------------

const err = (status: number, code: string, message: string, fields?: Record<string, string>): MockResult => ({ status, body: { error: { code, message, ...(fields ? { fields } : {}) } } })
const invalid = (fields: Record<string, string>) => err(422, 'validation_failed', '请求参数校验未通过', fields)
const reply = (ctx: MockContext, r: MockResult) => ctx.send(r.status, r.body)
const BAD_REQUEST = err(400, 'bad_request', '请求体不是合法的 JSON')
const badType = (key: string) => err(400, 'bad_request', `字段 "${key}" 类型不正确`)

const LIMITS = { groups: 20, domains_per_group: 10, weight_max: 100 } as const
const NAME_MAX = 40
const BASE_URL_MAX = 255

/** 固定的假分数：只有种子里的两个地址有分数，其余当 1 */
const SCORES: Readonly<Record<string, number>> = {
  'https://sub-a.example.com': 1.6,
  'https://sub-b.example.com': 0.4,
}

interface Domain {
  base_url: string
  weight: number
}
interface DomainGroup {
  id: string
  name: string
  is_default: boolean
  domains: Domain[]
  user_group_ids: string[]
}

const round2 = (v: number) => Math.round(v * 100) / 100
const UUID_RE = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/

// 种子：一个缺省组（分数让 sub-a 更受青睐）与一个分给 VIP 用户组的组
let groups: DomainGroup[] = [
  {
    id: 'd0a1b2c3-0000-4000-8000-000000000001',
    name: '缺省',
    is_default: true,
    domains: [
      { base_url: 'https://sub-a.example.com', weight: 2 },
      { base_url: 'https://sub-b.example.com', weight: 1 },
    ],
    user_group_ids: [],
  },
  {
    id: 'd0a1b2c3-0000-4000-8000-000000000002',
    name: 'VIP 专线',
    is_default: false,
    domains: [{ base_url: 'https://vip.example.com', weight: 1 }],
    user_group_ids: [GROUPS[0]!.id],
  },
]

/** 读的时候过滤掉已不在用户组表里的 id，模拟外键级联删除 */
const knownUserGroups = () => new Set(GROUPS.map((g) => g.id))

/** 响应形状照 Go 的 DomainConfigView：score / effective_weight 四舍五入到两位小数 */
function view() {
  return {
    groups: groups.map((g) => ({
      id: g.id,
      name: g.name,
      is_default: g.is_default,
      domains: g.domains.map((d) => {
        const score = round2(SCORES[d.base_url] ?? 1)
        return { base_url: d.base_url, weight: d.weight, score, effective_weight: round2(d.weight * score) }
      }),
      user_group_ids: g.user_group_ids.filter((id) => knownUserGroups().has(id)),
    })),
    scores_available: true,
    limits: LIMITS,
  }
}

type Normalized = { ok: true; url: string } | { ok: false; message: string }
const failURL = (message: string): Normalized => ({ ok: false, message })

/**
 * NormalizeBaseURL 的假实现，与 Go 逐条对应：去首尾空白、去末尾所有 /、
 * scheme 大小写不敏感但必须 https、host 转小写、只许 [a-z0-9.-] 且首尾是字母数字、
 * 端口 1–65535、不许路径/参数/片段/用户信息、长度 ≤ 255。
 * 不用 new URL()：WHATWG 对超范围端口直接抛错，而 Go 的 url.Parse 能解析出来再报端口。
 */
function normalizeBaseURL(raw: string): Normalized {
  const v = raw.trim().replace(/\/+$/, '')
  if (v.length > BASE_URL_MAX) return failURL('长度不能超过 255')
  // Go 的 url.Parse 拒绝控制字符
  if ([...v].some((c) => c.charCodeAt(0) < 0x20 || c.charCodeAt(0) === 0x7f)) return failURL('格式不正确')
  // scheme：有冒号时冒号前须是合法 scheme 形式，否则 url.Parse 失败；没有 scheme 时报 https
  const colon = v.indexOf(':')
  if (colon !== -1 && !/^[a-zA-Z][a-zA-Z0-9+.-]*$/.test(v.slice(0, colon))) return failURL('格式不正确')
  if (colon === -1 || v.slice(0, colon).toLowerCase() !== 'https') return failURL('必须以 https:// 开头')
  if (v.slice(colon + 1, colon + 3) !== '//') return failURL('必须包含域名')
  const rest = v.slice(colon + 3)
  const cut = (() => {
    for (let i = 0; i < rest.length; i++) {
      const ch = rest[i]
      if (ch === '/' || ch === '?' || ch === '#') return i
    }
    return rest.length
  })()
  const authority = rest.slice(0, cut)
  const tail = rest.slice(cut)
  // host:port 拆分（IPv6 字面量带方括号，端口跟在 ] 后面）
  let host = authority
  let port = ''
  const rb = authority.indexOf(']')
  if (rb === -1) {
    const c = authority.lastIndexOf(':')
    if (c !== -1) {
      host = authority.slice(0, c)
      port = authority.slice(c + 1)
      if (host.includes(':') || !/^[0-9]*$/.test(port)) return failURL('格式不正确')
    }
  } else {
    const c = authority.indexOf(':', rb)
    if (c !== -1) {
      host = authority.slice(0, c)
      port = authority.slice(c + 1)
      if (!/^[0-9]*$/.test(port)) return failURL('格式不正确')
    }
  }
  if (host === '') return failURL('必须包含域名')
  if (authority.includes('@')) return failURL('不能包含用户名或密码')
  if (tail !== '') return failURL('只能写到域名（可带端口），不能带路径、参数')
  const lower = host.toLowerCase()
  if (!/^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$/.test(lower)) {
    return failURL('域名只能含小写字母、数字、点和短横线（中文域名请写成 xn-- 开头的形式）')
  }
  if (port !== '') {
    const n = Number(port)
    if (n < 1 || n > 65535) return failURL('端口必须在 1–65535 之间')
  }
  return { ok: true, url: `https://${lower}${port === '' ? '' : ':' + port}` }
}

/** PUT 请求体：整份替换，顶层只许 groups（缺省或 null 当空） */
function decodeGroups(body: Json): { ok: true; list: RawGroup[] } | { ok: false; result: MockResult } {
  for (const k of Object.keys(body)) {
    if (k !== 'groups') return { ok: false, result: BAD_REQUEST }
  }
  const raw = body.groups
  if (raw !== undefined && raw !== null && !Array.isArray(raw)) return { ok: false, result: badType('groups') }
  const list: RawGroup[] = []
  for (const item of (raw ?? []) as Json[]) {
    if (item === null || typeof item !== 'object' || Array.isArray(item)) return { ok: false, result: badType('groups') }
    for (const k of Object.keys(item)) {
      if (k !== 'name' && k !== 'is_default' && k !== 'domains' && k !== 'user_group_ids') return { ok: false, result: BAD_REQUEST }
    }
    const g = item as Json
    if (g.name !== undefined && typeof g.name !== 'string') return { ok: false, result: badType('name') }
    if (g.is_default !== undefined && typeof g.is_default !== 'boolean') return { ok: false, result: badType('is_default') }
    if (g.user_group_ids !== undefined && (!Array.isArray(g.user_group_ids) || g.user_group_ids.some((x) => typeof x !== 'string'))) {
      return { ok: false, result: badType('user_group_ids') }
    }
    const rawDomains = g.domains
    if (rawDomains !== undefined && !Array.isArray(rawDomains)) return { ok: false, result: badType('domains') }
    const domains: Domain[] = []
    for (const d of (rawDomains ?? []) as Json[]) {
      if (d === null || typeof d !== 'object' || Array.isArray(d)) return { ok: false, result: badType('domains') }
      for (const k of Object.keys(d)) {
        if (k !== 'base_url' && k !== 'weight') return { ok: false, result: BAD_REQUEST }
      }
      const dd = d as Json
      // Go 里 weight 是 int：非整数在 JSON 解码阶段就报 400
      if (dd.weight !== undefined && (typeof dd.weight !== 'number' || !Number.isInteger(dd.weight))) return { ok: false, result: badType('weight') }
      if (dd.base_url !== undefined && typeof dd.base_url !== 'string') return { ok: false, result: badType('base_url') }
      domains.push({ base_url: typeof dd.base_url === 'string' ? dd.base_url : '', weight: typeof dd.weight === 'number' ? dd.weight : 0 })
    }
    list.push({
      name: typeof g.name === 'string' ? g.name : '',
      is_default: g.is_default === true,
      domains,
      user_group_ids: Array.isArray(g.user_group_ids) ? (g.user_group_ids as string[]) : [],
    })
  }
  return { ok: true, list }
}

interface RawGroup {
  name: string
  is_default: boolean
  domains: Domain[]
  user_group_ids: string[]
}

/**
 * 整份保存的校验，顺序与文案照 sd-spec「校验文案」，遇到第一个错误就返回；
 * 通过时顺带产出规范化结果（名称去空白、base_url 规范化、权重 0 当 1），
 * PUT 全部通过才用它替换状态，失败时不改任何状态。
 */
function validate(list: RawGroup[]): { ok: true; groups: Omit<DomainGroup, 'id'>[] } | { ok: false; fields: Record<string, string> } {
  if (list.length > LIMITS.groups) return { ok: false, fields: { groups: '订阅域名组最多 20 个' } }
  const names: string[] = []
  for (let i = 0; i < list.length; i++) {
    const name = list[i]!.name.trim()
    if (!name) return { ok: false, fields: { groups: `第 ${i + 1} 组：名称不能为空` } }
    if ([...name].length > NAME_MAX) return { ok: false, fields: { groups: `第 ${i + 1} 组：名称最多 40 个字` } }
    const j = names.indexOf(name)
    if (j !== -1) return { ok: false, fields: { groups: `第 ${i + 1} 组：名称和第 ${j + 1} 组重复` } }
    names.push(name)
  }
  if (list.filter((g) => g.is_default).length > 1) return { ok: false, fields: { groups: '只能有一个缺省组' } }
  for (let i = 0; i < list.length; i++) {
    const domains = list[i]!.domains
    if (domains.length === 0) return { ok: false, fields: { groups: `第 ${i + 1} 组：至少要有一个域名` } }
    if (domains.length > LIMITS.domains_per_group) return { ok: false, fields: { groups: `第 ${i + 1} 组：域名最多 10 个` } }
    const seen: string[] = []
    for (let k = 0; k < domains.length; k++) {
      const norm = normalizeBaseURL(domains[k]!.base_url)
      if (!norm.ok) return { ok: false, fields: { groups: `第 ${i + 1} 组第 ${k + 1} 个域名：${norm.message}` } }
      if (seen.includes(norm.url)) return { ok: false, fields: { groups: `第 ${i + 1} 组：${norm.url} 重复了` } }
      seen.push(norm.url)
      const w = domains[k]!.weight
      if (w < 0 || w > LIMITS.weight_max) return { ok: false, fields: { groups: `第 ${i + 1} 组第 ${k + 1} 个域名：权重必须是 1–100 的整数` } }
    }
  }
  const assigned = new Set<string>()
  for (const g of list) {
    for (const id of g.user_group_ids) {
      if (!UUID_RE.test(id)) return { ok: false, fields: { groups: '用户组 id 格式不正确' } }
      if (assigned.has(id)) return { ok: false, fields: { groups: '同一个用户组只能分到一个域名组' } }
      assigned.add(id)
    }
  }
  const known = knownUserGroups()
  for (const id of assigned) {
    if (!known.has(id)) return { ok: false, fields: { groups: '有用户组不存在（可能刚被删除），刷新后重试' } }
  }
  return {
    ok: true,
    groups: list.map((g, i) => ({
      name: names[i]!,
      is_default: g.is_default,
      domains: g.domains.map((dom) => ({
        // 循环里已逐个规范化通过，这里不会失败
        base_url: (normalizeBaseURL(dom.base_url) as { ok: true; url: string }).url,
        weight: dom.weight === 0 ? 1 : dom.weight,
      })),
      user_group_ids: [...g.user_group_ids],
    })),
  }
}

export const subscriptionDomainsModule: MockModule = {
  routes: {
    'GET /v1/subscription-domains': (ctx) => {
      ctx.send(200, view())
    },
    'PUT /v1/subscription-domains': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return reply(ctx, BAD_REQUEST)
      const d = decodeGroups(body)
      if (!d.ok) return reply(ctx, d.result)
      const v = validate(d.list)
      if (!v.ok) return reply(ctx, invalid(v.fields))
      // 整份替换：全部通过才落库，新组 id 重新生成
      groups = v.groups.map((g) => ({ id: crypto.randomUUID(), ...g }))
      ctx.send(200, view())
    },
  },
}
