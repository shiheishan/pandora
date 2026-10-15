import { z } from 'zod'

// ---------------------------------------------------------------------------
// 订阅域名分组：schema 与纯函数。校验文案与后端（sd-spec「校验文案」）逐字一致。
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

/** GET v1/user-groups 里只取 id 与 name（zod 默认丢掉多余键） */
export const userGroupOptionsSchema = z.object({
  groups: z.array(z.object({ id: z.string(), name: z.string() })),
})
export type UserGroupOptions = z.output<typeof userGroupOptionsSchema>

// ---------------------------------------------------------------------------
// 表单（编辑稿）
// ---------------------------------------------------------------------------

export interface DomainRowForm {
  baseUrl: string
  weight: string
}

export interface GroupForm {
  /** 只给 React 列表用：已有组用 id，新组用 new-<递增数字> */
  key: string
  name: string
  isDefault: boolean
  domains: DomainRowForm[]
  userGroupIds: string[]
}

export function formFrom(cfg: DomainConfig): GroupForm[] {
  return cfg.groups.map((g) => ({
    key: g.id,
    name: g.name,
    isDefault: g.is_default,
    domains: g.domains.map((d) => ({ baseUrl: d.base_url, weight: String(d.weight) })),
    userGroupIds: [...g.user_group_ids],
  }))
}

/** 新建一组的初值：没有缺省组时它就是缺省；带一个空域名行（权重 1） */
export function newGroup(existing: GroupForm[]): GroupForm {
  let n = 0
  for (const g of existing) {
    const m = /^new-(\d+)$/.exec(g.key)
    if (m) n = Math.max(n, Number(m[1]))
  }
  return {
    key: `new-${n + 1}`,
    name: '',
    isDefault: !existing.some((g) => g.isDefault),
    domains: [{ baseUrl: '', weight: '1' }],
    userGroupIds: [],
  }
}

// ---------------------------------------------------------------------------
// 保存：前端能做的校验 + 请求体（整份替换，规范化与重复地址等交给后端）
// ---------------------------------------------------------------------------

export interface DomainGroupBody {
  name: string
  is_default: boolean
  domains: Array<{ base_url: string; weight: number }>
  user_group_ids: string[]
}

// 检查顺序与后端一致：组数 → 各组名称 → 缺省组 → 各组域名 → 用户组重复
export function bodyFrom(
  form: GroupForm[],
  limits: DomainConfig['limits'],
): { body: { groups: DomainGroupBody[] } } | { error: string } {
  if (form.length > limits.groups) return { error: `订阅域名组最多 ${limits.groups} 个` }

  const seenNames: string[] = []
  for (let i = 0; i < form.length; i++) {
    const name = form[i]!.name.trim()
    if (!name) return { error: `第 ${i + 1} 组：名称不能为空` }
    if ([...name].length > 40) return { error: `第 ${i + 1} 组：名称最多 40 个字` }
    const dupAt = seenNames.indexOf(name)
    if (dupAt >= 0) return { error: `第 ${i + 1} 组：名称和第 ${dupAt + 1} 组重复` }
    seenNames.push(name)
  }

  if (form.filter((g) => g.isDefault).length > 1) return { error: '只能有一个缺省组' }

  const assigned = new Set<string>()
  const out: DomainGroupBody[] = []
  for (let i = 0; i < form.length; i++) {
    const g = form[i]!
    // 全空（地址为空白）的域名行忽略不计
    const domains: Array<{ base_url: string; weight: number }> = []
    for (let k = 0; k < g.domains.length; k++) {
      const row = g.domains[k]!
      const baseUrl = row.baseUrl.trim()
      if (!baseUrl) continue
      if (!baseUrl.toLowerCase().startsWith('https://')) {
        return { error: `第 ${i + 1} 组第 ${k + 1} 个域名：必须以 https:// 开头` }
      }
      const weight = row.weight.trim()
      const n = Number(weight)
      if (!/^[0-9]+$/.test(weight) || n < 1 || n > limits.weight_max) {
        return { error: `第 ${i + 1} 组第 ${k + 1} 个域名：权重必须是 1–${limits.weight_max} 的整数` }
      }
      domains.push({ base_url: baseUrl, weight: n })
    }
    if (domains.length === 0) return { error: `第 ${i + 1} 组：至少要有一个域名` }
    if (domains.length > limits.domains_per_group) {
      return { error: `第 ${i + 1} 组：域名最多 ${limits.domains_per_group} 个` }
    }

    for (const id of g.userGroupIds) {
      if (assigned.has(id)) return { error: '同一个用户组只能分到一个域名组' }
      assigned.add(id)
    }

    out.push({ name: g.name.trim(), is_default: g.isDefault, domains, user_group_ids: [...g.userGroupIds] })
  }

  return { body: { groups: out } }
}

// ---------------------------------------------------------------------------
// 展示
// ---------------------------------------------------------------------------

/** 有效权重的说明文字；分数读不到时只说基础权重。数字按后端给的原样 */
export function effectiveText(
  d: { weight: number; score: number; effective_weight: number },
  scoresAvailable: boolean,
): string {
  return scoresAvailable
    ? `当前有效权重 ${d.effective_weight}（基础 ${d.weight} × 分数 ${d.score}）`
    : `当前按基础权重 ${d.weight}（暂时读不到分数）`
}
