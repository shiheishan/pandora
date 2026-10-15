import type { MockModule } from '../types.ts'
import { readStrict } from './billing.ts'
import { gate, scenario } from './fixtures.ts'

export interface ProbeResult {
  base_url: string
  ok: boolean
}

/** 记录每个用户最近一次探测上报结果（仅供测试验证） */
const lastReports = new Map<string, ProbeResult[]>()

export function lastProbeReport(userId: string): ProbeResult[] | undefined {
  return lastReports.get(userId)
}

export const subdomains: MockModule = {
  routes: {
    'GET /v1/me/subscription-domains': async (ctx) => {
      if (!(await gate(ctx))) return
      if (scenario() === 'multi') {
        ctx.send(200, {
          domains: ['https://sub-a.example.test', 'https://sub-b.example.test'],
        })
        return
      }
      ctx.send(200, { domains: [] })
    },

    'POST /v1/me/subscription-domains/probe': async (ctx) => {
      const body = await readStrict(ctx, ['results'])
      if (!body) return

      if (!Array.isArray(body.results)) {
        return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      }

      for (const item of body.results) {
        if (!item || typeof item !== 'object' || Array.isArray(item)) {
          return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
        }
        const obj = item as Record<string, unknown>
        const keys = Object.keys(obj)
        if (keys.length !== 2 || !('base_url' in obj) || !('ok' in obj)) {
          return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
        }
        if (typeof obj.base_url !== 'string' || typeof obj.ok !== 'boolean') {
          return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
        }
      }

      if (body.results.length > 10) {
        return ctx.fail(422, 'validation_failed', '请求参数校验未通过', {
          results: '一次最多报告 10 个域名',
        })
      }

      lastReports.set(ctx.user.userId, body.results as ProbeResult[])
      ctx.send(204)
    },
  },
}
