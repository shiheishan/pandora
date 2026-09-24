/**
 * [INPUT]: 依赖 node:crypto 的 randomUUID，依赖 ../types 的 MockModule
 * [OUTPUT]: 对外提供 subs 模块的假接口 MockModule
 * [POS]: dev/mock/portal 的「我的订阅（门户-02）」假接口，归门户前端；形状照 api-contract.md（含修订 Rn）。GET v1/me/subscriptions 同时供外框头像菜单的套餐徽标使用
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomUUID } from 'node:crypto'
import type { MockModule } from '../types.ts'

export const subs: MockModule = {
  routes: {
    'GET /v1/me/subscriptions': (ctx) =>
      ctx.send(200, {
        subscriptions: [
          {
            id: randomUUID(),
            plan_id: randomUUID(),
            price_id: '',
            plan_name: '专业版',
            plan_version: 3,
            status: 'active',
            current_period_start: '2026-09-04T00:00:00Z',
            current_period_end: '2026-11-04T00:00:00Z',
            currency: 'CNY',
            amount: 5900,
            quotas: [],
          },
        ],
      }),
  },
}
