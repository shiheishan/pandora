/**
 * [INPUT]: 依赖 node:crypto 的 randomUUID，依赖 ../types 的 Json / MockResult / MockRoute
 * [OUTPUT]: 对外提供 packRoutes（由 plans.ts 展开进 plans 模块）
 * [POS]: dev/mock/admin 的「套餐（后台-04）· 流量包」四接口（修订 R73）：列表（status 筛选，在售在前再按 sort_order、创建时间）、新建（即在售）、修改（expected_updated_at 乐观锁，409 fields.updated_at = "current=<RFC3339Nano>"）、上下架（已是目标状态 409）。catalog.read / catalog.publish + reauth + 幂等 scope 照契约，校验键名与文案照 adminops/traffic_packs.go；新建、修改、上架受销售开关控制，下架不受；按 DisallowUnknownFields 拒绝未知字段
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomUUID } from 'node:crypto'
import type { Json, MockResult, MockRoute } from '../types.ts'

const GiB = 1024 ** 3
const DAY = 86_400_000
const err = (status: number, code: string, message: string, fields?: Record<string, string>): MockResult => ({ status, body: { error: { code, message, ...(fields ? { fields } : {}) } } })
const isInt = (v: unknown): v is number => typeof v === 'number' && Number.isInteger(v)

interface Pack {
  id: string
  name: string
  traffic_bytes: number
  currency: string
  unit_amount: number
  recommended: boolean
  status: 'active' | 'archived'
  sort_order: number
  sold_count: number
  created_at: string
  updated_at: string
}

// updated_at 每次写都要变（后端触发器改写成 now()），同一毫秒里连写两次也不能撞
let clock = Date.now()
const stamp = () => new Date((clock = Math.max(clock + 1, Date.now()))).toISOString()

const seed = (name: string, gb: number, currency: string, amount: number, sort: number, sold: number, over: Partial<Pack> = {}): Pack => {
  const at = new Date(Date.now() - (60 - sort) * DAY).toISOString()
  return {
    id: randomUUID(),
    name,
    traffic_bytes: gb * GiB,
    currency,
    unit_amount: amount,
    recommended: false,
    status: 'active',
    sort_order: sort,
    sold_count: sold,
    created_at: at,
    updated_at: at,
    ...over,
  }
}

const packs: Pack[] = [
  seed('50 GB 加油包', 50, 'CNY', 1500, 10, 412),
  seed('100 GB 加油包', 100, 'CNY', 2500, 20, 968, { recommended: true }),
  seed('300 GB 大流量包', 300, 'CNY', 6000, 30, 137),
  seed('100 GB Top-up', 100, 'USD', 390, 40, 21),
  seed('1 TB 年度包', 1024, 'CNY', 16800, 50, 9, { status: 'archived' }),
]

const KEYS = ['name', 'traffic_bytes', 'currency', 'unit_amount', 'recommended', 'sort_order']

/** validatePackInput：键名与文案同后端 */
function problems(b: Json): Record<string, string> {
  const f: Record<string, string> = {}
  const name = typeof b.name === 'string' ? [...b.name.trim()].length : 0
  if (name < 1 || name > 60) f.name = '名称 1 到 60 个字'
  if (!(isInt(b.traffic_bytes) && b.traffic_bytes >= 1 && b.traffic_bytes <= 2 ** 50)) f.traffic_bytes = '容量必须大于 0，且不超过 1 PiB'
  if (!['CNY', 'USD'].includes(String(b.currency))) f.currency = '仅允许 CNY 或 USD'
  if (!(isInt(b.unit_amount) && b.unit_amount >= 1 && b.unit_amount <= 100_000_000)) f.unit_amount = '价格必须大于 0，且不超过 100 万'
  if (b.sort_order !== undefined && !(isInt(b.sort_order) && Math.abs(b.sort_order) <= 1_000_000)) f.sort_order = '排序号超出范围'
  if (b.recommended !== undefined && typeof b.recommended !== 'boolean') f.recommended = '必须是布尔值'
  return f
}

function unknownField(body: Json, allowed: readonly string[]): MockResult | null {
  const extra = Object.keys(body).find((k) => !allowed.includes(k))
  return extra ? err(400, 'bad_request', `请求体包含未知字段 "${extra}"`) : null
}

const invalid = (fields: Record<string, string>) => err(422, 'validation_failed', '请求参数校验未通过', fields)
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const SALES_OFF = err(503, 'service_unavailable', '服务暂时不可用')
const NOT_FOUND = err(404, 'not_found', '资源不存在或无权访问')

function findPack(id: string | undefined): Pack | null {
  return id && UUID.test(id) ? (packs.find((p) => p.id === id) ?? null) : null
}

/** expected_updated_at 与当前不一致：409 fields.updated_at = "current=<RFC3339Nano>" */
function staleCheck(p: Pack, b: Json): MockResult | null {
  if (typeof b.expected_updated_at !== 'string' || !b.expected_updated_at) return invalid({ expected_updated_at: '必填：列表里读到的 updated_at' })
  if (Date.parse(b.expected_updated_at) !== Date.parse(p.updated_at)) return err(409, 'conflict', '流量包已被其他管理员修改，请刷新后重试', { updated_at: `current=${p.updated_at}` })
  return null
}

export function packRoutes(salesEnabled: () => boolean): Record<string, MockRoute> {
  return {
    'GET /v1/traffic-packs': (ctx) => {
      if (!ctx.requirePermission('catalog.read')) return
      const status = ctx.query.get('status') ?? ''
      if (status && status !== 'active' && status !== 'archived') return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { status: '只能是 active 或 archived' })
      const rows = packs
        .filter((p) => !status || p.status === status)
        .sort((a, b) => (a.status === b.status ? 0 : a.status === 'active' ? -1 : 1) || a.sort_order - b.sort_order || a.created_at.localeCompare(b.created_at))
      ctx.send(200, { packs: rows })
    },

    'POST /v1/traffic-packs': async (ctx) => {
      if (!ctx.requirePermission('catalog.publish') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('catalog_traffic_pack_create', () => {
        const bad = unknownField(body, KEYS)
        if (bad) return bad
        const f = problems(body)
        if (Object.keys(f).length) return invalid(f)
        if (!salesEnabled()) return SALES_OFF
        const at = stamp()
        const pack: Pack = {
          id: randomUUID(),
          name: String(body.name).trim(),
          traffic_bytes: body.traffic_bytes as number,
          currency: String(body.currency),
          unit_amount: body.unit_amount as number,
          recommended: body.recommended === true,
          status: 'active',
          sort_order: isInt(body.sort_order) ? body.sort_order : 0,
          sold_count: 0,
          created_at: at,
          updated_at: at,
        }
        packs.push(pack)
        return { status: 201, body: { pack } }
      })
    },

    'PUT /v1/traffic-packs/:id': async (ctx) => {
      if (!ctx.requirePermission('catalog.publish') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('catalog_traffic_pack_update', () => {
        const p = findPack(ctx.params.id)
        if (!p) return NOT_FOUND
        const bad = unknownField(body, [...KEYS, 'expected_updated_at'])
        if (bad) return bad
        const f = problems(body)
        if (Object.keys(f).length) return invalid(f)
        const staleness = staleCheck(p, body)
        if (staleness) return staleness
        if (!salesEnabled()) return SALES_OFF
        Object.assign(p, {
          name: String(body.name).trim(),
          traffic_bytes: body.traffic_bytes,
          currency: String(body.currency),
          unit_amount: body.unit_amount,
          recommended: body.recommended === true,
          sort_order: isInt(body.sort_order) ? body.sort_order : 0,
          updated_at: stamp(),
        })
        return { status: 200, body: { pack: p } }
      })
    },

    'POST /v1/traffic-packs/:id/status': async (ctx) => {
      if (!ctx.requirePermission('catalog.publish') || !ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      await ctx.idempotent('catalog_traffic_pack_status', () => {
        const p = findPack(ctx.params.id)
        if (!p) return NOT_FOUND
        const bad = unknownField(body, ['status', 'expected_updated_at'])
        if (bad) return bad
        if (body.status !== 'active' && body.status !== 'archived') return invalid({ status: '只能是 active 或 archived' })
        const staleness = staleCheck(p, body)
        if (staleness) return staleness
        if (p.status === body.status) return err(409, 'conflict', body.status === 'active' ? '流量包已经在售' : '流量包已经下架')
        // 上架受销售开关控制，下架不受（与归档套餐一致）
        if (body.status === 'active' && !salesEnabled()) return SALES_OFF
        p.status = body.status
        p.updated_at = stamp()
        return { status: 200, body: { pack: p } }
      })
    },
  }
}
