import { formatMoney } from '../../../core/format'
import { GB, formatGB, localInputToIso, minorToYuan, pad, parseCount, yuanToMinor, type Built, type FieldErrors, type Tone } from './logic'
import type { Batch, GiftTemplate, Plan, Rewards, Usage } from './schemas'

// ---------------------------------------------------------------------------
// 礼品卡
// ---------------------------------------------------------------------------
export const TEMPLATE_TYPES = [
  ['general', '通用卡'],
  ['plan', '套餐卡'],
  ['mystery', '盲盒'],
] as const

/** 模板卡片：面额、类型标签、「兑换后」说明（契约 GET v1/gift-cards 的设计映射） */
export function templateFace(t: Pick<GiftTemplate, 'type' | 'rewards'>, plans: readonly Plan[] | undefined): { face: string; kind: string; after: string } {
  const r = t.rewards
  if (t.type === 'plan') return { face: plans?.find((p) => p.id === r.plan_id)?.name ?? '套餐', kind: '套餐', after: '立即开通' }
  if (t.type === 'mystery') return { face: '盲盒', kind: `${r.pool?.length ?? 0} 个奖品`, after: '随机抽取' }
  if (r.balance) return { face: formatMoney(r.balance, 'CNY'), kind: '余额', after: '直接入账' }
  if (r.traffic_bytes) return { face: formatGB(r.traffic_bytes), kind: '流量', after: '当期有效' }
  if (r.expire_days) return { face: `${r.expire_days} 天`, kind: '延期', after: '顺延到期' }
  if (r.reset_quota) return { face: '重置', kind: '流量', after: '清零本期用量' }
  return { face: '—', kind: '通用', after: '—' }
}

/** 兑换得到的内容：「余额 +¥100.00 · 流量 +100 GB」；套餐卡 granted 为空用模板名，盲盒用 prize_label */
export function grantedLabel(u: Pick<Usage, 'granted' | 'template_name' | 'prize_label'>): string {
  const g = u.granted
  const parts = [
    g.balance ? `余额 +${formatMoney(g.balance, 'CNY')}` : '',
    g.traffic_bytes ? `流量 +${formatGB(g.traffic_bytes)}` : '',
    g.expire_days ? `+${g.expire_days} 天` : '',
    g.reset_quota ? '重置流量' : '',
  ].filter(Boolean)
  if (u.prize_label) return parts.length ? `${u.prize_label}（${parts.join(' · ')}）` : u.prize_label
  return parts.length ? parts.join(' · ') : u.template_name
}

/** 批次显示名：GB-MMDD-前 4 位，同一天的批次也能分开；CSV 文件名另用 id 前 8 位 */
export function batchLabel(b: Pick<Batch, 'id' | 'created_at'>): string {
  const t = new Date(b.created_at)
  return `GB-${pad(t.getMonth() + 1)}${pad(t.getDate())}-${b.id.slice(0, 4).toUpperCase()}`
}
export const batchFileName = (id: string) => `gift-codes-${id.slice(0, 8)}.csv`

/**
 * 卡码列表的筛选：列表与「导出当前筛选」（GET v1/gift-cards/codes/report，掩码报表）共用一份，
 * 空状态不进查询串——与后端 CodeFilter 同口径，导出的就是列表里看到的那些行
 */
export type CodeStatusFilter = '' | 'unused' | 'used' | 'disabled' | 'expired'
export function codeFilterQuery(batchId: string, status: CodeStatusFilter): { batch_id: string; status?: string } {
  return status ? { batch_id: batchId, status } : { batch_id: batchId }
}
/** 掩码报表的兜底文件名（服务端的 Content-Disposition 优先） */
export const codesReportFileName = (batchId: string) => `gift-codes-report-${batchId.slice(0, 8)}.csv`

export const CODE_STATUS: Readonly<Record<string, { label: string; tone: Tone }>> = {
  unused: { label: '可用', tone: 'ok' },
  used: { label: '已兑换', tone: 'neutral' },
  disabled: { label: '已停用', tone: 'danger' },
  expired: { label: '已过期', tone: 'warn' },
}

/** 兑换率 = used ÷ total，保留一位；total 为 0 时「—」 */
export function redeemRate(used: number, total: number): string {
  return total > 0 ? `${((used / total) * 100).toFixed(1)}%` : '—'
}

export interface PrizeForm {
  label: string
  weight: string
  balance: string
  trafficGb: string
  days: string
}

export interface TemplateForm {
  id: string | null
  type: GiftTemplate['type']
  name: string
  description: string
  status: GiftTemplate['status']
  balance: string
  trafficGb: string
  days: string
  resetQuota: boolean
  planId: string
  priceId: string
  pool: PrizeForm[]
  newUserOnly: boolean
  paidUserOnly: boolean
  requireInvite: boolean
  allowedPlanIds: string[]
  maxUsePerUser: string
  cooldownHours: string
  themeColor: string
}

const emptyPrize = (): PrizeForm => ({ label: '', weight: '1', balance: '', trafficGb: '', days: '' })

export function templateToForm(t: GiftTemplate | null): TemplateForm {
  const r: Rewards = t?.rewards ?? {}
  const yuan = (v?: number) => (v ? minorToYuan(v) : '')
  const gb = (v?: number) => (v ? String(Number((v / GB).toFixed(3))) : '')
  return {
    id: t?.id ?? null,
    type: t?.type ?? 'general',
    name: t?.name ?? '',
    description: t?.description ?? '',
    status: t?.status ?? 'active',
    balance: yuan(r.balance),
    trafficGb: gb(r.traffic_bytes),
    days: r.expire_days ? String(r.expire_days) : '',
    resetQuota: r.reset_quota ?? false,
    planId: r.plan_id ?? '',
    priceId: r.price_id ?? '',
    pool: r.pool?.length
      ? r.pool.map((p) => ({ label: p.label, weight: String(p.weight), balance: yuan(p.balance), trafficGb: gb(p.traffic_bytes), days: p.expire_days ? String(p.expire_days) : '' }))
      : [emptyPrize(), emptyPrize()],
    newUserOnly: t?.conditions.new_user_only ?? false,
    paidUserOnly: t?.conditions.paid_user_only ?? false,
    requireInvite: t?.conditions.require_invite ?? false,
    allowedPlanIds: t?.conditions.allowed_plan_ids ?? [],
    maxUsePerUser: t?.limits.max_use_per_user ? String(t.limits.max_use_per_user) : '',
    cooldownHours: t?.limits.cooldown_hours ? String(t.limits.cooldown_hours) : '',
    themeColor: t?.theme_color || '#b9442b',
  }
}
export { emptyPrize }

/** GB 输入（可带一位以上小数）→ 字节；空串 → 0，非法 → null */
function gbToBytes(input: string): number | null {
  const s = input.trim()
  if (s === '') return 0
  if (!/^\d+(\.\d+)?$/.test(s)) return null
  return Math.round(Number(s) * GB)
}

/** 表单 → POST v1/gift-cards 的请求体；错误键 name / rewards / conditions / limits 与后端 fields 同名 */
export function buildTemplateRequest(f: TemplateForm): Built<Record<string, unknown>> {
  const errors: FieldErrors = {}
  const name = f.name.trim()
  if (!name || [...name].length > 120) errors.name = '名称必填，不超过 120 字'

  const rewards: Record<string, unknown> = {}
  const money = (s: string) => (s.trim() ? yuanToMinor(s) : 0)
  const dayCount = (s: string) => {
    const n = parseCount(s)
    return n === null ? 0 : n
  }
  if (f.type === 'general') {
    const balance = money(f.balance)
    const traffic = gbToBytes(f.trafficGb)
    const expire = dayCount(f.days)
    if (balance === null || traffic === null || Number.isNaN(expire)) errors.rewards = '余额填金额、流量填 GB、天数填整数'
    else if (!balance && !traffic && !expire && !f.resetQuota) errors.rewards = '通用卡至少要送一样东西：余额、流量、延长到期或重置流量'
    else if (expire > 3650) errors.rewards = '延长天数不能超过 3650 天'
    else Object.assign(rewards, balance ? { balance } : {}, traffic ? { traffic_bytes: traffic } : {}, expire ? { expire_days: expire } : {}, f.resetQuota ? { reset_quota: true } : {})
  } else if (f.type === 'plan') {
    if (!f.planId || !f.priceId) errors.rewards = '套餐卡要选定套餐和价格'
    else Object.assign(rewards, { plan_id: f.planId, price_id: f.priceId })
  } else {
    if (f.pool.length < 2) errors.rewards = '盲盒至少要有 2 个奖品'
    else if (f.pool.length > 50) errors.rewards = '盲盒奖品不能超过 50 个'
    const pool = f.pool.map((p, i) => {
      const weight = parseCount(p.weight)
      const balance = money(p.balance)
      const traffic = gbToBytes(p.trafficGb)
      const expire = dayCount(p.days)
      if (!errors.rewards) {
        if (!p.label.trim()) errors.rewards = `第 ${i + 1} 个奖品缺少名称`
        else if (weight === null || Number.isNaN(weight) || weight < 1) errors.rewards = `第 ${i + 1} 个奖品的权重要填正整数`
        else if (balance === null || traffic === null || Number.isNaN(expire)) errors.rewards = `第 ${i + 1} 个奖品的数值格式不对`
        else if (!balance && !traffic && !expire) errors.rewards = `第 ${i + 1} 个奖品什么都不送`
      }
      return { label: p.label.trim(), weight: weight ?? 0, ...(balance ? { balance } : {}), ...(traffic ? { traffic_bytes: traffic } : {}), ...(expire ? { expire_days: expire } : {}) }
    })
    rewards.pool = pool
  }

  if (f.newUserOnly && f.paidUserOnly) errors.conditions = '「仅新用户」和「仅付费用户」不能同时勾选'
  const conditions: Record<string, unknown> = {
    ...(f.newUserOnly ? { new_user_only: true } : {}),
    ...(f.paidUserOnly ? { paid_user_only: true } : {}),
    ...(f.requireInvite ? { require_invite: true } : {}),
    ...(f.allowedPlanIds.length ? { allowed_plan_ids: f.allowedPlanIds } : {}),
  }
  const maxUse = parseCount(f.maxUsePerUser)
  const cooldown = parseCount(f.cooldownHours)
  if (Number.isNaN(maxUse) || Number.isNaN(cooldown)) errors.limits = '次数与冷却小时填非负整数，留空为不限'
  const limits: Record<string, unknown> = { ...(maxUse ? { max_use_per_user: maxUse } : {}), ...(cooldown ? { cooldown_hours: cooldown } : {}) }

  if (Object.keys(errors).length) return { ok: false, errors }
  return {
    ok: true,
    body: {
      ...(f.id ? { id: f.id } : {}),
      name,
      description: f.description.trim(),
      type: f.type,
      status: f.status,
      rewards,
      conditions,
      limits,
      theme_color: f.themeColor,
    },
  }
}

export interface GenerateForm {
  count: string
  prefix: string
  expiresAt: string
}

/** 表单 → POST v1/gift-cards/{id}/codes：数量 1..5000，前缀大写字母数字 ≤ 8，有效期不能在过去 */
export function buildGenerateRequest(f: GenerateForm): Built<{ count: number; prefix?: string; expires_at?: string }> {
  const errors: FieldErrors = {}
  const count = parseCount(f.count)
  if (count === null || Number.isNaN(count) || count < 1 || count > 5000) errors.count = '一次生成 1 到 5000 个'
  const prefix = f.prefix.trim().toUpperCase()
  if (!/^[A-Z0-9]{0,8}$/.test(prefix)) errors.prefix = '只能用大写字母和数字，最多 8 位'
  const iso = localInputToIso(f.expiresAt)
  if (f.expiresAt && !iso) errors.expires_at = '时间格式不正确'
  else if (iso && new Date(iso).getTime() <= Date.now()) errors.expires_at = '有效期不能设在过去'
  if (Object.keys(errors).length) return { ok: false, errors }
  return { ok: true, body: { count: count!, ...(prefix ? { prefix } : {}), ...(iso ? { expires_at: iso } : {}) } }
}
