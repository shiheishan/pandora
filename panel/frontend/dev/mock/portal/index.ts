/**
 * [INPUT]: 依赖同目录十一个页面文件的 MockModule，依赖 ../types 的 MockModule
 * [OUTPUT]: 对外提供 PORTAL_MODULES
 * [POS]: dev/mock/portal 的登记表：mock-api.ts 处理完外壳接口后按这里的顺序询问各页面；与 src/portal/screens 的十一个页面一一对应，门户前端只改各页面的文件，本文件不再改动
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { MockModule } from '../types.ts'
import { account } from './account.ts'
import { checkout } from './checkout.ts'
import { help } from './help.ts'
import { messages } from './messages.ts'
import { orders } from './orders.ts'
import { overview } from './overview.ts'
import { plans } from './plans.ts'
import { referral } from './referral.ts'
import { subs } from './subs.ts'
import { tickets } from './tickets.ts'
import { wallet } from './wallet.ts'

export const PORTAL_MODULES: readonly MockModule[] = [overview, subs, plans, checkout, orders, wallet, referral, tickets, messages, help, account]
