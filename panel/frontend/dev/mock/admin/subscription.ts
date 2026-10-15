import type { Json, MockContext, MockModule, MockResult } from '../types.ts'

// ---------------------------------------------------------------------------
// 订阅附加设置（settings）与订阅模板（subscription_templates），形状与 Go 一致。
// ---------------------------------------------------------------------------
const MAX_TEMPLATE_BYTES = 262144
const FORMATS = ['clash', 'clash-premium', 'stash', 'singbox', 'surfboard', 'loon'] as const
type SubFormat = (typeof FORMATS)[number]

const err = (status: number, code: string, message: string, fields?: Record<string, string>): MockResult => ({ status, body: { error: { code, message, ...(fields ? { fields } : {}) } } })
const invalid = (fields: Record<string, string>) => err(422, 'validation_failed', '请求参数校验未通过', fields)
const NOT_FOUND = err(404, 'not_found', '资源不存在或无权访问')
const reply = (ctx: MockContext, r: MockResult) => ctx.send(r.status, r.body)
const iso = () => new Date().toISOString()
const bytes = (s: string) => new TextEncoder().encode(s).length

const DEFAULT_CLASH = `mixed-port: 7890
allow-lan: false
mode: rule
log-level: info

{{proxies}}

proxy-groups:
{{proxy_groups}}

rules:
  - GEOIP,CN,DIRECT
  - MATCH,节点选择
`

const DEFAULT_SURFBOARD = `#!MANAGED-CONFIG {{subscribe_url}} interval=43200 strict=false

[General]
loglevel = notify

[Proxy]
{{proxies}}

[Proxy Group]
{{proxy_groups}}

[Rule]
GEOIP,CN,DIRECT
FINAL,节点选择
`

const DEFAULT_LOON = '{{proxies}}\n'

const DEFAULT_SINGBOX = `{
  "log": {
    "level": "info"
  },
  "outbounds": [
    "{{outbounds}}"
  ],
  "route": {
    "final": "节点选择"
  }
}
`

interface Placeholder {
  token: string
  required: boolean
  desc: string
}

const CLASH_PH: readonly Placeholder[] = [
  { token: '{{proxies}}', required: true, desc: '整份 proxies 段，单独一行顶格' },
  { token: '{{proxy_groups}}', required: true, desc: '两个代理组：自动选择、节点选择' },
  { token: '{{proxy_names}}', required: false, desc: '节点名列表，给自定义组用' },
]

const PLACEHOLDERS: Readonly<Record<SubFormat, readonly Placeholder[]>> = {
  clash: CLASH_PH,
  'clash-premium': CLASH_PH,
  stash: CLASH_PH,
  surfboard: [
    { token: '{{proxies}}', required: true, desc: '全部节点（单独一行）' },
    { token: '{{proxy_groups}}', required: true, desc: '策略组（单独一行）' },
    { token: '{{proxy_names}}', required: false, desc: '节点名列表（行内）' },
    { token: '{{subscribe_url}}', required: false, desc: '这份订阅的链接' },
  ],
  loon: [{ token: '{{proxies}}', required: true, desc: '全部节点（单独一行）' }],
  singbox: [
    { token: '"{{outbounds}}"', required: true, desc: '全部出站（含节点选择、自动选择、direct）' },
    { token: '"{{proxy_tags}}"', required: false, desc: '节点的 tag 列表' },
  ],
}

function defaultTemplate(format: SubFormat): string {
  switch (format) {
    case 'clash':
    case 'clash-premium':
    case 'stash':
      return DEFAULT_CLASH
    case 'surfboard':
      return DEFAULT_SURFBOARD
    case 'loon':
      return DEFAULT_LOON
    case 'singbox':
      return DEFAULT_SINGBOX
  }
}

function parseFormat(raw: string): SubFormat | null {
  const f = raw.trim() as SubFormat
  return FORMATS.includes(f) ? f : null
}

function contentType(format: SubFormat): string {
  if (format === 'singbox') return 'application/json; charset=utf-8'
  if (format === 'clash' || format === 'clash-premium' || format === 'stash') return 'text/yaml; charset=utf-8'
  return 'text/plain; charset=utf-8'
}

const SAMPLE_NAMES = ['示例 · 香港 01', '示例 · 日本 01', '示例 · 美国 01', '示例 · 新加坡 01']
const SAMPLE_PROXIES_CLASH = `proxies:
  - {name: ${JSON.stringify(SAMPLE_NAMES[0])}, type: ss, server: hk.example.com, port: 8388, cipher: aes-128-gcm, password: demo}
  - {name: ${JSON.stringify(SAMPLE_NAMES[1])}, type: vmess, server: jp.example.com, port: 443, uuid: 00000000-0000-4000-8000-000000000000, alterId: 0, cipher: auto, tls: true}
  - {name: ${JSON.stringify(SAMPLE_NAMES[2])}, type: trojan, server: us.example.com, port: 443, password: demo, sni: us.example.com}
  - {name: ${JSON.stringify(SAMPLE_NAMES[3])}, type: hysteria2, server: sg.example.com, port: 443, password: demo}`
const SAMPLE_PROXY_GROUPS = `  - {name: 自动选择, type: url-test, url: 'http://www.gstatic.com/generate_204', interval: 300, proxies: [${SAMPLE_NAMES.map((n) => JSON.stringify(n)).join(', ')}]}
  - {name: 节点选择, type: select, proxies: [自动选择, ${SAMPLE_NAMES.map((n) => JSON.stringify(n)).join(', ')}]}`
const SAMPLE_PROXY_NAMES = SAMPLE_NAMES.map((n) => JSON.stringify(n)).join(', ')
const SAMPLE_SUBSCRIBE_URL = 'https://sub.example.com/000000000000/preview'
const SAMPLE_OUTBOUND = '{"type":"shadowsocks","tag":"示例 · 香港 01","server":"hk.example.com","server_port":8388,"method":"aes-128-gcm","password":"demo"}'

function previewBody(format: SubFormat, tmpl: string): string {
  const map: Record<string, string> = {
    '{{proxies}}': format === 'loon' || format === 'surfboard' ? SAMPLE_NAMES.map((n) => `${n} = ss, hk.example.com, 8388, aes-128-gcm, demo`).join('\n') : SAMPLE_PROXIES_CLASH,
    '{{proxy_groups}}': SAMPLE_PROXY_GROUPS,
    '{{proxy_names}}': SAMPLE_PROXY_NAMES,
    '{{subscribe_url}}': SAMPLE_SUBSCRIBE_URL,
    '"{{outbounds}}"': SAMPLE_OUTBOUND,
    '"{{proxy_tags}}"': JSON.stringify(SAMPLE_NAMES[0]),
  }
  let out = tmpl
  const keys = Object.keys(map).sort((a, b) => b.length - a.length || a.localeCompare(b))
  for (const k of keys) out = out.split(k).join(map[k]!)
  return out
}

function validateTemplate(format: SubFormat, content: string): Record<string, string> | null {
  if (!content.trim()) return { content: '模板不能为空' }
  if (bytes(content) > MAX_TEMPLATE_BYTES) return { content: '模板不能超过 256 KB' }
  for (const p of PLACEHOLDERS[format]) {
    if (p.required && !content.includes(p.token)) return { content: '缺少占位 ' + p.token }
    // 与后端 ValidateTemplate 一致：必需占位只能出现一次
    if (p.required && content.split(p.token).length > 2) return { content: p.token + ' 只能出现一次' }
  }
  return null
}

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
const custom = new Map<string, { content: string; updated_at: string }>()

function templateDetail(format: SubFormat) {
  const row = custom.get(format)
  const def = defaultTemplate(format)
  return {
    format,
    customized: row !== undefined,
    content: row?.content ?? def,
    default: def,
    updated_at: row?.updated_at ?? null,
    placeholders: PLACEHOLDERS[format].map((p) => ({ token: p.token, required: p.required, desc: p.desc })),
    max_bytes: MAX_TEMPLATE_BYTES,
  }
}

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

    'GET /v1/subscription-templates': (ctx) => {
      const templates = FORMATS.map((format) => {
        const row = custom.get(format)
        return {
          format,
          customized: row !== undefined,
          bytes: row ? bytes(row.content) : 0,
          updated_at: row?.updated_at ?? null,
        }
      })
      ctx.send(200, { templates })
    },
    'GET /v1/subscription-templates/:format': (ctx) => {
      const format = parseFormat(ctx.params.format!)
      if (!format) return reply(ctx, NOT_FOUND)
      ctx.send(200, templateDetail(format))
    },
    'PUT /v1/subscription-templates/:format': async (ctx) => {
      if (!ctx.requireReauth()) return
      const format = parseFormat(ctx.params.format!)
      if (!format) return reply(ctx, NOT_FOUND)
      const d = await decode(ctx, { content: 'string' })
      if (!d.ok) return reply(ctx, d.result)
      const content = typeof d.body.content === 'string' ? d.body.content : ''
      const fields = validateTemplate(format, content)
      if (fields) return reply(ctx, invalid(fields))
      const updated_at = iso()
      custom.set(format, { content, updated_at })
      ctx.send(200, templateDetail(format))
    },
    'DELETE /v1/subscription-templates/:format': (ctx) => {
      if (!ctx.requireReauth()) return
      const format = parseFormat(ctx.params.format!)
      if (!format) return reply(ctx, NOT_FOUND)
      custom.delete(format)
      ctx.send(200, templateDetail(format))
    },
    'POST /v1/subscription-templates/:format/preview': async (ctx) => {
      const format = parseFormat(ctx.params.format!)
      if (!format) return reply(ctx, NOT_FOUND)
      const d = await decode(ctx, { content: 'string' })
      if (!d.ok) return reply(ctx, d.result)
      const raw = typeof d.body.content === 'string' ? d.body.content : ''
      let content = defaultTemplate(format)
      if (raw.trim()) {
        const fields = validateTemplate(format, raw)
        if (fields) return reply(ctx, invalid(fields))
        content = raw
      }
      const body = previewBody(format, content)
      ctx.send(200, { body, content_type: contentType(format), node_count: 4 })
    },
  },
}
