/**
 * [INPUT]: 依赖 ../types 的 MockModule，依赖 ./fixtures 的 portalState / subscriptionView / linkUrl / gate
 * [OUTPUT]: 对外提供 subs 模块的假接口 MockModule
 * [POS]: dev/mock/portal 的「我的订阅（门户-02）」假接口，归门户前端；形状照 api-contract.md（含修订 Rn）。GET v1/me/subscriptions 同时供外框头像菜单的套餐徽标使用（外框只读 plan_name / status，扩充字段不影响它）；订阅地址、拉取统计、节点摘要（保留规则 3：只有名称 / 协议 / 倍率）与换发
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomBytes } from 'node:crypto'
import type { MockModule } from '../types.ts'
import { gate, linkUrl, portalState, subscriptionView } from './fixtures.ts'

// 节点接口只对这三种状态下发（subscription/service.go ListOwnedNodePreviews）
const NODE_STATUSES = new Set(['active', 'trialing', 'grace'])

export const subs: MockModule = {
  routes: {
    'GET /v1/me/subscriptions': async (ctx) => {
      if (!(await gate(ctx))) return
      const state = portalState(ctx.user.userId)
      ctx.send(200, { subscriptions: state.subs.map((s) => subscriptionView(s, state.packBytes)) })
    },

    // 只含有可用凭据的订阅；拉取统计是凭据维度的
    'GET /v1/me/subscription-links': async (ctx) => {
      if (!(await gate(ctx))) return
      const links = portalState(ctx.user.userId)
        .subs.filter((s) => s.token !== null && s.status !== 'expired')
        .map((s) => ({
          subscription_id: s.id,
          url: linkUrl(s.token!),
          expires_at: null,
          fetch_count: s.fetchCount,
          last_fetched_at: s.lastFetchedAt,
          distinct_sources_24h: s.sources24h,
        }))
      ctx.send(200, { links })
    },

    // 不属于本人、状态不在 {active,trialing,grace}、没有有效凭据：一律 404
    'GET /v1/me/subscriptions/:id/nodes': async (ctx) => {
      if (!(await gate(ctx))) return
      const sub = portalState(ctx.user.userId).subs.find((s) => s.id === ctx.params.id)
      if (!sub || !NODE_STATUSES.has(sub.status) || sub.token === null) return ctx.fail(404, 'not_found', '资源不存在')
      ctx.res.setHeader('Cache-Control', 'no-store')
      ctx.send(200, { count: sub.nodes.length, nodes: sub.nodes })
    },

    // 无 body、不幂等：每次调用都再换一次，旧凭据立即作废，拉取统计从零开始
    'POST /v1/me/subscriptions/:id/rotate': (ctx) => {
      const sub = portalState(ctx.user.userId).subs.find((s) => s.id === ctx.params.id)
      if (!sub) return ctx.fail(404, 'not_found', '资源不存在')
      sub.token = randomBytes(18).toString('base64url')
      sub.fetchCount = 0
      sub.lastFetchedAt = null
      sub.sources24h = 0
      ctx.send(200, { url: linkUrl(sub.token) })
    },
  },
}
