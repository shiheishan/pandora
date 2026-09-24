/**
 * [INPUT]: 依赖 ../types 的 MockModule
 * [OUTPUT]: 对外提供 wallet 模块的假接口 MockModule
 * [POS]: dev/mock/portal 的「钱包（门户-05）」假接口，归门户前端；形状照 api-contract.md（含修订 Rn）。GET v1/me/balance 同时供外框顶栏余额胶囊使用
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { MockModule } from '../types.ts'

export const wallet: MockModule = {
  routes: {
    'GET /v1/me/balance': (ctx) => ctx.send(200, { balance: 2650, currency: 'CNY', history: [] }),
  },
}
