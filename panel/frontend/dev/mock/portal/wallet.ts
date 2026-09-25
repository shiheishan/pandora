/**
 * [INPUT]: 依赖 ../types 的 MockModule，依赖 ./fixtures 的 portalState / gate / makeSub，依赖 ./billing 的校验、下单与余额流水，依赖 ./catalog 的礼品卡与目录
 * [OUTPUT]: 对外提供 wallet 模块的假接口 MockModule
 * [POS]: dev/mock/portal 的「钱包（门户-05）」假接口，归门户前端；形状照 api-contract.md（含修订 R31、R68）：余额与流水（外框余额胶囊也读它）、充值建单（幂等 balance_topup_create、200、金额 100–5000000 分）、礼品卡预览（不回发行量、套餐卡带套餐名与周期）与兑换（幂等 gift_card_redeem；流量进流量包余额，延期要有生效订阅）、我的兑换记录
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { MockModule } from '../types.ts'
import { BillingError, moveBalance, placeOrder, readStrict } from './billing.ts'
import { findPlan, GIFT_CARDS, type GiftTemplate } from './catalog.ts'
import { gate, makeSub, portalState, type PortalState } from './fixtures.ts'

const LIVE = new Set(['active', 'trialing', 'grace', 'past_due'])
const INVALID = '卡密无效、已被使用或已过期'
const normalize = (code: unknown) => (typeof code === 'string' ? code.replace(/\s+/g, '').toUpperCase() : '')

function lookup(state: PortalState, code: string): GiftTemplate {
  const card = GIFT_CARDS[code]
  if (!card || state.usedCodes.has(code)) throw new BillingError(422, 'validation_failed', INVALID)
  return card
}

/** 按模板发奖；返回契约 redeem 响应 */
function grant(state: PortalState, code: string, card: GiftTemplate) {
  const live = state.subs.find((s) => LIVE.has(s.status))
  // 盲盒固定抽第二项，便于复现
  const prize = card.type === 'mystery' ? card.rewards.pool?.[1] : undefined
  const r = prize ?? card.rewards
  const summary: string[] = []
  // 延长到期要有生效订阅（修订 R31 后流量奖励不再要求）
  if (r.expire_days && !live) throw new BillingError(422, 'validation_failed', '你当前没有生效中的订阅，这类奖励需要先有一个套餐才能发放')
  if (r.balance) {
    moveBalance(state, 'gift_card_balance', r.balance, `礼品卡 ${code.slice(0, 12)}…`)
    summary.push(`余额 +¥${(r.balance / 100).toFixed(2)}`)
  }
  if (r.traffic_bytes) {
    state.packBytes += r.traffic_bytes
    summary.push(`流量包 +${Math.round(r.traffic_bytes / 1024 ** 3)} GB`)
  }
  if (r.expire_days && live) {
    live.current_period_end = new Date(new Date(live.current_period_end).getTime() + r.expire_days * 86_400_000).toISOString()
    summary.push(`订阅延长 ${r.expire_days} 天`)
  }
  let planGranted: string | undefined
  if (card.type === 'plan') {
    const plan = findPlan(card.rewards.plan_id)!
    if (live) live.current_period_end = new Date(new Date(live.current_period_end).getTime() + 30 * 86_400_000).toISOString()
    else state.subs.unshift(makeSub({ planId: plan.id, priceId: card.rewards.price_id!, status: 'active', usedGiB: 0, elapsedDays: 0, resetInDays: 30, expiresInDays: 30, online: 0, sources: 0 }))
    planGranted = plan.name
    summary.push(live ? `${plan.name}顺延 30 天` : `已开通${plan.name}`)
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
    redeemed_at: new Date().toISOString(),
  })
  return {
    template_name: card.name,
    type: card.type,
    ...(prize ? { prize_label: prize.label } : {}),
    ...(r.balance ? { balance: r.balance } : {}),
    ...(r.traffic_bytes ? { traffic_bytes: r.traffic_bytes } : {}),
    ...(r.expire_days ? { expire_days: r.expire_days } : {}),
    ...(planGranted ? { plan_granted: planGranted } : {}),
    summary,
  }
}

export const wallet: MockModule = {
  routes: {
    'GET /v1/me/balance': async (ctx) => {
      if (!(await gate(ctx))) return
      const state = portalState(ctx.user.userId)
      ctx.send(200, { balance: state.balance, currency: 'CNY', history: state.ledger.slice(0, 100) })
    },

    'POST /v1/me/topups': async (ctx) => {
      const body = await readStrict(ctx, ['amount', 'currency'])
      if (!body) return
      await ctx.idempotent('balance_topup_create', () => {
        const amount = body.amount
        if (typeof amount !== 'number' || !Number.isInteger(amount) || amount < 100) return new BillingError(422, 'validation_failed', '充值金额太小', { amount: '最少 100 分' }).result()
        if (amount > 5_000_000) return new BillingError(422, 'validation_failed', '单次充值金额超出上限', { amount: '最多 5000000 分' }).result()
        if (body.currency !== undefined && body.currency !== 'CNY' && body.currency !== 'USD') return new BillingError(422, 'validation_failed', '充值币种只支持 CNY 或 USD', { currency: '只支持 CNY 或 USD' }).result()
        const order = placeOrder(portalState(ctx.user.userId), { kind: 'topup', subtotal: amount, coupon: undefined, priceId: null, useBalance: undefined, effect: { type: 'topup', amount } })
        return { status: 200, body: { order_id: order.id, order_no: order.order_no, amount, currency: 'CNY' } }
      })
    },

    'POST /v1/gift-cards/preview': async (ctx) => {
      const body = await readStrict(ctx, ['code'])
      if (!body) return
      const code = normalize(body.code)
      try {
        const card = lookup(portalState(ctx.user.userId), code)
        const plan = card.type === 'plan' ? findPlan(card.rewards.plan_id) : undefined
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
            ...(plan ? { plan_name: plan.name, interval: 'month', interval_count: 1 } : {}),
          },
        })
      } catch (e) {
        if (!(e instanceof BillingError)) throw e
        ctx.fail(e.status, e.code, e.message)
      }
    },

    'POST /v1/gift-cards/redeem': async (ctx) => {
      const body = await readStrict(ctx, ['code'])
      if (!body) return
      await ctx.idempotent('gift_card_redeem', () => {
        try {
          const state = portalState(ctx.user.userId)
          const code = normalize(body.code)
          const card = lookup(state, code)
          if (card.redeemError) throw new BillingError(422, 'validation_failed', card.redeemError)
          return { status: 200, body: grant(state, code, card) }
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
