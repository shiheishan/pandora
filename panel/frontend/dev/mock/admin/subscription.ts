import type { Json, MockContext, MockModule, MockResult } from '../types.ts'

// ---------------------------------------------------------------------------
// 订阅附加设置（settings），形状与 Go 一致。
// 订阅模板页只读列出「客户端 → 格式」（前端写死），后端没有模板接口。
// ---------------------------------------------------------------------------
const err = (status: number, code: string, message: string, fields?: Record<string, string>): MockResult => ({ status, body: { error: { code, message, ...(fields ? { fields } : {}) } } })
const reply = (ctx: MockContext, r: MockResult) => ctx.send(r.status, r.body)

type Decoded = { ok: true; body: Json } | { ok: false; result: MockResult }
async function decode(ctx: MockContext, shape: Record<string, 'boolean' | 'strings' | 'string' | 'int'>): Promise<Decoded> {
  const bad = (message = '请求体不是合法的 JSON'): Decoded => ({ ok: false, result: err(400, 'bad_request', message) })
  const body = await ctx.body()
  if (!body) return bad()
  for (const k of Object.keys(body)) {
    if (!(k in shape)) return bad()
    const want = shape[k]!
    const v = body[k]
    if (want === 'boolean' && typeof v !== 'boolean') return bad(`字段 "${k}" 类型不正确`)
    if (want === 'string' && typeof v !== 'string') return bad(`字段 "${k}" 类型不正确`)
    if (want === 'int' && !(typeof v === 'number' && Number.isInteger(v))) return bad(`字段 "${k}" 类型不正确`)
    if (want === 'strings' && (!Array.isArray(v) || v.some((x) => typeof x !== 'string'))) return bad(`字段 "${k}" 类型不正确`)
  }
  return { ok: true, body }
}

const extras = { info_nodes: false, protocol_prefix: false, fetch_limit_per_hour: 60 }

export const subscriptionModule: MockModule = {
  routes: {
    'GET /v1/settings/subscription': (ctx) => {
      ctx.send(200, { ...extras })
    },
    'POST /v1/settings/subscription': async (ctx) => {
      if (!ctx.requireReauth()) return
      const d = await decode(ctx, { info_nodes: 'boolean', protocol_prefix: 'boolean', fetch_limit_per_hour: 'int' })
      if (!d.ok) return reply(ctx, d.result)
      const b = d.body
      // 先校验完再改状态，4xx 时内存里的设置保持原样
      if (b.fetch_limit_per_hour !== undefined) {
        const n = b.fetch_limit_per_hour as number
        if (n < 1 || n > 10000) {
          return reply(ctx, err(422, 'validation_failed', '请求参数校验未通过', { fetch_limit_per_hour: '每小时拉取上限要在 1 到 10000 之间' }))
        }
      }
      if (b.info_nodes !== undefined) extras.info_nodes = b.info_nodes as boolean
      if (b.protocol_prefix !== undefined) extras.protocol_prefix = b.protocol_prefix as boolean
      if (b.fetch_limit_per_hour !== undefined) extras.fetch_limit_per_hour = b.fetch_limit_per_hour as number
      ctx.send(200, { ...extras })
    },
  },
}
