/**
 * [INPUT]: 依赖同目录十个模块文件的 MockModule，依赖 ../types 的 MockModule
 * [OUTPUT]: 对外提供 ADMIN_MODULES
 * [POS]: dev/mock/admin 的登记表：mock-api.ts 处理完外壳接口后按这里的顺序询问各模块；与 src/admin/screens 的十个模块一一对应，各会话只改自己模块的文件，本文件不再改动
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { MockModule } from '../types.ts'
import { billing } from './billing.ts'
import { content } from './content.ts'
import { dash } from './dash.ts'
import { marketing } from './marketing.ts'
import { nodes } from './nodes.ts'
import { plans } from './plans.ts'
import { security } from './security.ts'
import { system } from './system.ts'
import { tickets } from './tickets.ts'
import { users } from './users.ts'

export const ADMIN_MODULES: readonly MockModule[] = [dash, tickets, users, plans, billing, marketing, nodes, content, system, security]
