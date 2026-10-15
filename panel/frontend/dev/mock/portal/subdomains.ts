import type { MockModule } from '../types.ts'
import { gate } from './fixtures.ts'

/** 记录领票据的次数（仅供测试验证）。 */
let ticketCalls = 0
export function probeTicketCalls(): number {
  return ticketCalls
}

export const subdomains: MockModule = {
  routes: {
    // 假后端没有订阅域名，也不下发探测页：总是回空票据，门户就不开 iframe
    'POST /v1/sub-probe/ticket': async (ctx) => {
      if (!(await gate(ctx))) return
      ticketCalls++
      ctx.send(200, { ticket: '' })
    },
  },
}
