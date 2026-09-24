/**
 * [INPUT]: 依赖 ../types 的 MockModule
 * [OUTPUT]: 对外提供 help 模块的假接口 MockModule
 * [POS]: dev/mock/portal 的「帮助中心（门户-09）」假接口，归门户前端；形状、错误码、reauth 与幂等照 api-contract.md（含修订 Rn）；目前为空
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { MockModule } from '../types.ts'

export const help: MockModule = {}
