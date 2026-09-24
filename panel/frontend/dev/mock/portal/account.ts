/**
 * [INPUT]: 依赖 ../types 的 MockModule，依赖 ../quick-login 的 issueQuickLogin
 * [OUTPUT]: 对外提供 account 模块的假接口 MockModule
 * [POS]: dev/mock/portal 的「账号安全（门户-10）」假接口，归门户前端；形状照 api-contract.md（含修订 Rn）。签发快捷登录令牌在这里，消费端 POST v1/auth/quick-login 属外壳，留在 mock-api.ts，两边经 quick-login.ts 共用令牌表
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { issueQuickLogin } from '../quick-login.ts'
import type { MockModule } from '../types.ts'

export const account: MockModule = {
  routes: {
    'POST /v1/me/quick-login': (ctx) => {
      const { token, expires } = issueQuickLogin(ctx.user.userId)
      ctx.send(200, { token, expires_at: new Date(expires).toISOString(), expires_in: 60 })
    },
  },
}
