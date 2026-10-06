import type { MockModule } from '../types.ts'
import { gate, MOCK_TIMEZONE, portalState, SCENARIOS, setScenario, zoneMidnight } from './fixtures.ts'

export const overview: MockModule = {
  anonymous: {
    'POST /v1/__mock/portal-scenario': async (ctx) => {
      const body = await ctx.body()
      const name = typeof body?.name === 'string' ? body.name : ''
      if (!setScenario(name)) return ctx.fail(422, 'validation_failed', `场景只能是 ${SCENARIOS.join(' / ')}`, { name: '未知场景' })
      ctx.send(200, { scenario: name })
    },
  },
  routes: {
    // 修订 R47：days 须为 1–93 的整数（422 fields.days）；不是本人的订阅一律 404；缺省窗口为本期流量周期，最多 93 天，无数据的日子补 0
    'GET /v1/me/subscriptions/:id/usage': async (ctx) => {
      if (!(await gate(ctx))) return
      const raw = ctx.query.get('days')
      let limit = 93
      if (raw !== null) {
        const n = Number(raw)
        if (!/^\d+$/.test(raw) || n < 1 || n > 93) return ctx.fail(422, 'validation_failed', '参数不合法', { days: '须为 1–93 的整数' })
        limit = n
      }
      const sub = portalState(ctx.user.userId).subs.find((s) => s.id === ctx.params.id)
      if (!sub) return ctx.fail(404, 'not_found', '资源不存在')
      const days = sub.days.slice(-limit)
      const total = days.reduce((s, d) => s + d.bytes, 0)
      ctx.send(200, {
        timezone: MOCK_TIMEZONE,
        period_start: zoneMidnight(days[0]!.date),
        period_end: sub.resetAt,
        days,
        today_bytes: days[days.length - 1]!.bytes,
        avg_daily_bytes: Math.floor(total / days.length),
      })
    },
  },
}
