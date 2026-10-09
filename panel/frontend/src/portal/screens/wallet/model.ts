import { formatMoney } from '../../../core/format'
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

/** 契约门户-05 余额明细的 kind 映射；未知类型返回 null，由调用方显示 memo */
const LEDGER: Readonly<Record<string, string>> = {
  balance_topup: '充值',
  balance_hold: '下单用余额（冻结）',
  plan_change_refund: '换套餐退回',
  balance_release: '订单取消退回',
  balance_adjusted: '人工调整',
  commission_to_balance: '佣金转入',
  gift_card_balance: '礼品卡',
  late_payment_applied: '挂账转入',
  late_payment_suspense: '挂账转入',
}
export const ledgerLabel = (kind: string): string | null => LEDGER[kind] ?? null

/** 卡码只收字母数字（服务端也转大写去空白）；输入里的空格与连字符之外的符号原样留给后端判 */
export const normalizeGiftCode = (raw: string) => raw.replace(/\s+/g, '').toUpperCase()

/** 卡面大字：余额 / 流量 / 延期 / 套餐 / 盲盒 */
export function giftFace(card: GiftCard): string {
  if (card.type === 'mystery') {
    const labels = (card.rewards.pool ?? []).map((p) => p.label).filter(Boolean)
    return labels.length ? `盲盒：可能抽到 ${labels.join(' / ')}` : '盲盒'
  }
  if (card.type === 'plan') return [card.plan_name ?? card.name, card.interval ? periodLabel(card.interval, card.interval_count ?? 1) : ''].filter(Boolean).join(' · ')
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
  if (card.rewards.traffic_bytes) return '流量包不过期，用完为止'
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
export const BADGES: Readonly<Record<string, string>> = { same_plan: '同款，最常见', soonest_expiry: '最快到期', most_used: '用得最多', least_remaining: '剩得最少' }

export interface RedeemView {
  key: string
  label: string
  desc: string
  badge?: string
  sentence: string
  verb: string
}

export interface RedeemCtx {
  card: GiftCard
  held: readonly Subscription[]
  naming: Naming
  /** 套餐月价（判断「升级成」还是「换成」）；拿不到时为 undefined */
  monthPrice: (planId: string | undefined) => number | undefined
  now?: Date
}

const cardTitle = (card: GiftCard) => (card.type === 'plan' ? [card.plan_name ?? card.name, card.interval ? periodLabel(card.interval, card.interval_count ?? 1) : ''].filter(Boolean).join(' · ') : card.name)

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
          desc: expired ? `已过期，恢复使用到 ${to} · 链接不变` : `到期日 ${from} → ${to} · 链接不变`,
          badge,
          sentence: `${cardTitle(card)}加到${who}：${expired ? `恢复使用，到 ${to}` : `到期日 ${from} → ${to}`}。链接不变，设备不用重新添加。`,
          verb: `兑换，续到 ${to}`,
        }
      }
      case 'change': {
        if (o.expired) {
          return {
            key: o.key,
            label: m ? `恢复「${sn}」，改成${np}` : `恢复已过期的${plan}，改成${np}`,
            desc: `到 ${to} · 链接不变，设备不用重新添加`,
            badge,
            sentence: `${who}恢复使用并改成${np}，到 ${to}。链接不变，设备不用重新添加。`,
            verb: '兑换，恢复使用',
          }
        }
        const up = (c.monthPrice(card.rewards.plan_id) ?? 0) > (c.monthPrice(o.plan_id) ?? Number.MAX_SAFE_INTEGER)
        const back = credit > 0 ? `${plan}没用完的 ${money(credit)} 退到钱包余额` : ''
        // 只退已付价值，赠送的时长（加时长卡、套餐卡续的期）不保留（用户 10-08）：有就在选之前写明
        const gift = (o.gift_days_lost ?? 0) > 0 ? `赠送的 ${o.gift_days_lost} 天不保留` : ''
        return {
          key: o.key,
          label: noDefault ? `把${m ? `「${sn}」` : '现在'}的${plan}${up ? '升级成' : '换成'}${np}` : m ? `换掉「${sn}」` : `把现在的${plan}换成${np}`,
          desc: [noDefault ? '' : `到 ${to}`, '链接不变', back, gift].filter(Boolean).join(' · '),
          badge,
          sentence: `${who}今天换成${np}，从今天起算，到 ${to}。链接不变${back ? `；${plan}这期还没用完的天数和流量按 ${money(credit)} 算给你，退到钱包余额（在「钱包」里，可用于续费、加流量、买套餐）` : ''}${gift ? `；${gift}` : ''}。`,
          verb: `兑换，换成${np}`,
        }
      }
      case 'new':
        return {
          key: o.key,
          label: noDefault ? `另开一份${np}` : `再开一份${np}`,
          desc: noDefault ? '会得到新链接，要另外添加到 App' : `到 ${to} · 会得到一个新链接，要另外添加到 App`,
          badge,
          sentence: `开一份新的${np}${to ? `，到 ${to}` : ''}。会得到一个新链接，要另外添加到 App${held.length ? `；${existing}不受影响` : ''}。`,
          verb: '兑换，开一份新的',
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
          desc: `这个月已用 ${gb(used)}${cap ? `（${Math.round((used / cap) * 100)}%）` : ''} → 0`,
          badge,
          sentence: card.type === 'mystery' ? `兑换后随机抽一项奖励，加到${who}。` : `${who}这个月的流量清零重算：已用 ${gb(used)} → 0${cap ? `，又有 ${gb(cap)} 能用` : ''}。链接、到期日都不变${extra}。`,
          verb: card.type === 'mystery' ? '兑换，抽一次' : '兑换，流量清零重算',
        }
      }
      default: {
        const bytes = card.rewards.traffic_bytes ?? 0
        const left = Math.max(0, (o.traffic_cap ?? 0) - (o.traffic_used ?? 0)) + (o.pack_remaining ?? 0)
        return {
          key: o.key,
          label: m && sub ? `「${naming.dn(sub)}」` : `你的${plan}`,
          desc: o.traffic_cap ? `剩 ${gb(left)}${bytes ? ` → ${gb(left + bytes)}` : ''}` : '',
          badge,
          sentence: card.type === 'mystery' ? `兑换后随机抽一项奖励，加到${who}。` : `${bytes ? gb(bytes) : '流量'}加到${who}，用完为止。链接不变${extra}。`,
          verb: card.type === 'mystery' ? '兑换，抽一次' : `兑换，加 ${gb(bytes)}`,
        }
      }
    }
  })
}

/** 这张卡没有可落的那一份时：送流量的先存着，加时长、重置要先有在用的套餐 */
export function placementBlocked(card: GiftCard): boolean {
  const r = card.type === 'mystery' ? {} : card.rewards
  return card.type === 'plan' || Boolean(r.expire_days) || Boolean(r.reset_quota)
}

/** 纯余额、只送流量（没有可落的份）的卡：一句话与按钮 */
export function simpleRedeem(card: GiftCard): { sentence: string; verb: string } {
  const r = card.rewards
  if (card.type === 'mystery') return { sentence: '兑换后随机抽一项奖励。', verb: '兑换，抽一次' }
  if (r.traffic_bytes && !r.balance) return { sentence: `${gb(r.traffic_bytes)} 流量包先存着，等你有在用的套餐时可以加上，不会过期。`, verb: `兑换，存下 ${gb(r.traffic_bytes)}` }
  if (r.balance) return { sentence: `${formatMoney(r.balance)} 直接进钱包余额，结账时自动先用。${r.traffic_bytes ? `另有 ${gb(r.traffic_bytes)} 流量包先存着。` : ''}`, verb: `兑换，余额 +${formatMoney(r.balance)}` }
  return { sentence: '兑换后马上生效。', verb: '兑换' }
}

/** 选中的用法：自己点过的优先，否则服务端的默认；默认为空（不同款套餐卡）时一个都不选，按钮置灰「先选一种用法」 */
export function selectedView(views: readonly RedeemView[], picked: string | null, defaultKey: string): RedeemView | null {
  return views.find((v) => v.key === (picked ?? defaultKey)) ?? null
}
