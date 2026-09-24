/**
 * [INPUT]: 依赖 ../types 的 MockModule
 * [OUTPUT]: 对外提供 messages 模块的假接口 MockModule
 * [POS]: dev/mock/portal 的「消息（门户-08）」假接口，归门户前端；形状照 api-contract.md（含修订 Rn）。GET v1/me/notifications 同时供外框铃铛未读角标轮询
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { MockModule } from '../types.ts'

export const messages: MockModule = {
  routes: {
    'GET /v1/me/notifications': (ctx) => ctx.send(200, { notifications: [], unread: 2 }),
  },
}
