import { href } from '../../../core/router'
import { planTraffic, priceFor, type Plan } from '../common/catalog'
import { day, devicesText, gb, leftOf, money, moneyShort, periodLabel, type Naming } from '../common/purchase'
import type { BalanceSplit, Quote, QuoteRow } from '../common/quote'
import { isLive, type Subscription } from '../common/subscriptions'
import { pickTrafficQuota } from '../common/traffic'
import { expiryText, titleOf } from '../common/card-text'
import type { Target } from './model'

// ---------------------------------------------------------------------------
// 确认页的文字（原型 order() 与 scrConfirm），只拼字，金额全取报价行。
// 叫法：套餐、一份、链接、App、添加；「原套餐没用完的部分」可展开「怎么算的」。
// ---------------------------------------------------------------------------
const DAY_MS = 86_400_000

interface ConfirmCtx {
  target: Target
  /** 续费、换套餐、叠加作用的那一份 */
  sub?: Subscription
  /** 续费：这份的套餐；换套餐、新买、叠加：目标套餐 */
  plan?: Plan
  /** 换套餐：原来的套餐 */
  oldPlan?: Plan
  row: QuoteRow
  naming: Naming
  held: readonly Subscription[]
  now: Date
}

export type Note = string | { b: string; rest: string }

/** 左栏顶上那张小卡（样稿 .ck-item）：贴纸名、一行事实、右侧灰字 */
export interface ConfirmItem {
  name: string
  facts: string
  pill?: string
}

interface ConfirmCopy {
  title: string
  /** 续费、新买、叠加、重置：左栏顶上的小卡 */
  item?: ConfirmItem
  /** 换套餐：「现在 → 换成」两格（样稿 .ck-swap） */
  swap?: { from: ConfirmItem; to: ConfirmItem }
  /** 主按钮的动作：「续费」「换成进阶版」「买一份新的」「叠加」 */
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

/** 哨兵远期日期不显示，显示「永久」 */
const showEnd = (iso: string | null | undefined, now: Date) => {
  if (!iso) return ''
  const y = new Date(iso).getUTCFullYear()
  if (y >= 2090) return '永久'
  return day(iso, now)
}

/** 套餐每期流量；没有目录时按这一份的额度 */
function trafficOf(plan: Plan | undefined, sub: Subscription | undefined): string {
  if (plan) return planTraffic(plan)
  const limit = sub ? pickTrafficQuota(sub.quotas)?.limit : null
  return limit ? `每期 ${gb(limit)}` : '不限流量'
}

/** 「3 台设备」「设备不限台数」 */
const devicesFact = (n: number | null) => (n === null ? '设备不限台数' : `${devicesText(n)}设备`)

/** 流量那一格去掉「每期 / 共」前缀后的量：「100G」「不限」 */
const amountOf = (traffic: string) => traffic.replace(/^(每期|共) /, '').replace('不限流量', '不限')

/** 换套餐的流量一句（审计 #15：按时间的套餐说「每期」，不说「每月」）：「每期流量 500G → 300G」；跨类型「流量 每期 100G → 共 50G」 */
function trafficShift(from: string, to: string): string {
  const per = (t: string) => t.startsWith('每期 ') || t === '不限流量'
  return per(from) && per(to) ? `每期流量 ${amountOf(from)} → ${amountOf(to)}` : `流量 ${from} → ${to}`
}

/** 换大还是换便宜：按月价比，没有月价按这一档与原来的价钱比 */
function isUpgrade(plan: Plan | undefined, oldPlan: Plan | undefined, row: QuoteRow, sub: Subscription | undefined): boolean {
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

/** 一次性套餐的有效期行文案 */
function validityRow(row: QuoteRow, now: Date): { k: string; v: string } {
  if (row.validity_days === null) return { k: '有效期', v: '永久有效' }
  const end = showEnd(row.period_end, now)
  return { k: '有效期', v: `今天起 ${row.validity_days} 天，到 ${end}` }
}

/** 「买多久」每档下面的小字：续费、新买写到哪天；换套餐写算式（¥42 − ¥24 / = 付 ¥18） */
export function tierNote(target: Target, row: QuoteRow): [string, string?] {
  if (target.kind === 'change' && row.credit > 0) {
    return [`${moneyShort(row.subtotal)} − ${moneyShort(row.credit)}`, row.total > 0 ? `= 付 ${moneyShort(row.total)}` : `= 退 ${moneyShort(row.refund)}`]
  }
  if (row.period_end) {
    const end = showEnd(row.period_end, new Date())
    return [end === '永久' ? '永久' : `到 ${end}`]
  }
  return ['']
}

export function confirmCopy(c: ConfirmCtx): ConfirmCopy {
  const { target, sub, plan, row, naming, now } = c
  const m = naming.multi
  if (target.kind === 'renew' && sub) {
    const expired = isExpired(sub)
    const end = row.period_end ? showEnd(row.period_end, now) : ''
    const prev = sub.current_period_end ? day(sub.current_period_end, now) : ''
    const verb = expired ? '恢复使用' : '续费'
    const planLeft = pickTrafficQuota(sub.quotas)?.remaining
    return {
      title: verb,
      verb,
      item: { name: titleOf(sub), facts: `${sub.label ? `${sub.plan_name} · ` : ''}${trafficOf(plan, sub)} · ${devicesFact(sub.device_limit)}`, pill: expired ? '已到期' : expiryText(sub, now).main },
      callout: [expired ? `付款后马上恢复使用。从今天起算，到 ${end}；过期那 ${expiredDays(sub, now)} 天不补。链接不变，设备上不用重新添加。` : `到期日 ${prev} → ${end}。链接不变，现在用的设备不用重新添加。`],
      rows: [...(m ? [{ k: '给', v: naming.dn(sub) }] : []), { k: `${sub.plan_name} · ${tier(row)}`, v: money(row.subtotal) }, ...coupon(row), { k: '到期日', v: expired ? `今天起，到 ${end}` : `${prev} → ${end}` }],
      notes:
        !expired && planLeft !== null && planLeft !== undefined
          ? [`现在剩的 ${gb(planLeft)} 用到 ${prev}，到时不累加；${prev} 起新的一期，${trafficOf(plan, sub)}。`]
          : [],
      alt: target.fromPlans ? { text: `要给别人另买一份${sub.plan_name}？`, href: href('/checkout', { new: sub.plan_id }) } : undefined,
    }
  }
  if (target.kind === 'stack' && sub && plan) {
    const who = naming.who(sub)
    const { left } = leftOf(sub)
    const add = plan.traffic_bytes
    const trafficV =
      add === null
        ? '不限流量'
        : left === null
          ? `加 ${gb(add)}`
          : `剩 ${gb(left)} → 加完 ${gb(left + add)}`
    const prevEnd = sub.current_period_end ? day(sub.current_period_end, now) : ''
    const validityV =
      sub.permanent || row.validity_days === null
        ? '永久'
        : row.period_end
          ? `${prevEnd} → ${showEnd(row.period_end, now)}`
          : prevEnd
    return {
      title: `叠加到${who}`,
      verb: '叠加',
      item: {
        name: titleOf(sub),
        facts: `一次性 · ${sub.permanent || row.validity_days === null ? '永久有效' : `有效 ${row.validity_days} 天`} · ${devicesFact(sub.device_limit)}`,
        pill: left === null ? '不限流量' : `剩 ${gb(left)}`,
      },
      callout: [`叠加后还是原来那个链接，不用重新添加。`],
      rows: [
        ...(m ? [{ k: '叠加到', v: naming.dn(sub) }] : []),
        { k: `${plan.name} · 一次性`, v: money(row.subtotal) },
        ...coupon(row),
        { k: '总流量', v: trafficV },
        { k: '有效期', v: validityV },
      ],
      notes: [],
    }
  }
  if (target.kind === 'change' && sub && plan) {
    const op = sub.plan_name
    const np = plan.name
    const end = row.period_end ? showEnd(row.period_end, now) : ''
    const prev = sub.current_period_end ? day(sub.current_period_end, now) : ''
    const up = isUpgrade(plan, c.oldPlan, row, sub)
    const expired = isExpired(sub)
    const onceTarget = plan.kind === 'one_time'
    const callout = expired
      ? [`恢复使用并换成${np}：链接不变，设备上不用重新添加。从今天起算，到 ${end}。`]
      : [
          up
            ? `现在就换成${np}：链接不变，不用重新添加。${op}今天停用。`
            : `现在就换成${np}：链接不变。${trafficShift(trafficOf(c.oldPlan, sub), trafficOf(plan, undefined))}、设备 ${devicesText(sub.device_limit)} → ${devicesText(plan.max_devices)}，今天生效。${row.refund > 0 ? `多出的 ${money(row.refund)} 退到钱包余额。` : ''}`,
          onceTarget ? '' : `从今天起重新算一期，到期 ${prev} → ${end}。`,
        ].filter(Boolean)
    const d = row.credit_detail
    // 审计 #3：口径照 Go order/service/proration.go——原来是订阅套餐只按剩余付费时间算（credit_detail 的流量恒为 0），
    // 一次性取剩余天数与剩余流量里少的那个，永久的只看流量；流量上限不含送的
    const oldOnce = sub.plan_kind === 'one_time'
    const oldPermanent = oldOnce && sub.permanent
    const ratio = d ? pct(d.ratio_ppm / 1_000_000) : ''
    const daysPart = d ? `还剩 ${d.days_left}/${d.days_total} 天（${pct(d.days_total ? d.days_left / d.days_total : 0)}）` : ''
    const trafficPart = d ? `流量还剩 ${gb(d.traffic_left)}/${gb(d.traffic_total)}（${pct(d.traffic_total ? d.traffic_left / d.traffic_total : 0)}）` : ''
    const calc = !d
      ? undefined
      : !oldOnce
        ? `${op}这期付了 ${money(d.paid)}。${daysPart}，${money(d.paid)} × ${ratio} = ${money(row.credit)}。按下单那一刻算；送的天数不算钱。`
        : oldPermanent
          ? `${op}付了 ${money(d.paid)}。${trafficPart}，${money(d.paid)} × ${ratio} = ${money(row.credit)}。按下单那一刻算；送的流量不算钱。`
          : `${op}付了 ${money(d.paid)}。${daysPart}，${trafficPart}，按少的那项算：${money(d.paid)} × ${ratio} = ${money(row.credit)}。按下单那一刻算；送的天数和流量不算钱。`
    const notes: Note[] = []
    if (row.credit > 0 && d) {
      const unused = !oldOnce ? `${op}这期没用完的 ${d.days_left} 天` : oldPermanent ? `${op}没用完的 ${gb(d.traffic_left)}` : `${op}没用完的 ${gb(d.traffic_left)} 和 ${d.days_left} 天`
      notes.push({
        b: `${unused}，已经按 ${money(row.credit)} 算给你了`,
        rest: `（先抵${np}的 ${money(row.subtotal - row.discount)}，${row.refund > 0 ? `多的 ${money(row.refund)} 退到钱包` : row.total > 0 ? `还需付 ${money(row.total)}` : '正好抵完'}）。从今天起按${np}（${trafficOf(plan, undefined)}）重新算。`,
      })
    }
    if (row.refund > 0) notes.push(`退回的 ${money(row.refund)} 在「钱包」里，可以用来续费、买套餐，不能提现。`)
    // 只有一次性（非永久）才会因为流量用得快少算钱
    if (oldOnce && !oldPermanent && !up && d && d.days_total && d.traffic_total && d.traffic_left / d.traffic_total < d.days_left / d.days_total - 0.05) {
      notes.push(`你的流量用得比天数快，现在换只能算回 ${money(row.credit)}。不急的话，等这份用得差不多了再换更划算。`)
    }
    const endRow = onceTarget ? validityRow(row, now) : { k: '到期日', v: expired ? `今天起，到 ${end}` : `${prev} → ${end}` }
    return {
      title: `换成${np}`,
      verb: `换成${np}`,
      swap: {
        from: { name: titleOf(sub), facts: `${trafficOf(c.oldPlan, sub)} · ${devicesFact(sub.device_limit)}` },
        to: { name: np, facts: `${planTraffic(plan)} · ${devicesFact(plan.max_devices)}` },
      },
      callout,
      formula: row.credit > 0 || row.discount > 0 ? changeFormula(row) : undefined,
      rows: [
        ...(m ? [{ k: '换的是', v: naming.dn(sub) }] : []),
        { k: `${np} · ${tier(row)}`, v: money(row.subtotal) },
        ...coupon(row),
        ...(row.credit > 0 ? [{ k: '原套餐没用完的部分', v: `−${money(row.credit)}`, ok: true, calc }] : []),
        endRow,
        ...(row.refund > 0 ? [{ k: '多出的退回余额', v: `+${money(row.refund)}`, ok: true }] : []),
      ],
      notes,
      change: '换了以后想换回来也可以，按同样的方法算钱。',
      alt: { text: expired ? `不想换？另外再买一份${np}` : `不想停用${op}？保留它，另外再买一份${np}`, href: href('/checkout', { new: plan.id }) },
    }
  }
  if (target.kind === 'new' && plan) {
    const held = c.held
    const once = plan.kind === 'one_time'
    const endRow = once ? validityRow(row, now) : { k: '到期日', v: `今天起，到 ${row.period_end ? showEnd(row.period_end, now) : ''}` }
    const existing = held.length === 1 ? `你现在的${held[0]!.plan_name}` : `现有的 ${held.length} 份`
    const tierLabel = once ? '一次性' : tier(row)
    return {
      item: { name: plan.name, facts: `${once ? '一次性 · ' : ''}${planTraffic(plan)} · ${devicesFact(plan.max_devices)}` },
      title: held.length ? '再买一份' : `买${plan.name}`,
      verb: held.length ? '买一份新的' : `买${plan.name}`,
      callout: [held.length ? `会得到一个新链接，要在用它的设备上添加到 App。${existing}不受影响，各自到期、各自算流量。` : '会得到一个链接，在要用的设备上添加到 App 就能用。'],
      rows: [{ k: `${plan.name} · ${tierLabel}`, v: money(row.subtotal) }, ...coupon(row), endRow, { k: '流量', v: `${planTraffic(plan)}，从 0 开始` }],
      notes: [],
    }
  }
  if (target.kind === 'reset' && sub) {
    // 重置只把已用清零、送的流量保留：重置后能用满「上限 + 送的」（leftOf 的 total）
    const { left, total, used } = leftOf(sub)
    const scope = sub.plan_kind === 'one_time' ? '有效期内' : '本期'
    const times = sub.reset_limit === null ? '不限次数' : `${scope}还能买 ${Math.max(0, sub.reset_limit - sub.resets_used)} 次`
    return {
      title: '重置流量',
      verb: '重置',
      item: { name: titleOf(sub), facts: `${sub.label ? `${sub.plan_name} · ` : ''}${scope}已用 ${gb(used)}${total !== null ? ` / ${gb(total)}` : ''}`, pill: times },
      callout: [
        m
          ? `把「${naming.dn(sub)}」这一期已用的流量清零，马上生效。链接不变，到期时间不变。`
          : `把你的${sub.plan_name}这一期已用的流量清零，马上生效。链接不变，到期时间不变。`,
      ],
      rows: [
        ...(m ? [{ k: '重置', v: naming.dn(sub) }] : []),
        { k: '重置流量', v: money(row.subtotal) },
        ...coupon(row),
        ...(total !== null && left !== null ? [{ k: '重置后能用', v: `${gb(left)} → ${gb(total)}` }] : []),
      ],
      notes: ['只清这一期已用的流量；到期时间都不变。', ...(sub.traffic_used === 0 ? ['这一期还没用流量，现在重置不会多出流量。'] : [])],
    }
  }
  return { title: '确认', verb: '确认', callout: [], rows: [], notes: [] }
}

// ---------------------------------------------------------------------------
// 付钱那一块：余额开关、合计、凑最低额的说明、主按钮
// ---------------------------------------------------------------------------
interface PayCopy {
  /** 余额那一行；null = 不显示（不用付钱或没有余额） */
  balanceLine: { on: boolean; locked: boolean; text: string; strong?: string } | null
  sum: { text: string; strong?: string; after?: string }
  /** 凑最低额：紧挨金额、正常字色 */
  keptNote?: string
  /** 要选付款方式 */
  needsMethod: boolean
  button: string
  /** below_minimum：用尽余额后剩下的低于在线支付最低额，这一单下不了（只有换套餐 ≤ ¥0.99 的零头会免） */
  tooSmall: boolean
}

/** methods：付款方式的名字（「支付宝、微信支付」），写进最低额的说明 */
export function payCopy(quote: Quote, row: QuoteRow, split: BalanceSplit, useBalance: boolean, verb: string, methods: string): PayCopy {
  const balance = quote.balance
  const balanceLine =
    row.total > 0 && balance > 0
      ? row.with_balance.forced
        ? { on: true, locked: true, text: `这单只要 ${money(row.total)}，低于支付最低额，只能用余额付` }
        : split.below_minimum
          ? { on: true, locked: true, text: `余额 ${money(split.applied)} 已全用上` }
          : useBalance
            ? { on: true, locked: false, strong: `已用余额 ${money(split.applied)}`, text: '（可关掉）' }
            : { on: false, locked: false, text: `用余额（有 ${money(balance)}，现在没用）` }
      : null
  let sum: PayCopy['sum']
  if (row.total === 0) sum = { text: '这次不用付钱', ...(row.refund > 0 ? { after: `，多出的 ${money(row.refund)} 退到钱包余额` } : {}) }
  else if (split.small_due) sum = { text: `零头 ${money(split.waived)} 不到支付最低额，这次免了${split.applied > 0 ? `；余额抵 ${money(split.applied)}` : ''}` }
  else if (split.below_minimum) sum = { text: `${split.applied > 0 ? `余额 ${money(split.applied)} 全用上，` : ''}还差 ${money(split.payable)}。在线付款最少要付 ${money(quote.min_payment)}，这一单付不了：先给钱包充一点，或换成更长的时长` }
  else if (split.applied > 0 && split.payable > 0) sum = { text: `余额抵 ${money(split.applied)}，还需支付 `, strong: money(split.payable) }
  else if (split.applied > 0) sum = { text: '余额够付，', strong: money(split.applied), after: ' 全部用余额' }
  else sum = { text: '要付 ', strong: money(split.payable) }
  const tooSmall = split.below_minimum
  const needsMethod = split.payable > 0 && !split.below_minimum
  const button = tooSmall ? '余额不够，先充值再来' : row.total === 0 || split.small_due ? verb : split.payable > 0 ? `${verb}，付 ${money(split.payable)}` : `${verb}，用余额付 ${money(split.applied)}`
  return {
    balanceLine,
    sum,
    keptNote: split.kept > 0 ? `${methods || '在线付款'}最低要付 ${money(quote.min_payment)}，所以这次余额只用 ${money(split.applied)}，剩下 ${money(split.kept)} 还在余额里。` : undefined,
    needsMethod,
    button,
    tooSmall,
  }
}
