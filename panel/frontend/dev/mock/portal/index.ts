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
