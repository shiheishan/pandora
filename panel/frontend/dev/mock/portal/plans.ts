/**
 * [INPUT]: 依赖 ../types 的 MockModule，依赖 ./fixtures 的 portalState / gate
 * [OUTPUT]: 对外提供 plans 模块的假接口 MockModule
 * [POS]: dev/mock/portal 的「选购套餐（门户-03）」假接口，归门户前端；形状照 api-contract.md（含修订 R30）。第 ① 步只有概览剩余流量要的 GET v1/me/traffic-packs，第 ② 步选购与结账在此扩充
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomUUID } from 'node:crypto'
import type { MockModule } from '../types.ts'
import { GIB, gate, portalState } from './fixtures.ts'

export const plans: MockModule = {
  routes: {
    'GET /v1/me/traffic-packs': async (ctx) => {
      if (!(await gate(ctx))) return
      const remaining = portalState(ctx.user.userId).packBytes
      const packs =
        remaining > 0
          ? [{ id: randomUUID(), source: 'gift_card', order_id: null, granted_bytes: 50 * GIB, consumed_bytes: 50 * GIB - remaining, remaining_bytes: remaining, created_at: new Date(Date.now() - 20 * 86_400_000).toISOString() }]
          : []
      ctx.send(200, { remaining_bytes_total: remaining, packs })
    },
  },
}
