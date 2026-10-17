import { z } from 'zod'

// ---------------------------------------------------------------------------
// 订阅域名：schema 与平铺列表用的纯函数（添加 / 移除仍走整份 PUT）。
// ---------------------------------------------------------------------------

/** GET / PUT v1/subscription-domains 的响应（PUT 回保存后的最新配置） */
export const domainConfigSchema = z.object({
  groups: z.array(
    z.object({
      id: z.string(),
      name: z.string(),
      is_default: z.boolean(),
      domains: z.array(
        z.object({
          base_url: z.string(),
          weight: z.number().int(),
          score: z.number(),
          effective_weight: z.number(),
        }),
      ),
      user_group_ids: z.array(z.string()),
    }),
  ),
  scores_available: z.boolean(),
  limits: z.object({
    groups: z.number().int(),
    domains_per_group: z.number().int(),
    weight_max: z.number().int(),
  }),
})
export type DomainConfig = z.output<typeof domainConfigSchema>

/** GET v1/user-groups 里只取 id、name、status（zod 默认丢掉多余键）；停用的组在勾选区隐藏，已勾选的照样显示 */
export const userGroupOptionsSchema = z.object({
  groups: z.array(z.object({ id: z.string(), name: z.string(), status: z.enum(['active', 'disabled']) })),
})
export type UserGroupOptions = z.output<typeof userGroupOptionsSchema>

export interface DomainGroupBody {
  name: string
  is_default: boolean
  domains: Array<{ base_url: string; weight: number }>
  user_group_ids: string[]
}

export interface DomainRow {
  groupId: string
  groupName: string
  host: string
  baseUrl: string
  scope: string
}

const HOST_RE = /^[a-z0-9-]+(\.[a-z0-9-]+)+$/
const IPV4_RE = /^(\d{1,3}\.){3}\d{1,3}$/

/** 由 base_url 取 host（去 https:// 与结尾 /） */
export function hostOf(baseUrl: string): string {
  return baseUrl.trim().toLowerCase().replace(/^https:\/\//, '').replace(/\/+$/, '')
}

/** 输入 → host；不合法返回 null */
export function parseHost(input: string): string | null {
  let s = input.trim().toLowerCase()
  s = s.replace(/^https:\/\//, '').replace(/\/+$/, '')
  if (s.includes(':') || s.includes('/') || s.includes('?') || s.includes('#')) return null
  if (!HOST_RE.test(s) || IPV4_RE.test(s)) return null
  return s
}

function userGroupNames(ids: string[], userGroups: ReadonlyArray<{ id: string; name: string }>): string {
  return ids.map((id) => userGroups.find((g) => g.id === id)?.name ?? '已删除的用户组').join('、')
}

/** 组的作用范围文案（与平铺列表一致） */
export function scopeForGroup(
  g: DomainConfig['groups'][number],
  allGroups: DomainConfig['groups'],
  userGroups: ReadonlyArray<{ id: string; name: string }>,
): string {
  const names = userGroupNames(g.user_group_ids, userGroups)
  const hasIds = g.user_group_ids.length > 0
  if (g.is_default) {
    if (!hasIds) return allGroups.length === 1 ? '所有用户' : '其余用户'
    return `${names}与其余用户`
  }
  if (!hasIds) return '未分配用户组（不会用到）'
  return `仅 ${names}`
}

/** 平铺：按组顺序展开，作用范围取所在组的用户组 */
export function flattenDomains(cfg: DomainConfig, userGroups: ReadonlyArray<{ id: string; name: string }>): DomainRow[] {
  const rows: DomainRow[] = []
  for (const g of cfg.groups) {
    const scope = scopeForGroup(g, cfg.groups, userGroups)
    for (const d of g.domains) {
      rows.push({ groupId: g.id, groupName: g.name, host: hostOf(d.base_url), baseUrl: d.base_url, scope })
    }
  }
  return rows
}

/** 现有配置 → PUT 体（原样带回 name / is_default / weight / user_group_ids） */
export function toBody(cfg: DomainConfig): { groups: DomainGroupBody[] } {
  return {
    groups: cfg.groups.map((g) => ({
      name: g.name,
      is_default: g.is_default,
      domains: g.domains.map((d) => ({ base_url: d.base_url, weight: d.weight })),
      user_group_ids: [...g.user_group_ids],
    })),
  }
}

function allHosts(cfg: DomainConfig): Set<string> {
  const set = new Set<string>()
  for (const g of cfg.groups) {
    for (const d of g.domains) set.add(hostOf(d.base_url))
  }
  return set
}

function uniqueDefaultName(existingNames: string[]): string {
  const used = new Set(existingNames)
  if (!used.has('缺省')) return '缺省'
  for (let n = 2; n < 1000; n++) {
    const name = `缺省 ${n}`
    if (!used.has(name)) return name
  }
  return '缺省'
}

/** 加一个域名；返回新 PUT 体或错误文案 */
export function addDomainBody(
  cfg: DomainConfig,
  input: string,
  groupId?: string,
): { body: { groups: DomainGroupBody[] }; host: string } | { error: string } {
  const host = parseHost(input)
  if (!host) return { error: '填一个域名，如 sub.example.com' }
  if (allHosts(cfg).has(host)) return { error: '这个域名已经在列表里' }

  const baseUrl = `https://${host}`
  const limits = cfg.limits
  const body = toBody(cfg)
  const targetId = groupId?.trim()
  if (targetId) {
    const idx = cfg.groups.findIndex((g) => g.id === targetId)
    if (idx >= 0) {
      const gName = cfg.groups[idx]!.name
      const g = body.groups[idx]!
      if (g.domains.length >= limits.domains_per_group) {
        return { error: `「${gName}」组最多 ${limits.domains_per_group} 个域名` }
      }
      g.domains.push({ base_url: baseUrl, weight: 1 })
      return { body, host }
    }
  }

  const defaultIdx = body.groups.findIndex((g) => g.is_default)

  if (defaultIdx >= 0) {
    const g = body.groups[defaultIdx]!
    if (g.domains.length >= limits.domains_per_group) {
      return { error: `缺省组最多 ${limits.domains_per_group} 个域名` }
    }
    g.domains.push({ base_url: baseUrl, weight: 1 })
    return { body, host }
  }

  if (body.groups.length >= limits.groups) {
    return { error: `订阅域名组最多 ${limits.groups} 个` }
  }
  const name = uniqueDefaultName(body.groups.map((g) => g.name))
  body.groups.push({ name, is_default: true, domains: [{ base_url: baseUrl, weight: 1 }], user_group_ids: [] })
  return { body, host }
}

/** 「作用范围」下拉：每个现有组一项，value 是组 id，缺省组 label 用组名，其他组 `${组名} · ${scopeForGroup(...)}`；没有缺省组时最前面加 { value: '', label: '缺省（新建）' } */
export function domainScopeOptions(
  cfg: DomainConfig,
  userGroups: ReadonlyArray<{ id: string; name: string }>,
): Array<{ value: string; label: string }> {
  const hasDefault = cfg.groups.some((g) => g.is_default)
  const opts = cfg.groups.map((g) => ({
    value: g.id,
    label: g.is_default ? g.name : `${g.name} · ${scopeForGroup(g, cfg.groups, userGroups)}`,
  }))
  return hasDefault ? opts : [{ value: '', label: '缺省（新建）' }, ...opts]
}

/** 下拉初值：缺省组的 id，没有缺省组为 '' */
export function defaultScopeValue(cfg: DomainConfig): string {
  return cfg.groups.find((g) => g.is_default)?.id ?? ''
}

/** 移除一个域名；dropsGroup 为真表示这是该组最后一个，组一起删 */
export function removeDomainBody(
  cfg: DomainConfig,
  groupId: string,
  baseUrl: string,
): { body: { groups: DomainGroupBody[] }; dropsGroup: boolean; groupName: string } {
  const src = cfg.groups.find((g) => g.id === groupId)
  const groupName = src?.name ?? ''
  const body = toBody(cfg)
  const idx = body.groups.findIndex((_g, i) => cfg.groups[i]?.id === groupId)
  if (idx < 0) return { body, dropsGroup: false, groupName }

  const g = body.groups[idx]!
  const nextDomains = g.domains.filter((d) => d.base_url !== baseUrl)
  const dropsGroup = nextDomains.length === 0
  if (dropsGroup) body.groups.splice(idx, 1)
  else g.domains = nextDomains
  return { body, dropsGroup, groupName }
}
