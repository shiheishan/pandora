import type { MockModule } from '../types.ts'
import { BillingError, moveBalance, placeOrder, readStrict, renewSub, swapPlan } from './billing.ts'
import { findPlan, findPrice, GIB, GIFT_CARDS, type GiftTemplate } from './catalog.ts'
import { addInterval, gate, isLiveSub, isRevivable, makeSub, portalState, subPermanent, usedBytes, type PortalState, type SubFixture } from './fixtures.ts'
import { ChoiceError, options, resolve, type Candidate, type Choice, type Offer, type Option } from './purchase.ts'
import { creditOf } from './quote.ts'

const DAY_MS = 86_400_000
const INVALID = '卡密无效、已被使用或已过期'
const NO_PLACEMENT = '现在没有在用的套餐，这张卡暂时用不了。先续费再来兑换，卡不会过期'
const normalize = (code: unknown) => (typeof code === 'string' ? code.replace(/\s+/g, '').toUpperCase() : '')

function lookup(state: PortalState, code: string): GiftTemplate {
  const card = GIFT_CARDS[code]
  if (!card || state.usedCodes.has(code)) throw new BillingError(422, 'validation_failed', INVALID)
  return card
}

// ---------------------------------------------------------------------------
// 落点（设计稿 2.5）：卡面 → Offer → purchase.Options；preview 回 placement {question, options, default_key}，
// 纯余额卡为 null；redeem 带 choice，在当前选项里 Match，不在里面回 422 且卡不被用掉
// ---------------------------------------------------------------------------
export function candidates(state: PortalState): Candidate[] {
  const now = Date.now()
  return state.subs.map((s) => ({
    subscriptionId: s.id,
    planId: s.plan_id,
    planKind: findPlan(s.plan_id)?.kind ?? 'subscription',
    state: isLiveSub(s) ? 'live' : isRevivable(s, now) ? 'revivable' : 'dead',
    periodEnd: new Date(s.current_period_end).getTime(),
    permanent: subPermanent(s),
    trafficUsed: usedBytes(s),
    trafficCap: s.limitBytes ?? 0,
  }))
}

type Rewards = GiftTemplate['rewards']

/** 盲盒取奖池各项的并集：抽中哪项都落在同一份上 */
function offerOf(card: GiftTemplate): Offer | null {
  if (card.type === 'plan') {
    const plan = findPlan(card.rewards.plan_id)
    return { kind: 'plan', planId: card.rewards.plan_id, planKind: plan?.kind ?? 'subscription' }
  }
  const all: Rewards[] = card.type === 'mystery' ? (card.rewards.pool ?? []) : [card.rewards]
  const hasDays = all.some((r) => (r.expire_days ?? 0) > 0)
  const hasReset = all.some((r) => r.reset_quota === true)
  const hasTraffic = all.some((r) => (r.traffic_bytes ?? 0) > 0)
  const count = [hasDays, hasReset, hasTraffic].filter(Boolean).length
  if (count === 0) return null
  if (count === 1 && card.type !== 'mystery') return { kind: hasDays ? 'days' : hasReset ? 'reset' : 'traffic' }
  return { kind: 'mixed', hasDays, hasReset, hasTraffic, mystery: card.type === 'mystery' }
}

const QUESTION: Readonly<Record<string, string>> = { extend_days: '加到哪一份？', reset_traffic: '重算哪一份？', add_traffic: '加到哪一份？' }

function renewalBase(periodEndMs: number, now: number): number {
  return periodEndMs > now ? periodEndMs : now
}

/** 落地后的到期时刻（毫秒）；永久或盲盒加时长不给 */
function newEnd(card: GiftTemplate, op: Option, sub: SubFixture | undefined, now: number): { end: number | null; permanent: boolean } {
  if (card.type === 'plan') {
    const plan = findPlan(card.rewards.plan_id)!
    const price = findPrice(plan, card.rewards.price_id)!
    if (price.billing_interval === 'one_time') {
      const stackPermanent = op.kind === 'stack' && sub && subPermanent(sub)
      if (stackPermanent || price.validity_days === null) return { end: null, permanent: true }
      let from = now
      if (op.kind === 'stack' && sub) {
        const pe = new Date(sub.current_period_end).getTime()
        if (pe > now) from = pe
      }
      return { end: from + price.validity_days! * DAY_MS, permanent: false }
    }
    let from = now
    if (op.kind === 'renew' && sub) from = renewalBase(new Date(sub.current_period_end).getTime(), now)
    return { end: addInterval(from, price), permanent: false }
  }
  if (op.kind === 'extend_days' && sub && card.type !== 'mystery' && (card.rewards.expire_days ?? 0) > 0) {
    const from = Math.max(now, new Date(sub.current_period_end).getTime())
    return { end: from + (card.rewards.expire_days ?? 0) * DAY_MS, permanent: false }
  }
  return { end: null, permanent: false }
}

/** giftcard.placementViews：选项连同这一份现在的样子与落地之后；Go 字段带 omitempty，零值缺席 */
function placementView(state: PortalState, card: GiftTemplate, op: Option, now: number) {
  const sub = op.subscription_id ? state.subs.find((s) => s.id === op.subscription_id) : undefined
  const { end, permanent } = newEnd(card, op, sub, now)
  const credit = op.kind === 'change' && sub && isLiveSub(sub) ? creditOf(sub, now).credit : 0
  const used = sub ? usedBytes(sub) : 0
  const cap = sub?.limitBytes
  return {
    ...op,
    ...(sub?.label ? { label: sub.label } : {}),
    ...(sub
      ? {
          plan_id: sub.plan_id,
          plan_name: sub.plan_name,
          state: isLiveSub(sub) ? 'live' : isRevivable(sub, now) ? 'revivable' : 'dead',
          period_end: sub.current_period_end,
        }
      : {}),
    ...(permanent ? { new_permanent: true } : end !== null ? { new_period_end: new Date(end).toISOString() } : {}),
    ...(credit > 0 ? { credit } : {}),
    ...(used > 0 ? { traffic_used: used } : {}),
    ...(cap != null && cap > 0 ? { traffic_cap: cap } : {}),
  }
}

function placement(state: PortalState, card: GiftTemplate) {
  const offer = offerOf(card)
  if (!offer) return null
  const [opts, def] = options(offer, candidates(state))
  const now = Date.now()
  const question = card.type === 'plan' ? (def ? '怎么用这张卡？' : '这张卡怎么用？') : (QUESTION[opts[0]?.kind ?? ''] ?? '用在哪一份？')
  return { question, options: opts.map((o) => placementView(state, card, o, now)), default_key: def }
}

function stackSub(sub: SubFixture, plan: ReturnType<typeof findPlan>, price: ReturnType<typeof findPrice>, now: number) {
  if (!plan || !price) return
  if (sub.limitBytes !== null) {
    if (plan.trafficBytes === null) sub.limitBytes = null
    else sub.limitBytes += plan.trafficBytes
  }
  if (!subPermanent(sub)) {
    if (price.validity_days === null) sub.current_period_end = new Date(addInterval(now, price)).toISOString()
    else {
      let from = now
      const pe = new Date(sub.current_period_end).getTime()
      if (pe > now) from = pe
      sub.current_period_end = new Date(from + price.validity_days * DAY_MS).toISOString()
    }
  }
  sub.status = 'active'
  sub.price_id = price.id
  sub.amount = price.unit_amount
}

function formatTrafficGb(bytes: number) {
  return `${Math.round(bytes / GIB)} GB`
}

/** 按模板与选定的那一份发奖；返回契约 redeem 响应 */
function grant(state: PortalState, code: string, card: GiftTemplate, choice: Choice | null) {
  const offer = offerOf(card)
  let target: Option | null = null
  if (offer) {
    try {
      target = resolve(choice, options(offer, candidates(state))[0])
    } catch (e) {
      if (e instanceof ChoiceError) throw new BillingError(422, 'validation_failed', e.message, { choice: e.message })
      throw e
    }
  }
  const sub = target?.subscription_id ? state.subs.find((s) => s.id === target.subscription_id) : undefined
  // 盲盒固定抽第二项，便于复现
  const prize = card.type === 'mystery' ? card.rewards.pool?.[1] : undefined
  const r = prize ?? card.rewards
  const summary: string[] = []
  const now = Date.now()
  // 加时长、重置、送流量都要落在某一份上（Go grant 的 needsSub）：一份可落的都没有就拒绝，卡不被用掉
  if ((r.expire_days || r.reset_quota || r.traffic_bytes) && !sub) throw new BillingError(422, 'validation_failed', NO_PLACEMENT)
  if (r.balance) {
    moveBalance(state, 'gift_card_balance', r.balance, `礼品卡 ${code.slice(0, 12)}…`)
    summary.push(`余额 +¥${(r.balance / 100).toFixed(2)}`)
  }
  if (r.traffic_bytes) {
    if (sub!.limitBytes === null) {
      if (card.type === 'mystery') summary.push(`这份不限流量，流量 +${formatTrafficGb(r.traffic_bytes)} 已跳过`)
      else throw new BillingError(422, 'validation_failed', '这份套餐不限流量，加不了流量')
    } else {
      sub!.bonusBytes += r.traffic_bytes
      summary.push(`本期流量 +${formatTrafficGb(r.traffic_bytes)}`)
    }
  }
  if (r.expire_days && sub) {
    if (subPermanent(sub)) summary.push(`「永久」套餐不加时长，${r.expire_days} 天已跳过`)
    else {
      const revive = !isLiveSub(sub)
      sub.current_period_end = new Date(Math.max(now, new Date(sub.current_period_end).getTime()) + r.expire_days * DAY_MS).toISOString()
      if (revive) Object.assign(sub, { status: 'active', current_period_start: new Date(now).toISOString() })
      summary.push(`延长 ${r.expire_days} 天`)
    }
  }
  if (r.reset_quota && sub) {
    sub.days = sub.days.map((d) => ({ ...d, bytes: 0 }))
    summary.push('本期流量已重置')
  }
  let planGranted: string | undefined
  if (card.type === 'plan' && target) {
    const plan = findPlan(card.rewards.plan_id)!
    const price = findPrice(plan, card.rewards.price_id)!
    planGranted = plan.name
    if (target.kind === 'stack' && sub) {
      stackSub(sub, plan, price, now)
      summary.push(`已叠加到${plan.name}`)
    } else if (target.kind === 'renew' && sub) {
      renewSub(sub, price, now)
      summary.push(`${plan.name}续了一期`)
    } else if (target.kind === 'change' && sub) {
      const credit = isLiveSub(sub) ? creditOf(sub, now).credit : 0
      swapPlan(sub, plan, price)
      if (credit > 0) moveBalance(state, 'plan_change_refund', credit, `兑换${plan.name}，退回`)
      summary.push(`已换成${plan.name}`)
    } else {
      const fresh = makeSub({ planId: plan.id, priceId: price.id, status: 'active', usedGiB: 0, elapsedDays: 0, resetInDays: 30, expiresInDays: 30, online: 0, sources: 0 })
      if (price.billing_interval === 'one_time' && price.validity_days != null) fresh.current_period_end = new Date(now + price.validity_days * DAY_MS).toISOString()
      else fresh.current_period_end = new Date(addInterval(now, price)).toISOString()
      state.subs.push(fresh)
      summary.push(`已开通${plan.name}`)
    }
  }
  state.usedCodes.add(code)
  state.redemptions.unshift({
    template_name: card.name,
    type: card.type,
    code_hint: `${code.slice(0, 12)}…`,
    ...(prize ? { prize_label: prize.label } : {}),
    ...(r.balance ? { balance: r.balance } : {}),
    ...(r.traffic_bytes ? { traffic_bytes: r.traffic_bytes } : {}),
    ...(r.expire_days ? { expire_days: r.expire_days } : {}),
    redeemed_at: new Date(now).toISOString(),
  })
  return {
    template_name: card.name,
    type: card.type,
    ...(prize ? { prize_label: prize.label } : {}),
    ...(r.balance ? { balance: r.balance } : {}),
    ...(r.traffic_bytes ? { traffic_bytes: r.traffic_bytes } : {}),
    ...(r.expire_days ? { expire_days: r.expire_days } : {}),
    ...(r.reset_quota ? { quota_reset: true } : {}),
    ...(planGranted ? { plan_granted: planGranted } : {}),
    summary,
  }
}

function cardPreviewFields(card: GiftTemplate) {
  if (card.type !== 'plan') return {}
  const plan = findPlan(card.rewards.plan_id)
  const price = plan ? findPrice(plan, card.rewards.price_id) : undefined
  if (!plan || !price) return {}
  return {
    plan_name: plan.name,
    plan_kind: plan.kind,
    interval: price.billing_interval,
    interval_count: price.interval_count,
    ...(price.validity_days != null ? { validity_days: price.validity_days } : {}),
  }
}

/** choice 形状：{kind, subscription_id?}；缺席为 null */
function choiceOf(raw: unknown): Choice | null {
  if (raw === undefined || raw === null) return null
  if (typeof raw !== 'object') throw new BillingError(422, 'validation_failed', '参数不合法', { choice: '须为对象' })
  const c = raw as Record<string, unknown>
  return { kind: c.kind as Choice['kind'], ...(typeof c.subscription_id === 'string' ? { subscription_id: c.subscription_id } : {}) }
}

export const wallet: MockModule = {
  routes: {
    'GET /v1/me/balance': async (ctx) => {
      if (!(await gate(ctx))) return
      const state = portalState(ctx.user.userId)
      ctx.send(200, { balance: state.balance, history: state.ledger.slice(0, 100) })
    },

    'POST /v1/me/topups': async (ctx) => {
      const body = await readStrict(ctx, ['amount'])
      if (!body) return
      await ctx.idempotent('balance_topup_create', () => {
        const amount = body.amount
        if (typeof amount !== 'number' || !Number.isInteger(amount) || amount < 100) return new BillingError(422, 'validation_failed', '充值金额太小', { amount: '最少 100 分' }).result()
        if (amount > 5_000_000) return new BillingError(422, 'validation_failed', '单次充值金额超出上限', { amount: '最多 5000000 分' }).result()
        const order = placeOrder(portalState(ctx.user.userId), { kind: 'topup', subtotal: amount, discount: 0, total: amount, applied: 0, effect: { type: 'topup', amount } })
        return { status: 200, body: { order_id: order.id, order_no: order.order_no, amount } }
      })
    },

    'POST /v1/gift-cards/preview': async (ctx) => {
      const body = await readStrict(ctx, ['code'])
      if (!body) return
      const code = normalize(body.code)
      try {
        const card = lookup(portalState(ctx.user.userId), code)
        // 盲盒只回奖品名（weight 0），不回具体奖励
        const rewards = card.type === 'mystery' ? { pool: card.rewards.pool?.map((p) => ({ label: p.label, weight: 0 })) } : card.rewards
        ctx.send(200, {
          card: {
            id: code,
            name: card.name,
            description: card.description,
            type: card.type,
            status: 'active',
            rewards,
            conditions: {},
            limits: {},
            theme_color: '#b9442b',
            created_at: '2026-09-01T00:00:00Z',
            ...cardPreviewFields(card),
            placement: placement(portalState(ctx.user.userId), card),
          },
        })
      } catch (e) {
        if (!(e instanceof BillingError)) throw e
        ctx.fail(e.status, e.code, e.message)
      }
    },

    'POST /v1/gift-cards/redeem': async (ctx) => {
      const body = await readStrict(ctx, ['code', 'choice'])
      if (!body) return
      await ctx.idempotent('gift_card_redeem', () => {
        try {
          const state = portalState(ctx.user.userId)
          const code = normalize(body.code)
          const card = lookup(state, code)
          if (card.redeemError) throw new BillingError(422, 'validation_failed', card.redeemError)
          return { status: 200, body: grant(state, code, card, choiceOf(body.choice)) }
        } catch (e) {
          if (e instanceof BillingError) return e.result()
          throw e
        }
      })
    },

    'GET /v1/me/gift-cards': async (ctx) => {
      if (!(await gate(ctx))) return
      ctx.send(200, { redemptions: portalState(ctx.user.userId).redemptions.slice(0, 100) })
    },
  },
}
