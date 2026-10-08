import { href } from '../../../core/router'
import { priceFor, type Pack, type Plan } from '../common/catalog'
import { day, devicesText, gb, leftOf, money, moneyShort, periodLabel, planTraffic, type Naming } from '../common/purchase'
import type { BalanceSplit, Quote, QuoteRow } from '../common/quote'
import { isLive, type Subscription } from '../common/subscriptions'
import { pickTrafficQuota } from '../common/traffic'
import type { Target } from './model'

// ---------------------------------------------------------------------------
// 确认页的文字（原型 order() 与 scrConfirm），只拼字，金额全取报价行。
// 叫法：套餐、一份、链接、App、添加；「原套餐没用完的部分」可展开「怎么算的」。
// ---------------------------------------------------------------------------
const DAY_MS = 86_400_000

export interface ConfirmCtx {
  target: Target
  /** 续费、换套餐、加流量作用的那一份 */
  sub?: Subscription
  /** 续费：这份的套餐；换套餐、新买：目标套餐 */
  plan?: Plan
  /** 换套餐：原来的套餐 */
  oldPlan?: Plan
  pack?: Pack
  row: QuoteRow
  naming: Naming
  held: readonly Subscription[]
  now: Date
}

export type Note = string | { b: string; rest: string }

export interface ConfirmCopy {
  title: string
  /** 主按钮的动作：「续费」「换成进阶版」「买一份新的」「加 100G」 */
  verb: string
  callout: string[]
  rows: Array<{ k: string; v: string; ok?: boolean; calc?: string }>
  notes: Note[]
  /** 换套餐选中档下面的一句完整算式 */
  formula?: string
  /** 绿色的退路一句（「换了以后想换回来也可以…」） */
  change?: string
  alt?: { text: string; href: string }
}

const pct = (x: number) => `${Math.round(x * 100)}%`
const expiredDays = (sub: Subscription, now: Date) => Math.max(1, Math.floor((now.getTime() - new Date(sub.current_period_end ?? now).getTime()) / DAY_MS))
const isExpired = (sub: Subscription | undefined) => sub !== undefined && !isLive(sub)
const tier = (row: QuoteRow) => periodLabel(row.interval, row.interval_count)
const coupon = (row: QuoteRow) => (row.discount > 0 ? [{ k: `优惠码${row.coupon ? ` ${row.coupon.code}` : ''}`, v: `−${money(row.discount)}`, ok: true }] : [])

/** 套餐每期流量；没有目录时按这一份的额度 */
function trafficOf(plan: Plan | undefined, sub: Subscription | undefined): string {
  if (plan) return planTraffic(plan)
  const limit = sub ? pickTrafficQuota(sub.quotas)?.limit : null
  return limit ? `每期 ${gb(limit)}` : '不限流量'
}

/** 换大还是换便宜：按月价比，没有月价按这一档与原来的价钱比 */
export function isUpgrade(plan: Plan | undefined, oldPlan: Plan | undefined, row: QuoteRow, sub: Subscription | undefined): boolean {
  const a = plan && priceFor(plan, '1m')?.unit_amount
  const b = oldPlan && priceFor(oldPlan, '1m')?.unit_amount
  if (a !== undefined && b !== undefined) return a > b
  return row.subtotal > (sub?.renewal_price?.unit_amount ?? 0)
}

/** 换套餐的算式：「¥42.00 − 原套餐没用完的 ¥24.00 = 今天付 ¥18.00」 */
export function changeFormula(row: QuoteRow): string {
  const minus = [row.discount > 0 ? ` − 优惠 ${money(row.discount)}` : '', row.credit > 0 ? ` − 原套餐没用完的 ${money(row.credit)}` : ''].join('')
  const result = row.total > 0 ? `今天付 ${money(row.total)}` : row.refund > 0 ? `今天不用付，退 ${money(row.refund)} 到钱包余额` : '今天不用付'
  return `${money(row.subtotal)}${minus} = ${result}`
}

/** 「买多久」每档下面的小字：续费、新买写到哪天；换套餐写算式（¥42 − ¥24 / = 付 ¥18） */
export function tierNote(target: Target, row: QuoteRow): [string, string?] {
  if (target.kind === 'change' && row.credit > 0) {
    return [`${moneyShort(row.subtotal)} − ${moneyShort(row.credit)}`, row.total > 0 ? `= 付 ${moneyShort(row.total)}` : `= 退 ${moneyShort(row.refund)}`]
  }
  return [row.period_end ? `到 ${day(row.period_end)}` : '']
}

export function confirmCopy(c: ConfirmCtx): ConfirmCopy {
  const { target, sub, plan, row, naming, now } = c
  const m = naming.multi
  if (target.kind === 'renew' && sub) {
    const expired = isExpired(sub)
    const end = row.period_end ? day(row.period_end, now) : ''
    const prev = sub.current_period_end ? day(sub.current_period_end, now) : ''
    const verb = expired ? '恢复使用' : '续费'
    const planLeft = pickTrafficQuota(sub.quotas)?.remaining
    return {
      title: verb,
      verb,
      callout: [expired ? `付款后马上恢复使用。从今天起算，到 ${end}；过期那 ${expiredDays(sub, now)} 天不补。链接不变，设备上不用重新添加。` : `到期日 ${prev} → ${end}。链接不变，现在用的设备不用重新添加。`],
      rows: [...(m ? [{ k: '给', v: naming.dn(sub) }] : []), { k: `${sub.plan_name} · ${tier(row)}`, v: money(row.subtotal) }, ...coupon(row), { k: '到期日', v: expired ? `今天起，到 ${end}` : `${prev} → ${end}` }],
      notes:
        !expired && planLeft !== null && planLeft !== undefined
          ? [`现在剩的 ${gb(planLeft)} 用到 ${prev}，到时不累加；${prev} 起新的一期，${trafficOf(plan, sub)}。${sub.pack_remaining_bytes > 0 ? '流量包不受影响，一直留着。' : ''}`]
          : [],
      alt: target.fromPlans ? { text: `要给别人另买一份${sub.plan_name}？`, href: href('/checkout', { new: sub.plan_id }) } : undefined,
    }
  }
  if (target.kind === 'change' && sub && plan) {
    const op = sub.plan_name
    const np = plan.name
    const end = row.period_end ? day(row.period_end, now) : ''
    const prev = sub.current_period_end ? day(sub.current_period_end, now) : ''
    const up = isUpgrade(plan, c.oldPlan, row, sub)
    const expired = isExpired(sub)
    const callout = expired
      ? [`恢复使用并换成${np}：链接不变，设备上不用重新添加。从今天起算，到 ${end}。`]
      : [
          up
            ? `现在就换成${np}：链接不变，不用重新添加。${op}今天停用。`
            : `现在就换成${np}：链接不变。每月流量 ${trafficOf(c.oldPlan, sub).replace(/^每\S+ /, '')} → ${trafficOf(plan, undefined).replace(/^每\S+ /, '')}、设备 ${devicesText(sub.device_limit)} → ${devicesText(plan.max_devices)}，今天生效。${row.refund > 0 ? `多出的 ${money(row.refund)} 退到钱包余额。` : ''}`,
          `从今天起重新算一期，到期 ${prev} → ${end}。`,
        ]
    const d = row.credit_detail
    const calc = d
      ? `${op}这期付了 ${money(d.paid)}。还剩 ${d.days_left}/${d.days_total} 天（${pct(d.days_total ? d.days_left / d.days_total : 0)}），流量还剩 ${gb(d.traffic_left)}/${gb(d.traffic_total)}（${pct(d.traffic_total ? d.traffic_left / d.traffic_total : 0)}），按少的那项算：${money(d.paid)} × ${pct(d.ratio_ppm / 1_000_000)} = ${money(row.credit)}。按下单那一刻算；送的天数和流量包不算钱。`
      : undefined
    const notes: Note[] = []
    if (row.credit > 0 && d) {
      notes.push({
        b: `${op}这期没用完的 ${gb(d.traffic_left)} 和 ${d.days_left} 天，已经按 ${money(row.credit)} 算给你了`,
        rest: `（先抵${np}的 ${money(row.subtotal - row.discount)}，${row.refund > 0 ? `多的 ${money(row.refund)} 退到钱包` : row.total > 0 ? `还需付 ${money(row.total)}` : '正好抵完'}）。从今天起按${np}（${trafficOf(plan, undefined)}）重新算。`,
      })
    }
    if (row.refund > 0) notes.push(`退回的 ${money(row.refund)} 在「钱包」里，可以用来续费、加流量、买套餐，不能提现。`)
    if (!up && d && d.days_total && d.traffic_total && d.traffic_left / d.traffic_total < d.days_left / d.days_total - 0.05) {
      notes.push(`你的流量用得比天数快，现在换只能算回 ${money(row.credit)}。不急的话，快到期时再换更划算。`)
    }
    return {
      title: `换成${np}`,
      verb: `换成${np}`,
      callout,
      formula: row.credit > 0 || row.discount > 0 ? changeFormula(row) : undefined,
      rows: [
        ...(m && !target.chooser ? [{ k: '换的是', v: naming.dn(sub) }] : []),
        { k: `${np} · ${tier(row)}`, v: money(row.subtotal) },
        ...coupon(row),
        ...(row.credit > 0 ? [{ k: '原套餐没用完的部分', v: `−${money(row.credit)}`, ok: true, calc }] : []),
        { k: '到期日', v: expired ? `今天起，到 ${end}` : `${prev} → ${end}` },
        ...(row.refund > 0 ? [{ k: '多出的退回余额', v: `+${money(row.refund)}`, ok: true }] : []),
      ],
      notes,
      change: '换了以后想换回来也可以，按同样的方法算钱。',
      alt: { text: expired ? `不想换？另外再买一份${np}` : `不想停用${op}？保留它，另外再买一份${np}`, href: href('/checkout', { new: plan.id }) },
    }
  }
  if (target.kind === 'new' && plan) {
    const end = row.period_end ? day(row.period_end, now) : ''
    const held = c.held
    const existing = held.length === 1 ? `你现在的${held[0]!.plan_name}` : `现有的 ${held.length} 份`
    return {
      title: held.length ? '再买一份' : `买${plan.name}`,
      verb: held.length ? '买一份新的' : `买${plan.name}`,
      callout: [held.length ? `会得到一个新链接，要在用它的设备上添加到 App。${existing}不受影响，各自到期、各自算流量。` : '会得到一个链接，在要用的设备上添加到 App 就能用。'],
      rows: [{ k: `${plan.name} · ${tier(row)}`, v: money(row.subtotal) }, ...coupon(row), { k: '到期日', v: `今天起，到 ${end}` }, { k: '流量', v: `${planTraffic(plan)}，从 0 开始` }],
      notes: [],
    }
  }
  if (target.kind === 'pack' && sub && c.pack) {
    const size = gb(c.pack.traffic_bytes)
    const { left } = leftOf(sub)
    return {
      title: '加流量',
      verb: `加 ${size}`,
      callout: [m ? `${size} 马上加到「${naming.dn(sub)}」，只给这份用，用完为止。链接不变，不用重新添加。` : `${size} 马上加到你的${sub.plan_name}，用完为止。链接不变，不用重新添加。`],
      rows: [...(m ? [{ k: '加到', v: naming.dn(sub) }] : []), { k: `流量包 ${size}`, v: money(row.subtotal) }, ...coupon(row), ...(left !== null ? [{ k: '加完能用', v: `${gb(left)} → ${gb(left + c.pack.traffic_bytes)}` }] : [])],
      notes: ['流量包不会过期，续费、换套餐后跟着这份走。'],
      change: m ? '加错了也不怕：这份以后彻底停用时，没用完的流量包可以转到另一份。' : undefined,
    }
  }
  return { title: '确认', verb: '确认', callout: [], rows: [], notes: [] }
}

// ---------------------------------------------------------------------------
// 付钱那一块：余额开关、合计、凑最低额的说明、主按钮
// ---------------------------------------------------------------------------
export interface PayCopy {
  /** 余额那一行；null = 不显示（不用付钱或没有余额） */
  balanceLine: { on: boolean; locked: boolean; text: string; strong?: string } | null
  sum: { text: string; strong?: string; after?: string }
  /** 凑最低额：紧挨金额、正常字色 */
  keptNote?: string
  /** 要选付款方式 */
  needsMethod: boolean
  button: string
  /** 应付低于支付最低额、余额又不够、这单又不能免（只有换套餐的零头能免）：在线付不了，先充值或用余额 */
  tooSmall: boolean
}

/** methods：付款方式的名字（「支付宝、微信支付」），写进最低额的说明 */
export function payCopy(quote: Quote, row: QuoteRow, split: BalanceSplit, useBalance: boolean, verb: string, methods: string): PayCopy {
  const balance = quote.balance
  const balanceLine =
    row.total > 0 && balance > 0
      ? row.with_balance.forced
        ? { on: true, locked: true, text: `这单只要 ${money(row.total)}，低于支付最低额，只能用余额付` }
        : useBalance
          ? { on: true, locked: false, strong: `已用余额 ${money(split.applied)}`, text: '（可关掉）' }
          : { on: false, locked: false, text: `用余额（有 ${money(balance)}，现在没用）` }
      : null
  let sum: PayCopy['sum']
  if (row.total === 0) sum = { text: '这次不用付钱', ...(row.refund > 0 ? { after: `，多出的 ${money(row.refund)} 退到钱包余额` } : {}) }
  // SmallDue 收窄（A 路审查）：换套餐抵扣后的零头免掉（waived>0）；新买、续费、流量包不免，报价带标记、下单 422。
  // 标记的字段名 A 还没定，先按「small_due 而没免」认，定了再换
  else if (split.small_due && split.waived > 0) sum = { text: `差价 ${money(split.waived)} 不到支付最低额，这次免了${split.applied > 0 ? `；余额抵 ${money(split.applied)}` : ''}` }
  else if (split.small_due) sum = { text: `这单要付 ${money(split.payable)}，低于支付最低额 ${money(quote.min_payment)}，在线付不了。先给钱包充值，或打开余额付` }
  else if (split.applied > 0 && split.payable > 0) sum = { text: `余额抵 ${money(split.applied)}，还需支付 `, strong: money(split.payable) }
  else if (split.applied > 0) sum = { text: '余额够付，', strong: money(split.applied), after: ' 全部用余额' }
  else sum = { text: '要付 ', strong: money(split.payable) }
  const tooSmall = split.small_due && split.waived === 0 && split.payable > 0
  const needsMethod = split.payable > 0 && !split.small_due
  const button = tooSmall ? '先充值或用余额付' : row.total === 0 || split.small_due ? verb : split.payable > 0 ? `${verb}，付 ${money(split.payable)}` : `${verb}，用余额付 ${money(split.applied)}`
  return {
    balanceLine,
    sum,
    keptNote: split.kept > 0 ? `${methods || '在线付款'}最低要付 ${money(quote.min_payment)}，所以这次余额只用 ${money(split.applied)}，剩下 ${money(split.kept)} 还在余额里。` : undefined,
    needsMethod,
    button,
    tooSmall,
  }
}
