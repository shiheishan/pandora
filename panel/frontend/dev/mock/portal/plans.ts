import type { MockModule } from '../types.ts'
import { planView, plansNow } from './catalog.ts'
import { gate } from './fixtures.ts'

export const plans: MockModule = {
  anonymous: {
    'GET /v1/plans': async (ctx) => {
      if (!(await gate(ctx))) return
      const plans = plansNow()
        .filter((p) => p.prices.length > 0)
        .map(planView)
        .sort((a, b) => (b.recommended ? 1 : 0) - (a.recommended ? 1 : 0))
      ctx.send(200, { plans })
    },
  },
}
