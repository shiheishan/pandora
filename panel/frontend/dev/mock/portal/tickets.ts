/**
 * [INPUT]: 依赖 ../types 的 MockModule
 * [OUTPUT]: 对外提供 tickets 模块的假接口 MockModule
 * [POS]: dev/mock/portal 的「工单支持（门户-07）」假接口，归门户前端；形状、错误码、reauth 与幂等照 api-contract.md（含修订 Rn）；目前为空
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { MockModule } from '../types.ts'

export const tickets: MockModule = {}
