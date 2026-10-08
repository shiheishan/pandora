import type { MockModule } from '../types.ts'
import { readStrict } from './billing.ts'
import { clientName, gate, linkUrl, newToken, portalState, subscriptionView } from './fixtures.ts'
import { normalizeLabel } from './purchase.ts'

// 节点接口只对这三种状态下发（subscription/service.go ListOwnedNodePreviews）
const NODE_STATUSES = new Set(['active', 'trialing', 'grace'])
const MINUTE_MS = 60_000
const DAY_MS = 86_400_000

/**
 * 换新链接按份限频（设计稿 2.8）：每份每 10 分钟 1 次、每份每 24 小时 5 次，另按账号每 24 小时 20 次。
 * 超限 429 rate_limited「操作太频繁，请 X 分钟后再试」（middleware.RateLimit 的 RetryHint），Retry-After: 60
 */
function rotateWait(perSub: number[], all: number[], now: number): number | null {
  const recent = (list: number[], window: number) => list.filter((t) => now - t < window)
  const gap = recent(perSub, 10 * MINUTE_MS)
  if (gap.length >= 1) return Math.min(...gap) + 10 * MINUTE_MS - now
  const day = recent(perSub, DAY_MS)
  if (day.length >= 5) return Math.min(...day) + DAY_MS - now
  const account = recent(all, DAY_MS)
  if (account.length >= 20) return Math.min(...account) + DAY_MS - now
  return null
}

export const subs: MockModule = {
  routes: {
    // 设计稿 2.9：每项多 label / client_name / changeable / renew_until，pack_remaining_bytes 是这一份自己的；
    // 顶层 unattached_pack_bytes 是还没加到任何一份的流量包余量
    'GET /v1/me/subscriptions': async (ctx) => {
      if (!(await gate(ctx))) return
      const state = portalState(ctx.user.userId)
      ctx.send(200, { subscriptions: state.subs.map(subscriptionView), unattached_pack_bytes: state.unattachedBytes })
    },

    // 设计稿 2.8：改名不推进节点纪元；空串或 null 清除；同一用户下不区分大小写唯一，冲突 409
    'PATCH /v1/me/subscriptions/:id': async (ctx) => {
      const body = await readStrict(ctx, ['label'])
      if (!body) return
      const state = portalState(ctx.user.userId)
      const sub = state.subs.find((s) => s.id === ctx.params.id)
      if (!sub) return ctx.fail(404, 'not_found', '资源不存在')
      if (body.label !== null && typeof body.label !== 'string') return ctx.fail(422, 'validation_failed', '参数不合法', { label: '须为字符串或 null' })
      const n = normalizeLabel(body.label ?? '')
      if ('error' in n) return ctx.fail(422, 'validation_failed', '参数不合法', { label: n.error })
      const label = n.ok || null
      const taken = label && state.subs.find((s) => s !== sub && s.label?.toLowerCase() === label.toLowerCase())
      if (taken) return ctx.fail(409, 'conflict', `这个名字已经用在「${taken.label} · ${taken.plan_name}」上了`, { label: '换一个名字' })
      sub.label = label
      ctx.send(200, { label: sub.label, client_name: clientName(sub) })
    },

    // 只含有可用凭据的订阅；拉取统计是凭据维度的。过期 30 天内的照常列出、标 expired（只读）
    'GET /v1/me/subscription-links': async (ctx) => {
      if (!(await gate(ctx))) return
      const closedBefore = Date.now() - 30 * DAY_MS
      const links = portalState(ctx.user.userId)
        .subs.filter((s) => s.token !== null && s.status !== 'cancelled' && !s.renewalClosed && (s.status !== 'expired' || new Date(s.current_period_end).getTime() > closedBefore))
        .map((s) => ({
          subscription_id: s.id,
          url: linkUrl(s.token!),
          expires_at: null,
          fetch_count: s.fetchCount,
          last_fetched_at: s.lastFetchedAt,
          distinct_sources_24h: s.sources24h,
          expired: s.status === 'expired' || new Date(s.current_period_end).getTime() <= Date.now(),
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

    // 无 body、不幂等：每次调用都再换一次，旧凭据立即作废，拉取统计从零开始；只换这一份
    'POST /v1/me/subscriptions/:id/rotate': (ctx) => {
      const state = portalState(ctx.user.userId)
      const sub = state.subs.find((s) => s.id === ctx.params.id)
      if (!sub) return ctx.fail(404, 'not_found', '资源不存在')
      const now = Date.now()
      const wait = rotateWait(
        sub.rotations,
        state.subs.flatMap((s) => s.rotations),
        now,
      )
      if (wait !== null) {
        ctx.res.setHeader('Retry-After', '60')
        return ctx.fail(429, 'rate_limited', `操作太频繁，请 ${Math.max(1, Math.ceil(wait / MINUTE_MS))} 分钟后再试`)
      }
      // 过期期间不许换（subscription.ErrRotateWhileExpired）
      if (sub.status === 'expired' || new Date(sub.current_period_end).getTime() <= now) {
        return ctx.fail(409, 'conflict', '订阅已过期，续费后原链接会自动恢复；过期期间不能更换订阅链接')
      }
      sub.rotations.push(now)
      sub.token = newToken()
      sub.fetchCount = 0
      sub.lastFetchedAt = null
      sub.sources24h = 0
      ctx.send(200, { url: linkUrl(sub.token) })
    },
  },
}
