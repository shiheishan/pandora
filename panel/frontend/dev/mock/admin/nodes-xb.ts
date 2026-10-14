import { randomBytes, randomUUID } from 'node:crypto'
import type { Json, MockContext, MockResult, MockRoute } from '../types.ts'

export interface XBRelay {
  id: string
  name: string
  server_host: string
  server_port: number
  sni: string | null
  host_header: string | null
  enabled: boolean
  sort_order: number
  row_version: number
  created_at: string
  updated_at: string
}

export interface XBState {
  tags: string[]
  limit_bytes: number | null
  reset_day: number | null
  used_bytes: number
  reset_at: string
  exceeded_at: string | null
  schedule: Array<{ start: string; end: string; rate: number }>
  relays: XBRelay[]
  ech: { public_name: string; config_list: string; created_at: string } | null
  ech_retired: number
}

/** nodes.ts 的 Node 在结构上满足它 */
export interface XBHost {
  id: string
  name: string
  row_version: number
  deleted: boolean
  serving_status: string
  node_type: string | null
  protocol_config: unknown
  server_host: string | null
  xb: XBState
}

const NODE_TAGS_MAX = 10
const NODE_TAG_MAX_RUNES = 16
const NODE_RATE_SCHEDULE_MAX = 12
const NODE_TRAFFIC_RESET_DAY_MAX = 31
const RELAYS_PER_NODE_MAX = 20
const NODE_REASON_MAX = 500
const NODE_NAME_MAX = 64
export const TI = 1024 ** 4
export const GI = 1024 ** 3

const text = (v: unknown) => (typeof v === 'string' ? v : '')
const int = (v: unknown) => (typeof v === 'number' && Number.isInteger(v) ? v : null)
const hostRe = /^[A-Za-z0-9.-]+$/

function hasControlRune(s: string): boolean {
  for (const ch of s) {
    const c = ch.codePointAt(0)!
    if (c <= 0x1f || c === 0x7f) return true
  }
  return false
}

function normalizeNodeTags(inTags: string[]): string[] | { msg: string } {
  if (inTags.length === 0) return []
  const out: string[] = []
  const seen = new Set<string>()
  for (let i = 0; i < inTags.length; i++) {
    const idx = i + 1
    const tag = inTags[i]!.trim()
    if (!tag) return { msg: `第 ${idx} 个标签不能为空` }
    if (hasControlRune(tag)) return { msg: `第 ${idx} 个标签含有控制字符` }
    if ([...tag].length > NODE_TAG_MAX_RUNES) return { msg: `第 ${idx} 个标签最多 16 个字` }
    const lower = tag.toLowerCase()
    if (!seen.has(lower)) {
      seen.add(lower)
      out.push(tag)
    }
  }
  if (out.length > NODE_TAGS_MAX) return { msg: '最多 10 个标签' }
  return out
}

function parseHHMMStrict(s: string): number | null {
  if (s.length !== 5 || s[2] !== ':') return null
  for (const i of [0, 1, 3, 4]) {
    const c = s[i]!
    if (c < '0' || c > '9') return null
  }
  const h = Number(s.slice(0, 2))
  const m = Number(s.slice(3, 5))
  if (h < 0 || h > 23 || m < 0 || m > 59) return null
  return h * 60 + m
}

function parseScheduleStart(s: string): number | null {
  const min = parseHHMMStrict(s)
  if (min === null || min < 0 || min > 23 * 60 + 59) return null
  return min
}

function parseScheduleEnd(s: string): number | null {
  if (s === '24:00') return 1440
  const min = parseHHMMStrict(s)
  if (min === null || min < 1 || min > 23 * 60 + 59) return null
  return min
}

function nodeCheckRate(rate: number): boolean {
  if (!Number.isFinite(rate) || rate < 0 || rate > 9999.99) return false
  return Math.round(rate * 100) / 100 === rate
}

export function parseRateSchedule(inSegs: Array<{ start: string; end: string; rate: number }>): { ok: true; sorted: typeof inSegs } | { ok: false; msg: string } {
  if (inSegs.length === 0) return { ok: true, sorted: [] }
  if (inSegs.length > NODE_RATE_SCHEDULE_MAX) return { ok: false, msg: `最多 ${NODE_RATE_SCHEDULE_MAX} 段` }
  const out: Array<{ start: string; end: string; rate: number; startMin: number; endMin: number }> = []
  for (let i = 0; i < inSegs.length; i++) {
    const idx = i + 1
    const seg = inSegs[i]!
    const startMin = parseScheduleStart(seg.start)
    if (startMin === null) return { ok: false, msg: `第 ${idx} 段：时间要写成 HH:MM（如 08:00），结束可写 24:00` }
    const endMin = parseScheduleEnd(seg.end)
    if (endMin === null) return { ok: false, msg: `第 ${idx} 段：时间要写成 HH:MM（如 08:00），结束可写 24:00` }
    if (startMin >= endMin) return { ok: false, msg: `第 ${idx} 段：开始要早于结束；跨零点请拆成两段（如 22:00–24:00 与 00:00–06:00）` }
    if (!nodeCheckRate(seg.rate)) return { ok: false, msg: `第 ${idx} 段：倍率要在 0 到 9999.99 之间，最多两位小数` }
    out.push({ start: seg.start, end: seg.end, rate: seg.rate, startMin, endMin })
  }
  out.sort((a, b) => a.startMin - b.startMin)
  for (let i = 1; i < out.length; i++) {
    if (out[i]!.startMin < out[i - 1]!.endMin) return { ok: false, msg: `第 ${i + 1} 段与第 ${i} 段时间重叠` }
  }
  return { ok: true, sorted: out.map(({ start, end, rate }) => ({ start, end, rate })) }
}

function monthStartIso(): string {
  const now = new Date()
  return new Date(now.getFullYear(), now.getMonth(), 1).toISOString()
}

export const xbDefaults = (): XBState => ({
  tags: [],
  limit_bytes: null,
  reset_day: null,
  used_bytes: 0,
  reset_at: monthStartIso(),
  exceeded_at: null,
  schedule: [],
  relays: [],
  ech: null,
  ech_retired: 0,
})

export const XB_BODY_KEYS = ['tags', 'traffic_limit_bytes', 'traffic_reset_day', 'traffic_rate_schedule'] as const

export const XB_EXCEEDED_NOTE =
  '本周期流量已达上限，已停止下发，节点端也停掉了它的入站；到重置日自动恢复，也可以在节点设置里手动重置或调高上限'

function recalcExceeded(st: XBState): void {
  const now = new Date().toISOString()
  if (st.limit_bytes !== null && st.used_bytes >= st.limit_bytes) st.exceeded_at ??= now
  else st.exceeded_at = null
}

export function xbErrors(body: Json): Record<string, string> {
  const f: Record<string, string> = {}
  if ('tags' in body) {
    if (!Array.isArray(body.tags)) f.tags = '必须是字符串数组'
    else {
      const r = normalizeNodeTags(body.tags.map((t) => String(t)))
      if (!Array.isArray(r)) f.tags = r.msg
    }
  }
  if ('traffic_limit_bytes' in body) {
    const v = body.traffic_limit_bytes
    if (v !== null && (typeof v !== 'number' || !Number.isInteger(v) || v <= 0)) f.traffic_limit_bytes = '必须是正整数（字节）；不限请留空'
  }
  if ('traffic_reset_day' in body) {
    const v = body.traffic_reset_day
    if (v !== null && (typeof v !== 'number' || !Number.isInteger(v) || v < 1 || v > NODE_TRAFFIC_RESET_DAY_MAX)) {
      f.traffic_reset_day = '必须是 1 到 31 之间的整数；不自动重置请留空（短月取月末）'
    }
  }
  if ('traffic_rate_schedule' in body) {
    if (!Array.isArray(body.traffic_rate_schedule)) f.traffic_rate_schedule = '必须是数组'
    else {
      const parsed = parseRateSchedule((body.traffic_rate_schedule as Json[]).map((s) => ({ start: text(s.start), end: text(s.end), rate: Number(s.rate) })))
      if (!parsed.ok) f.traffic_rate_schedule = parsed.msg
    }
  }
  return f
}

export function applyXB(st: XBState, body: Json): void {
  if ('tags' in body && Array.isArray(body.tags)) {
    const r = normalizeNodeTags(body.tags.map((t) => String(t)))
    if (Array.isArray(r)) st.tags = r
  }
  if ('traffic_limit_bytes' in body) st.limit_bytes = body.traffic_limit_bytes === null ? null : int(body.traffic_limit_bytes)
  if ('traffic_reset_day' in body) st.reset_day = body.traffic_reset_day === null ? null : int(body.traffic_reset_day)
  if ('traffic_rate_schedule' in body && Array.isArray(body.traffic_rate_schedule)) {
    const parsed = parseRateSchedule((body.traffic_rate_schedule as Json[]).map((s) => ({ start: text(s.start), end: text(s.end), rate: Number(s.rate) })))
    if (parsed.ok) st.schedule = parsed.sorted
  }
  recalcExceeded(st)
}

function trafficRow(st: XBState) {
  return { limit_bytes: st.limit_bytes, reset_day: st.reset_day, used_bytes: st.used_bytes, reset_at: st.reset_at, exceeded_at: st.exceeded_at }
}

export const xbRow = (st: XBState) => ({
  tags: st.tags,
  traffic: trafficRow(st),
  traffic_rate_schedule: st.schedule,
  relay_count: st.relays.length,
  ech_enabled: st.ech !== null,
})

export const xbAdmin = (st: XBState) => ({ tags: st.tags, traffic: trafficRow(st), traffic_rate_schedule: st.schedule })

function storageTlsOn(nodeType: string | null, cfg: unknown): boolean {
  if (!cfg || typeof cfg !== 'object' || Array.isArray(cfg)) return false
  const c = cfg as Json
  const t = (nodeType ?? '').toLowerCase()
  if (t === 'anytls') return text(c.cert_path).trim() !== ''
  if (t === 'naive') return c.tls === true || c.tls === 1
  const tls = c.tls
  return tls === 1 || tls === true
}

function storageNetwork(cfg: unknown): string {
  if (!cfg || typeof cfg !== 'object' || Array.isArray(cfg)) return 'tcp'
  const n = text((cfg as Json).network).toLowerCase().trim()
  return n || 'tcp'
}

function storageReality(cfg: unknown): boolean {
  if (!cfg || typeof cfg !== 'object' || Array.isArray(cfg)) return false
  return (cfg as Json).tls === 2
}

export function echUnsupportedReason(nodeType: string | null, cfg: unknown): string {
  const t = (nodeType ?? '').toLowerCase()
  switch (t) {
    case 'vless':
    case 'vmess':
    case 'trojan':
      if (storageReality(cfg)) return 'REALITY 节点不支持 ECH（REALITY 自带伪装）'
      if (storageNetwork(cfg) === 'xhttp-h3' || storageNetwork(cfg) === 'mkcp') return 'QUIC 类协议暂不支持 ECH'
      if (!storageTlsOn(t, cfg)) return '只有开启 TLS 的节点能用 ECH'
      return ''
    case 'anytls':
    case 'naive':
      return storageTlsOn(t, cfg) ? '' : '只有开启 TLS 的节点能用 ECH'
    case 'hysteria2':
    case 'tuic':
    case 'juicity':
      return 'QUIC 类协议暂不支持 ECH'
    case 'shadowsocks':
    case 'socks':
    case 'http':
    case 'mieru':
    case 'shadowtls':
      return '该协议不支持 ECH'
    default:
      if (!t) return '该协议不支持 ECH'
      if (storageReality(cfg)) return 'REALITY 节点不支持 ECH（REALITY 自带伪装）'
      return '该协议不支持 ECH'
  }
}

function normalizeEchPublicName(raw: string): string | null {
  const name = raw.toLowerCase().trim()
  if (!name || name.length > 253 || name.includes('*')) return null
  if (/^\d+\.\d+\.\d+\.\d+$/.test(name) || name.includes(':')) return null
  if (!hostRe.test(name)) return null
  return name
}

function echPem(configListB64: string): string {
  const lines = ['-----BEGIN ECH CONFIGS-----']
  for (let i = 0; i < configListB64.length; i += 64) lines.push(configListB64.slice(i, i + 64))
  lines.push('-----END ECH CONFIGS-----')
  return lines.join('\n')
}

function relayOut(r: XBRelay, parentId: string) {
  return { ...r, parent_node_id: parentId }
}

function relayValidateName(raw: string): string | null {
  const name = raw.trim()
  if (!name || [...name].length > NODE_NAME_MAX) return null
  return name
}

function relayValidateHost(raw: string): string | null {
  const host = raw.trim()
  if (!host || host.length > 255 || !hostRe.test(host)) return null
  return host
}

function relayValidateSni(raw: unknown): { ok: true; value: string | null } | { ok: false } {
  if (raw === null) return { ok: true, value: null }
  const v = text(raw).trim()
  if (!v) return { ok: true, value: null }
  if (v.length > 253 || !hostRe.test(v) || /^\d+\.\d+\.\d+\.\d+$/.test(v)) return { ok: false }
  return { ok: true, value: v }
}

function relayValidateHostHeader(raw: unknown): { ok: true; value: string | null } | { ok: false } {
  if (raw === null) return { ok: true, value: null }
  const v = text(raw).trim()
  if (!v) return { ok: true, value: null }
  if (v.length > 255 || !hostRe.test(v)) return { ok: false }
  return { ok: true, value: v }
}

function findRelay(list: XBHost[], relayId: string): { host: XBHost; relay: XBRelay } | undefined {
  for (const host of list) {
    const relay = host.xb.relays.find((r) => r.id === relayId)
    if (relay) return { host, relay }
  }
  return undefined
}

function validateReason(raw: unknown): string | { field: string } {
  const v = text(raw).trim()
  if ([...v].length > NODE_REASON_MAX) return { field: 'reason' }
  return v
}

const err = (status: number, code: string, message: string, fields?: Record<string, string>): MockResult => ({
  status,
  body: { error: { code, message, ...(fields ? { fields } : {}) } },
})
const invalid = (fields: Record<string, string>) => err(422, 'validation_failed', '请求参数校验未通过', fields)
const notFound = () => err(404, 'not_found', '资源不存在或无权访问')
const reply = (ctx: MockContext, r: MockResult) => ctx.send(r.status, r.body)
const unknownField = (body: Json, allowed: readonly string[]) => Object.keys(body).find((k) => !allowed.includes(k)) ?? null

export interface XBRoutesDeps {
  find(id?: string): XBHost | undefined
  list(): XBHost[]
  batchPatch(n: XBHost, set: Json): Record<string, string> | null
  batchApply(n: XBHost, set: Json): void
  batchStatus(rows: XBHost[], to: string): MockResult | null
  batchApplyStatus(rows: XBHost[], to: string): void
}

/**
 * 中转、流量重置、批量修改、ECH 的路由（照 node/handler 的 routes_xb.go）。
 * 中转与 ECH 在 Go 里是别的表、流量重置只改用量列，都不动节点的 row_version，所以这里不调 touch
 */
export function xbRoutes(deps: XBRoutesDeps): Record<string, MockRoute> {
  return {
    'GET /v1/nodes/:id/relays': (ctx) => {
      const n = deps.find(ctx.params.id)
      if (!n) return reply(ctx, notFound())
      const relays = [...n.xb.relays].sort((a, b) => a.sort_order - b.sort_order || a.id.localeCompare(b.id))
      ctx.send(200, { relays: relays.map((r) => relayOut(r, n.id)) })
    },

    'POST /v1/nodes/:id/relays': async (ctx) => {
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('node_relay_create', () => {
        const extra = unknownField(body, ['name', 'server_host', 'server_port', 'sni', 'host_header', 'enabled', 'sort_order'])
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        const n = deps.find(ctx.params.id)
        if (!n) return notFound()
        if (n.serving_status === 'retired') return err(409, 'conflict', '节点已退役，不能再加中转')
        const name = relayValidateName(text(body.name))
        if (!name) return invalid({ name: '名称必须为 1 到 64 个字符' })
        const host = relayValidateHost(text(body.server_host))
        if (!host) return invalid({ server_host: '必须是有效的 IP 或 ASCII 主机名' })
        const port = int(body.server_port)
        if (port === null || port < 1 || port > 65535) return invalid({ server_port: '端口必须在 1 到 65535 之间' })
        const sni = body.sni === undefined ? { ok: true as const, value: null } : relayValidateSni(body.sni)
        if (!sni.ok) return invalid({ sni: '必须是域名；留空沿用父节点' })
        const hostHeader = body.host_header === undefined ? { ok: true as const, value: null } : relayValidateHostHeader(body.host_header)
        if (!hostHeader.ok) return invalid({ host_header: '必须是有效的主机名；留空沿用父节点' })
        if (n.xb.relays.length >= RELAYS_PER_NODE_MAX) return err(409, 'conflict', `每个节点最多 ${RELAYS_PER_NODE_MAX} 条中转`)
        const now = new Date().toISOString()
        const relay: XBRelay = {
          id: randomUUID(),
          name,
          server_host: host,
          server_port: port,
          sni: sni.value,
          host_header: hostHeader.value,
          enabled: body.enabled === undefined ? true : body.enabled === true,
          sort_order: int(body.sort_order) ?? 0,
          row_version: 1,
          created_at: now,
          updated_at: now,
        }
        n.xb.relays.push(relay)
        return { status: 201, body: relayOut(relay, n.id) }
      })
    },

    'PATCH /v1/node-relays/:id': async (ctx) => {
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const extra = unknownField(body, ['row_version', 'name', 'server_host', 'server_port', 'sni', 'host_header', 'enabled', 'sort_order'])
      if (extra) return ctx.fail(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
      const found = findRelay(deps.list(), ctx.params.id)
      if (!found) return reply(ctx, notFound())
      const { host: n, relay } = found
      if (!(typeof body.row_version === 'number' && body.row_version > 0)) return reply(ctx, invalid({ row_version: '必须提供正整数版本号' }))
      if (body.row_version !== relay.row_version) {
        return reply(ctx, err(409, 'conflict', '中转已被其他管理员修改，请刷新后重试', { row_version: `current=${relay.row_version}` }))
      }
      if (body.name !== undefined) {
        const name = relayValidateName(text(body.name))
        if (!name) return reply(ctx, invalid({ name: '名称必须为 1 到 64 个字符' }))
        relay.name = name
      }
      if (body.server_host !== undefined) {
        const h = relayValidateHost(text(body.server_host))
        if (!h) return reply(ctx, invalid({ server_host: '必须是有效的 IP 或 ASCII 主机名' }))
        relay.server_host = h
      }
      if (body.server_port !== undefined) {
        const port = int(body.server_port)
        if (port === null || port < 1 || port > 65535) return reply(ctx, invalid({ server_port: '端口必须在 1 到 65535 之间' }))
        relay.server_port = port
      }
      if ('sni' in body) {
        const sni = relayValidateSni(body.sni)
        if (!sni.ok) return reply(ctx, invalid({ sni: '必须是域名；留空沿用父节点' }))
        relay.sni = sni.value
      }
      if ('host_header' in body) {
        const hh = relayValidateHostHeader(body.host_header)
        if (!hh.ok) return reply(ctx, invalid({ host_header: '必须是有效的主机名；留空沿用父节点' }))
        relay.host_header = hh.value
      }
      if (body.enabled !== undefined) relay.enabled = body.enabled === true
      if (body.sort_order !== undefined) relay.sort_order = int(body.sort_order) ?? relay.sort_order
      relay.row_version += 1
      relay.updated_at = new Date().toISOString()
      ctx.send(200, relayOut(relay, n.id))
    },

    'DELETE /v1/node-relays/:id': async (ctx) => {
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const found = findRelay(deps.list(), ctx.params.id)
      if (!found) return reply(ctx, notFound())
      const { host: n, relay } = found
      if (!(typeof body.row_version === 'number' && body.row_version > 0)) return reply(ctx, invalid({ row_version: '必须提供正整数版本号' }))
      if (body.row_version !== relay.row_version) {
        return reply(ctx, err(409, 'conflict', '中转已被其他管理员修改，请刷新后重试', { row_version: `current=${relay.row_version}` }))
      }
      n.xb.relays = n.xb.relays.filter((r) => r.id !== relay.id)
      ctx.send(200, { deleted: true })
    },

    'POST /v1/nodes/:id/traffic/reset': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('node_traffic_reset', () => {
        const extra = unknownField(body, ['reason'])
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        const reason = validateReason(body.reason)
        if (typeof reason !== 'string') return invalid({ reason: '最多 500 个字符' })
        const n = deps.find(ctx.params.id)
        if (!n) return notFound()
        n.xb.used_bytes = 0
        n.xb.exceeded_at = null
        n.xb.reset_at = new Date().toISOString()
        return { status: 200, body: { ok: true } }
      })
    },

    'POST /v1/nodes/edit:batch': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('node_batch_edit', () => {
        const extra = unknownField(body, ['items', 'set', 'reason'])
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        const items = Array.isArray(body.items) ? (body.items as Json[]) : []
        const set = body.set && typeof body.set === 'object' && !Array.isArray(body.set) ? (body.set as Json) : {}
        const setExtra = unknownField(set, ['pool_id', 'traffic_rate', 'tags', 'serving_status', 'traffic_limit_bytes', 'traffic_reset_day', 'traffic_rate_schedule'])
        if (setExtra) return err(400, 'bad_request', `请求体包含未知字段 "${setExtra}"`)
        if (items.length < 1 || items.length > 100) return invalid({ items: '必须包含 1 到 100 个节点，ID 不重复且带 row_version' })
        const ids = new Set<string>()
        for (const it of items) {
          const id = text(it.id)
          if (!id || ids.has(id)) return invalid({ items: '必须包含 1 到 100 个节点，ID 不重复且带 row_version' })
          ids.add(id)
        }
        const reason = validateReason(body.reason)
        if (typeof reason !== 'string') return invalid({ reason: '最多 500 个字符' })
        const hasSet =
          'pool_id' in set ||
          'traffic_rate' in set ||
          'tags' in set ||
          'serving_status' in set ||
          'traffic_limit_bytes' in set ||
          'traffic_reset_day' in set ||
          'traffic_rate_schedule' in set
        if (!hasSet) return invalid({ set: '至少要改一个字段' })
        const rows: XBHost[] = []
        for (const it of items) {
          const n = deps.find(text(it.id))
          if (!n) return notFound()
          if (it.row_version !== n.row_version) {
            return err(409, 'conflict', `节点「${n.name}」：节点已被其他管理员修改，请刷新后重试`, { row_version: `current=${n.row_version}`, node_id: n.id })
          }
          rows.push(n)
        }
        const toStatus = text(set.serving_status)
        if (toStatus) {
          const st = deps.batchStatus(rows, toStatus)
          if (st) return st
        }
        for (const n of rows) {
          const patchErr = deps.batchPatch(n, set)
          if (patchErr) {
            const fields: Record<string, string> = { node_id: n.id, ...patchErr }
            const first = Object.values(patchErr)[0]!
            return err(422, 'validation_failed', `节点「${n.name}」：${first}`, fields)
          }
        }
        for (const n of rows) deps.batchApply(n, set)
        if (toStatus) deps.batchApplyStatus(rows, toStatus)
        return { status: 200, body: { ok: true, updated: rows.length } }
      })
    },

    'GET /v1/nodes/:id/ech': (ctx) => {
      const n = deps.find(ctx.params.id)
      if (!n) return reply(ctx, notFound())
      const unsupported = echUnsupportedReason(n.node_type, n.protocol_config)
      const enabled = n.xb.ech !== null
      ctx.send(200, {
        supported: unsupported === '',
        unsupported_reason: unsupported,
        enabled,
        public_name: enabled && n.xb.ech ? n.xb.ech.public_name : '',
        config_list: enabled && n.xb.ech ? n.xb.ech.config_list : null,
        config_pem: enabled && n.xb.ech ? echPem(n.xb.ech.config_list) : '',
        created_at: enabled && n.xb.ech ? n.xb.ech.created_at : null,
        retired_keys: n.xb.ech_retired,
      })
    },

    'POST /v1/nodes/:id/ech': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('node_ech_generate', () => {
        const extra = unknownField(body, ['public_name'])
        if (extra) return err(400, 'bad_request', `请求体包含未知字段 "${extra}"`)
        const n = deps.find(ctx.params.id)
        if (!n) return notFound()
        if (n.serving_status === 'retired') return err(409, 'conflict', '节点已退役')
        const unsupported = echUnsupportedReason(n.node_type, n.protocol_config)
        if (unsupported) return err(409, 'conflict', unsupported)
        let normInput = text(body.public_name).trim()
        if (!normInput) normInput = n.xb.ech?.public_name ?? text(n.server_host)
        const norm = normalizeEchPublicName(normInput)
        if (!norm) return invalid({ public_name: '必须是域名（客户端握手外层明文里的 SNI，通常填一个与节点同证书的域名）' })
        if (n.xb.ech) n.xb.ech_retired += 1
        const config_list = randomBytes(40).toString('base64')
        n.xb.ech = { public_name: norm, config_list, created_at: new Date().toISOString() }
        return {
          status: 200,
          body: {
            supported: true,
            unsupported_reason: '',
            enabled: true,
            public_name: norm,
            config_list,
            config_pem: echPem(config_list),
            created_at: n.xb.ech.created_at,
            retired_keys: n.xb.ech_retired,
          },
        }
      })
    },

    'DELETE /v1/nodes/:id/ech': (ctx) => {
      if (!ctx.requireReauth()) return
      const n = deps.find(ctx.params.id)
      if (!n) return reply(ctx, notFound())
      n.xb.ech = null
      n.xb.ech_retired = 0
      ctx.send(200, { ok: true })
    },
  }
}
