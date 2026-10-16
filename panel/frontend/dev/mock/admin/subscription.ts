import type { Json, MockContext, MockModule, MockResult } from '../types.ts'

// ---------------------------------------------------------------------------
// 订阅附加设置（settings），形状与 Go 一致。
// 订阅模板接口（/v1/subscription-templates）后端仍在，但 10-16 起后台只读列出「客户端 → 格式」、不再调用，假后端不再模拟。
// ---------------------------------------------------------------------------
const err = (status: number, code: string, message: string, fields?: Record<string, string>): MockResult => ({ status, body: { error: { code, message, ...(fields ? { fields } : {}) } } })
const reply = (ctx: MockContext, r: MockResult) => ctx.send(r.status, r.body)

type Decoded = { ok: true; body: Json } | { ok: false; result: MockResult }
async function decode(ctx: MockContext, shape: Record<string, 'boolean' | 'strings' | 'string'>): Promise<Decoded> {
  const bad = (message = '请求体不是合法的 JSON'): Decoded => ({ ok: false, result: err(400, 'bad_request', message) })
  const body = await ctx.body()
  if (!body) return bad()
  for (const k of Object.keys(body)) {
    if (!(k in shape)) return bad()
    const want = shape[k]!
    const v = body[k]
    if (want === 'boolean' && typeof v !== 'boolean') return bad(`字段 "${k}" 类型不正确`)
    if (want === 'string' && typeof v !== 'string') return bad(`字段 "${k}" 类型不正确`)
    if (want === 'strings' && (!Array.isArray(v) || v.some((x) => typeof x !== 'string'))) return bad(`字段 "${k}" 类型不正确`)
  }
  return { ok: true, body }
}

const extras = { info_nodes: false, protocol_prefix: false }

export const subscriptionModule: MockModule = {
  routes: {
    'GET /v1/settings/subscription': (ctx) => {
      ctx.send(200, { ...extras })
    },
    'POST /v1/settings/subscription': async (ctx) => {
      if (!ctx.requireReauth()) return
      const d = await decode(ctx, { info_nodes: 'boolean', protocol_prefix: 'boolean' })
      if (!d.ok) return reply(ctx, d.result)
      const b = d.body
      if (b.info_nodes !== undefined) extras.info_nodes = b.info_nodes as boolean
      if (b.protocol_prefix !== undefined) extras.protocol_prefix = b.protocol_prefix as boolean
      ctx.send(200, { ...extras })
    },
  },
}
