import { formatMoney } from '../../../core/format'
import type { IconName } from '../../kit'
import { validityText } from '../common/catalog'
import { shortDateTime } from '../common/orders'
import { day, gb, money, periodLabel, type Naming } from '../common/purchase'
import type { Subscription } from '../common/subscriptions'
import { compactBytes } from '../common/traffic'
import type { GiftCard, PlacementOption, Redemption } from './api'

export const TOPUP_PRESETS = [50, 100, 200, 500] as const

/** 元 → 分；最多两位小数，¥1–¥50000（后端 100–5000000 分） */
export function parseTopupAmount(input: string): { cents: number } | { error: string } {
  const text = input.trim()
  if (!text) return { error: '请输入充值金额' }
  if (!/^\d+(\.\d{1,2})?$/.test(text)) return { error: '金额最多两位小数' }
  const cents = Math.round(Number(text) * 100)
  if (cents < 100) return { error: '最少充值 ¥1' }
  if (cents > 5_000_000) return { error: '单次最多充值 ¥50,000' }
  return { cents }
}

/** 契约门户-05 余额明细的 kind 映射；未知类型返回 null */
const LEDGER: Readonly<Record<string, string>> = {
  balance_topup: '充值',
  balance_hold: '下单用余额',
  plan_change_refund: '换套餐退回',
  balance_release: '订单取消退回',
  balance_adjusted: '人工调整',
  commission_to_balance: '佣金转入',
  gift_card_balance: '礼品卡',
  late_payment_applied: '挂账转入',
  late_payment_suspense: '挂账转入',
}
export const ledgerLabel = (kind: string): string | null => LEDGER[kind] ?? null

/**
 * 余额记录副行时间后面的固定短语（审计 #5）：明细每行只有 kind / delta / memo / at，memo 是后端写给对账看的
 * （「换套餐剩余价值退回余额」、原因码 user_cancelled、「礼品卡兑换：模板名」……），不露给用户；
 * 只有人工调整显示 memo（管理员填的原因）。没有短语的只写时间。
 */
const LEDGER_NOTE: Readonly<Record<string, string>> = {
  balance_hold: '结账时先用余额',
  balance_release: '订单没付成，余额退回',
  plan_change_refund: '差额退回',
  gift_card_balance: '兑换余额卡',
}

const LEDGER_ICON: Readonly<Record<string, IconName>> = {
  balance_topup: 'in',
  balance_hold: 'out',
  plan_change_refund: 'swap',
  balance_release: 'in',
  commission_to_balance: 'gift',
  gift_card_balance: 'card',
  late_payment_applied: 'in',
  late_payment_suspense: 'in',
}

export interface LedgerView {
  title: string
  /** 副行：「10-12 17:03 · 差额退回」 */
  sub: string
  icon: IconName
  /** 出账（delta < 0）用灰色圆标，入账用主题色圆标 */
  out: boolean
  /** 「+¥10.00」/「−¥20.00」 */
  amount: string
}

export function ledgerView(h: { kind: string; delta: number; memo: string; at: string }): LedgerView {
  const out = h.delta < 0
  const note = h.kind === 'balance_adjusted' ? h.memo.trim() : (LEDGER_NOTE[h.kind] ?? '')
  return {
    title: ledgerLabel(h.kind) ?? (out ? '余额支出' : '余额入账'),
    sub: [shortDateTime(h.at), note].filter(Boolean).join(' · '),
    icon: out ? 'out' : (LEDGER_ICON[h.kind] ?? 'in'),
    out,
    amount: `${out ? '−' : '+'}${formatMoney(Math.abs(h.delta))}`,
  }
}

/** 余额记录按月分组（浏览器本地时区，与订单页 groupByMonth 同口径），保持原有顺序；不算月度合计（只拿得到最近 100 条，合计会不准） */
export function ledgerByMonth<T extends { at: string }>(rows: readonly T[]): Array<{ key: string; label: string; rows: T[] }> {
  const groups: Array<{ key: string; label: string; rows: T[] }> = []
  for (const row of rows) {
    const d = new Date(row.at)
    const key = `${d.getFullYear()}-${d.getMonth() + 1}`
    let g = groups.find((x) => x.key === key)
    if (!g) {
      g = { key, label: `${d.getFullYear()} 年 ${d.getMonth() + 1} 月`, rows: [] }
      groups.push(g)
    }
    g.rows.push(row)
  }
  return groups
}

/** 卡码只收字母数字（服务端也转大写去空白）；输入里的空格与连字符之外的符号原样留给后端判 */
export const normalizeGiftCode = (raw: string) => raw.replace(/\s+/g, '').toUpperCase()

/** 卡面上方的小字（样稿 .gc .eyebrow「套餐卡」）：只有一种奖励时叫那种卡，几种混在一起叫「礼品卡」 */
export function giftKind(card: GiftCard): string {
  if (card.type === 'plan') return '套餐卡'
  if (card.type === 'mystery') return '盲盒'
  const r = card.rewards
  const kinds = [r.balance ? '余额卡' : '', r.traffic_bytes ? '送流量卡' : '', r.expire_days ? '加时长卡' : '', r.reset_quota ? '流量重置卡' : ''].filter(Boolean)
  return kinds.length === 1 ? kinds[0]! : '礼品卡'
}

/** 卡面大字：余额 / 流量 / 延期 / 套餐 / 盲盒 */
export function giftFace(card: GiftCard): string {
  if (card.type === 'mystery') {
    const labels = (card.rewards.pool ?? []).map((p) => p.label).filter(Boolean)
    return labels.length ? `盲盒：可能抽到 ${labels.join(' / ')}` : '盲盒'
  }
  if (card.type === 'plan') {
    const name = card.plan_name ?? card.name
    if (card.plan_kind === 'one_time') return `${name} · 一次性 · ${validityText(card.validity_days ?? null)}`
    const period = card.interval ? periodLabel(card.interval, card.interval_count ?? 1) : ''
    return [name, period].filter(Boolean).join(' · ')
  }
  const r = card.rewards
  const parts = [
    r.balance ? `${formatMoney(r.balance)} 余额` : '',
    r.traffic_bytes ? `流量 +${compactBytes(r.traffic_bytes)}` : '',
    r.expire_days ? `延长 ${r.expire_days} 天` : '',
    r.reset_quota ? '重置本期流量' : '',
  ].filter(Boolean)
  return parts.length ? parts.join(' + ') : card.name
}

/** 卡面小字：优先后台写的说明，缺省按类型给固定文案 */
export function giftNote(card: GiftCard): string {
  if (card.description.trim()) return card.description
  if (card.type === 'plan') return ''
  if (card.type === 'mystery') return '兑换时随机抽取其中一项'
  if (card.rewards.traffic_bytes) return '送的流量：按时间的套餐只在本期有效，一次性套餐加到总流量上'
  if (card.rewards.balance) return '兑换后直接计入余额'
  return '兑换后立即生效'
}

/** 我的礼品卡「获得」列：盲盒奖品名优先，其次余额 / 流量 / 延期，套餐卡用模板名 */
export function redemptionGain(r: Redemption): string {
  if (r.prize_label) return r.prize_label
  const parts = [r.balance ? `${formatMoney(r.balance)} 余额` : '', r.traffic_bytes ? `流量 +${compactBytes(r.traffic_bytes)}` : '', r.expire_days ? `延长 ${r.expire_days} 天` : ''].filter(Boolean)
  return parts.length ? parts.join(' + ') : r.template_name
}

// ---------------------------------------------------------------------------
// 兑换卡的用法（原型 redeemOptions / redeemSentence / redeemVerb）：选项与默认值由服务端给（设计稿 2.5），
// 这里只按选项写字：选项上的一句、选中后「会发生什么」、主按钮的动作。
// ---------------------------------------------------------------------------
const BADGES: Readonly<Record<string, string>> = { same_plan: '同款，最常见', soonest_expiry: '最快到期', most_used: '用得最多', least_remaining: '剩得最少' }

interface RedeemView {
  key: string
  label: string
  desc: string
  badge?: string
  sentence: string
  verb: string
}

interface RedeemCtx {
  card: GiftCard
  held: readonly Subscription[]
  naming: Naming
  /** 套餐月价（判断「升级成」还是「换成」）；拿不到时为 undefined */
  monthPrice: (planId: string | undefined) => number | undefined
  now?: Date
}

const cardTitle = (card: GiftCard) => {
  if (card.type !== 'plan') return card.name
  const name = card.plan_name ?? card.name
  if (card.plan_kind === 'one_time') return `一次性 · ${validityText(card.validity_days ?? null)} · ${name}`
  const period = card.interval ? periodLabel(card.interval, card.interval_count ?? 1) : ''
  return [name, period].filter(Boolean).join(' · ')
}

const endPhrase = (o: PlacementOption, to: string) => (o.new_permanent ? '永久有效' : to ? `到 ${to}` : '')

const stackDesc = (o: PlacementOption, to: string) => {
  const tail = o.new_permanent ? '' : to ? `，天数顺延到 ${to}` : ''
  return `总流量加上${tail}，链接不变`
}

export function redeemViews(c: RedeemCtx, options: readonly PlacementOption[], defaultKey: string): RedeemView[] {
  const { card, held, naming } = c
  const now = c.now ?? new Date()
  const m = naming.multi
  const np = card.plan_name ?? card.name
  const noDefault = defaultKey === ''
  const existing = held.length === 1 ? `你现在的${held[0]!.plan_name}` : '现有的几份'
  const extra = card.rewards.balance && card.type !== 'plan' ? `；余额 +${formatMoney(card.rewards.balance)} 直接进钱包` : ''
  return options.map((o) => {
    const sub = held.find((s) => s.id === o.subscription_id)
    const plan = o.plan_name ?? sub?.plan_name ?? ''
    const who = sub ? naming.who(sub) : `你的${plan}`
    const sn = sub ? naming.sn(sub) : plan
    const from = o.period_end ? day(o.period_end, now) : ''
    const to = o.new_period_end ? day(o.new_period_end, now) : ''
    const credit = o.credit ?? 0
    const badge = o.badge ? BADGES[o.badge] : undefined
    switch (o.kind) {
      case 'renew': {
        const expired = o.state === 'revivable'
        return {
          key: o.key,
          label: m ? `续到「${sn}」` : `续到你的${np}`,
          desc: expired ? (o.new_permanent ? '恢复使用，永久有效 · 链接不变' : `已过期，恢复使用到 ${to} · 链接不变`) : o.new_permanent ? '永久有效 · 链接不变' : `到期日 ${from} → ${to} · 链接不变`,
          badge,
          sentence: `${cardTitle(card)}加到${who}：${expired ? (o.new_permanent ? '恢复使用，永久有效' : `恢复使用，到 ${to}`) : o.new_permanent ? '永久有效' : `到期日 ${from} → ${to}`}。链接不变，设备不用重新添加。`,
          verb: o.new_permanent ? '兑换，续成永久' : `兑换，续到 ${to}`,
        }
      }
      case 'change': {
        if (o.expired) {
          const end = endPhrase(o, to)
          return {
            key: o.key,
            label: m ? `恢复「${sn}」，改成${np}` : `恢复已过期的${plan}，改成${np}`,
            desc: [end, '链接不变，设备不用重新添加'].filter(Boolean).join(' · '),
            badge,
            sentence: `${who}恢复使用并改成${np}${end ? `，${end}` : '，永久有效'}。链接不变，设备不用重新添加。`,
            verb: '兑换，恢复使用',
          }
        }
        const up = (c.monthPrice(card.rewards.plan_id) ?? 0) > (c.monthPrice(o.plan_id) ?? Number.MAX_SAFE_INTEGER)
        const back = credit > 0 ? `${plan}没用完的 ${money(credit)} 退到钱包余额` : ''
        // 只退已付价值，赠送的时长（加时长卡、套餐卡续的期）不保留（用户 10-08）：有就在选之前写明
        const gift = (o.gift_days_lost ?? 0) > 0 ? `赠送的 ${o.gift_days_lost} 天不保留` : ''
        const end = endPhrase(o, to)
        return {
          key: o.key,
          label: noDefault ? `把${m ? `「${sn}」` : '现在'}的${plan}${up ? '升级成' : '换成'}${np}` : m ? `换掉「${sn}」` : `把现在的${plan}换成${np}`,
          desc: [noDefault ? '' : end, '链接不变', back, gift].filter(Boolean).join(' · '),
          badge,
          sentence: `${who}今天换成${np}，从今天起算${end ? `，${end}` : '，永久有效'}。链接不变${back ? `；${plan}这期还没用完的部分按 ${money(credit)} 算给你，退到钱包余额（在「钱包」里，可用于续费、买套餐）` : ''}${gift ? `；${gift}` : ''}。`,
          verb: `兑换，换成${np}`,
        }
      }
      case 'stack': {
        const stackTo = o.new_permanent ? '' : to
        const tail = o.new_permanent ? '' : stackTo ? `，天数顺延到 ${stackTo}` : ''
        return {
          key: o.key,
          label: m ? `叠加到「${sn}」` : `叠加到${np}`,
          desc: stackDesc(o, stackTo),
          badge,
          sentence: `${cardTitle(card)}叠到${who}：总流量加上${tail}。链接不变，设备不用重新添加。`,
          verb: '兑换，叠加',
        }
      }
      case 'new': {
        const end = endPhrase(o, to)
        return {
          key: o.key,
          label: noDefault ? `另开一份${np}` : `再开一份${np}`,
          desc: noDefault ? '会得到新链接，要另外添加到 App' : [end, '会得到一个新链接，要另外添加到 App'].filter(Boolean).join(' · '),
          badge,
          sentence: `开一份新的${np}${end ? `，${end}` : o.new_permanent ? '，永久有效' : ''}。会得到一个新链接，要另外添加到 App${held.length ? `；${existing}不受影响` : ''}。`,
          verb: '兑换，开一份新的',
        }
      }
      case 'extend_days': {
        const days = card.rewards.expire_days ?? 0
        return {
          key: o.key,
          label: m && sub ? `「${naming.dn(sub)}」` : `你的${plan}`,
          desc: `到期日 ${from} → ${to}`,
          badge,
          sentence: card.type === 'mystery' ? `兑换后随机抽一项奖励，加到${who}。` : `${days ? `${days} 天` : '时长'}加到${who}：到期日 ${from} → ${to}。链接不变${extra}。`,
          verb: card.type === 'mystery' ? '兑换，抽一次' : `兑换，加 ${days} 天`,
        }
      }
      case 'reset_traffic': {
        const used = o.traffic_used ?? 0
        const cap = o.traffic_cap ?? 0
        return {
          key: o.key,
          label: m && sub ? `「${naming.dn(sub)}」` : `你的${plan}`,
          desc: `本期已用 ${gb(used)}${cap ? `（${Math.round((used / cap) * 100)}%）` : ''} → 0`,
          badge,
          sentence: card.type === 'mystery' ? `兑换后随机抽一项奖励，加到${who}。` : `${who}本期的流量清零重算：已用 ${gb(used)} → 0${cap ? `，又有 ${gb(cap)} 能用` : ''}。链接、到期日都不变${extra}。`,
          verb: card.type === 'mystery' ? '兑换，抽一次' : '兑换，流量清零重算',
        }
      }
      default: {
        const bytes = card.rewards.traffic_bytes ?? 0
        const left = Math.max(0, (o.traffic_cap ?? 0) - (o.traffic_used ?? 0))
        return {
          key: o.key,
          label: m && sub ? `「${naming.dn(sub)}」` : `你的${plan}`,
          desc: o.traffic_cap ? `剩 ${gb(left)}${bytes ? ` → ${gb(left + bytes)}` : ''}` : '',
          badge,
          sentence: card.type === 'mystery' ? `兑换后随机抽一项奖励，加到${who}。` : `${bytes ? gb(bytes) : '流量'}加到${who}${sub?.plan_kind === 'one_time' ? '的总流量上' : '本期的流量上，只在本期有效'}。链接不变${extra}。`,
          verb: card.type === 'mystery' ? '兑换，抽一次' : `兑换，加 ${gb(bytes)}`,
        }
      }
    }
  })
}

/** 这张卡没有可落的那一份时：加时长、重置、送流量都要先有在用的套餐（Go grant 的 needsSub），只有余额能直接兑 */
export function placementBlocked(card: GiftCard): boolean {
  const r = card.type === 'mystery' ? {} : card.rewards
  return card.type === 'plan' || Boolean(r.expire_days) || Boolean(r.reset_quota) || Boolean(r.traffic_bytes)
}

/** 纯余额、只送流量（没有可落的份）的卡：一句话与按钮 */
export function simpleRedeem(card: GiftCard): { sentence: string; verb: string } {
  const r = card.rewards
  if (card.type === 'mystery') return { sentence: '兑换后随机抽一项奖励。', verb: '兑换，抽一次' }
  if (r.traffic_bytes && !r.balance) return { sentence: `${gb(r.traffic_bytes)} 加到你在用的套餐上；按时间的套餐只在本期有效。`, verb: `兑换，加 ${gb(r.traffic_bytes)}` }
  if (r.balance) return { sentence: `${formatMoney(r.balance)} 直接进钱包余额，结账时自动先用。${r.traffic_bytes ? `另有 ${gb(r.traffic_bytes)} 流量加到你在用的套餐上。` : ''}`, verb: `兑换，余额 +${formatMoney(r.balance)}` }
  return { sentence: '兑换后马上生效。', verb: '兑换' }
}

/** 选中的用法：自己点过的优先，否则服务端的默认；默认为空（不同款套餐卡）时一个都不选，按钮置灰「先选一种用法」 */
export function selectedView(views: readonly RedeemView[], picked: string | null, defaultKey: string): RedeemView | null {
  return views.find((v) => v.key === (picked ?? defaultKey)) ?? null
}
