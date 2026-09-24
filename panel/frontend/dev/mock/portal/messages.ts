/**
 * [INPUT]: 依赖 ../types 的 MockModule，依赖 ./fixtures 的 portalState / gate
 * [OUTPUT]: 对外提供 messages 模块的假接口 MockModule
 * [POS]: dev/mock/portal 的「消息（门户-08）」假接口，归门户前端；形状照 api-contract.md（含修订 Rn）。GET v1/me/notifications 同时供外框铃铛未读角标轮询（形状不变）；GET v1/me/announcements 供概览公告卡，第 ⑤ 步消息页在此扩充
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { MockModule } from '../types.ts'
import { gate, portalState } from './fixtures.ts'

export const messages: MockModule = {
  routes: {
    'GET /v1/me/notifications': (ctx) => ctx.send(200, { notifications: [], unread: 2 }),
    // 最多 20 条，置顶优先、再按发布时间倒序
    'GET /v1/me/announcements': async (ctx) => {
      if (!(await gate(ctx))) return
      const list = [...portalState(ctx.user.userId).announcements].sort((a, b) => Number(b.pinned) - Number(a.pinned) || (b.published_at ?? '').localeCompare(a.published_at ?? ''))
      ctx.send(200, { announcements: list.slice(0, 20) })
    },
  },
}
