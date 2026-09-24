/**
 * [INPUT]: 依赖 ../types 的 MockModule，依赖 ./fixtures 的 gate（error / slow 场景）
 * [OUTPUT]: 对外提供 referral 模块的假接口 MockModule
 * [POS]: dev/mock/portal 的「邀请返利（门户-06）」假接口，归门户前端；形状照 api-contract.md（含修订 Rn）。GET v1/me/commission 同时供外框头像菜单的可用佣金提示使用
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { MockModule } from '../types.ts'
import { gate } from './fixtures.ts'

export const referral: MockModule = {
  routes: {
    'GET /v1/me/commission': async (ctx) => {
      if (!(await gate(ctx))) return
      ctx.send(200, {
        summary: { currency: 'CNY', pending: 0, available: 8640, withdrawing: 0, settled: 0, invitees: 4, orders: 2, rate_percent: 20, min_withdraw: 5000 },
        entries: [],
        withdrawals: [],
      })
    },
  },
}
