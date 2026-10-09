import type { MockModule } from '../types.ts'
import { billing } from './billing.ts'
import { certsModule } from './certs.ts'
import { content } from './content.ts'
import { dash } from './dash.ts'
import { marketing } from './marketing.ts'
import { nodes } from './nodes.ts'
import { plans } from './plans.ts'
import { security } from './security.ts'
import { system } from './system.ts'
import { tickets } from './tickets.ts'
import { users } from './users.ts'

export const ADMIN_MODULES: readonly MockModule[] = [dash, tickets, users, plans, billing, marketing, nodes, certsModule, content, system, security]
