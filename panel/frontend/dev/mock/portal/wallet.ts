/**
 * [INPUT]: 依赖 ../types 的 MockModule，依赖 ./fixtures 的 gate（error / slow 场景）与 portalState（余额随下单抵扣变化）
 * [OUTPUT]: 对外提供 wallet 模块的假接口 MockModule
 * [POS]: dev/mock/portal 的「钱包（门户-05）」假接口，归门户前端；形状照 api-contract.md（含修订 Rn）。GET v1/me/balance 同时供外框顶栏余额胶囊使用
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { MockModule } from '../types.ts'
import { gate, portalState } from './fixtures.ts'

export const wallet: MockModule = {
  routes: {
    'GET /v1/me/balance': async (ctx) => {
      if (!(await gate(ctx))) return
      ctx.send(200, { balance: portalState(ctx.user.userId).balance, currency: 'CNY', history: [] })
    },
  },
}
