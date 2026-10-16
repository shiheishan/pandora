import { randomUUID } from 'node:crypto'
import type { Json, MockContext, MockModule, MockResult, MockRoute } from '../types.ts'
import { deliverableNodesInPool } from './nodes.ts'
import { activeNodesInPool, pools } from './nodes-infra.ts'
import {
  autoCode,
  detail,
  err,
  find,
  invalid,
  isInt,
  listRow,
  NOT_FOUND,
  plans,
  poolProblems,
  prepareBasic,
  publishProblems,
  stale,
  str,
  unknownField,
  UUID,
  validateOneTime,
  validateReset,
  validateSales,
  validateTierChanges,
  validateTiers,
  validateUsage,
  type Plan,
  type Price,
  type Version,
} from './plans-store.ts'

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------
function checkExpected(expected: unknown): Record<string, string> | null {
  if (!isInt(expected) || expected <= 0) {
    return { expected_row_version: '必须为正整数' }
  }
  return null
}

function lockEditablePlan(p: Plan, expected: number): MockResult | null {
  if (p.status === 'archived') {
    return err(409, 'conflict', '已归档套餐不能编辑')
  }
  if (p.row_version !== expected) {
    return stale(p.row_version)
  }
  return null
}

function parseUUIDList(field: string, raw: unknown): { ids: string[]; error?: Record<string, string> } {
  if (raw === undefined || raw === null) return { ids: [] }
  if (!Array.isArray(raw) || raw.some((x) => typeof x !== 'string' || !UUID.test(x)) || new Set(raw).size !== raw.length) {
    return { ids: [], error: { [field]: '必须是无重复的 UUID 列表' } }
  }
  return { ids: raw as string[] }
}

async function withPlan(
  ctx: MockContext,
  scope: string,
  run: (p: Plan, body: Json) => MockResult | Promise<MockResult>,
): Promise<void> {
  if (!ctx.requireReauth()) return
  const body = await ctx.body()
  if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
  await ctx.idempotent(scope, () => {
    const id = ctx.params.id ?? ''
    if (!UUID.test(id)) return NOT_FOUND
    const p = find(id)
    if (!p) return NOT_FOUND
    return run(p, body)
  })
}

// ---------------------------------------------------------------------------
// 路由定义
// ---------------------------------------------------------------------------
const routes: Record<string, MockRoute> = {
  // 1. GET /v1/plans
  'GET /v1/plans': (ctx) => {
    const kind = ctx.query.get('kind') ?? ''
    const status = ctx.query.get('status') ?? ''
    const q = (ctx.query.get('q') ?? '').trim()

    const fields: Record<string, string> = {}
    if (kind !== '' && kind !== 'subscription' && kind !== 'one_time') {
      fields.kind = '不支持的套餐类型'
    }
    if (status !== '' && status !== 'draft' && status !== 'active' && status !== 'archived') {
      fields.status = '不支持的套餐状态'
    }
    if ([...q].length > 100) {
      fields.q = '最多 100 个字符'
    }
    if (Object.keys(fields).length > 0) {
      return ctx.fail(422, 'validation_failed', '请求参数校验未通过', fields)
    }

    const qLower = q.toLowerCase()
    const rows = [...plans]
      .filter((p) => {
        if (kind && p.kind !== kind) return false
        if (status && p.status !== status) return false
        if (qLower && !p.name.toLowerCase().includes(qLower) && !p.code.toLowerCase().includes(qLower)) return false
        return true
      })
      .sort((a, b) => a.created_at.localeCompare(b.created_at) || a.id.localeCompare(b.id))
      .map(listRow)

    ctx.send(200, { plans: rows })
  },

  // 13. GET /v1/plans/:id/pools（字面段在通用写接口之前）
  'GET /v1/plans/:id/pools': (ctx) => {
    const id = ctx.params.id ?? ''
    if (!UUID.test(id)) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
    const p = find(id)
    if (!p) return ctx.fail(404, 'not_found', '资源不存在或无权访问')

    const boundSet = new Set(p.pool_ids)
    const activePools = pools
      .filter((x) => x.status !== 'disabled')
      .map((x) => ({
        id: x.id,
        name: x.name,
        active_nodes: activeNodesInPool(x.id),
        deliverable_nodes: deliverableNodesInPool(x.id),
        bound: boundSet.has(x.id),
      }))

    ctx.send(200, { pools: activePools })
  },

  // 2. GET /v1/plans/:id
  'GET /v1/plans/:id': (ctx) => {
    const id = ctx.params.id ?? ''
    if (!UUID.test(id)) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
    const p = find(id)
    if (!p) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
    ctx.send(200, { plan: detail(p) })
  },

  // 3. POST /v1/plans
  'POST /v1/plans': async (ctx) => {
    if (!ctx.requireReauth()) return
    const body = await ctx.body()
    if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')

    await ctx.idempotent('catalog_plan_create', () => {
      const bad = unknownField(body, [
        'kind',
        'name',
        'description',
        'prices',
        'unit_amount',
        'validity_days',
        'traffic_bytes',
        'max_devices',
        'throttle_kbps',
        'pool_ids',
        'reset',
        'visibility',
        'visible_group_ids',
        'allow_new_purchase',
        'allow_renewal',
        'allow_upgrade',
        'recommended',
        'publish',
      ])
      if (bad) return bad

      const kind = str(body.kind)
      const fields: Record<string, string> = {}

      // 类型与价格校验
      let tiersToCreate: Array<{ billing_interval: 'month' | 'year'; interval_count: number; unit_amount: number }> = []
      let oneTimeToCreate: { unit_amount: number; validity_days: number | null } | null = null

      if (kind === 'subscription') {
        const rawTiers = Array.isArray(body.prices) ? (body.prices as Json[]) : []
        const validTiers = rawTiers.filter((t) => t?.unit_amount !== null && t?.unit_amount !== undefined)
        validateTiers(validTiers, fields)
        if (body.unit_amount !== undefined && body.unit_amount !== null) {
          fields.unit_amount = '订阅套餐按周期定价，不能设一次性价格'
        }
        tiersToCreate = validTiers.map((t) => ({
          billing_interval: str(t.billing_interval) as 'month' | 'year',
          interval_count: Number(t.interval_count),
          unit_amount: Number(t.unit_amount),
        }))
      } else if (kind === 'one_time') {
        if (body.unit_amount === undefined || body.unit_amount === null) {
          fields.unit_amount = '一次性套餐必须设价格'
        } else {
          validateOneTime(body, fields)
          oneTimeToCreate = {
            unit_amount: Number(body.unit_amount),
            validity_days: (body.validity_days as number | null) ?? null,
          }
        }
        if (Array.isArray(body.prices) && (body.prices as unknown[]).length > 0) {
          fields.prices = '一次性套餐只有一个价格，不能设周期价'
        }
      } else {
        fields.kind = '套餐类型只能是订阅套餐或一次性套餐'
      }

      const basic = prepareBasic(body.name, body.description, fields)
      validateUsage(body, fields)

      const parsedPools = parseUUIDList('pool_ids', body.pool_ids)
      if (parsedPools.error) {
        Object.assign(fields, parsedPools.error)
      } else {
        const poolErr = poolProblems(parsedPools.ids)
        if (poolErr) fields.pool_ids = poolErr
      }

      if (body.reset !== undefined && body.reset !== null) {
        if (typeof body.reset !== 'object' || Array.isArray(body.reset)) {
          fields.reset = '必须为对象'
        } else {
          const r = body.reset as Json
          validateReset(r.price, r.limit, fields)
        }
      }

      const parsedGroups = parseUUIDList('visible_group_ids', body.visible_group_ids)
      if (parsedGroups.error) {
        Object.assign(fields, parsedGroups.error)
      }

      // planCreateFrom 的 allow_* 缺省处理
      const allowNewPurchase = body.allow_new_purchase === undefined ? true : Boolean(body.allow_new_purchase)
      const allowUpgrade = body.allow_upgrade === undefined ? true : Boolean(body.allow_upgrade)
      const allowRenewal = body.allow_renewal !== undefined ? Boolean(body.allow_renewal) : kind === 'subscription'

      const salesInput = {
        visibility: body.visibility,
        visible_group_ids: parsedGroups.ids,
        allow_new_purchase: allowNewPurchase,
        allow_renewal: allowRenewal,
        allow_upgrade: allowUpgrade,
        recommended: Boolean(body.recommended),
      }
      validateSales(kind, salesInput, fields)

      if (Object.keys(fields).length > 0) {
        return invalid(fields)
      }

      // 发布前置检查（publish: true 时失败回 422 且什么都不留）
      const willPublish = body.publish === true
      const pricesForCheck: Price[] = []
      const nowStr = new Date().toISOString()

      if (kind === 'subscription') {
        tiersToCreate.forEach((t) => {
          pricesForCheck.push({
            id: randomUUID(),
            billing_interval: t.billing_interval,
            interval_count: t.interval_count,
            unit_amount: t.unit_amount,
            validity_days: null,
            status: 'active',
            created_at: nowStr,
            currency: 'CNY',
            user_group_id: null,
          })
        })
      } else if (oneTimeToCreate) {
        pricesForCheck.push({
          id: randomUUID(),
          billing_interval: 'one_time',
          interval_count: 1,
          unit_amount: oneTimeToCreate.unit_amount,
          validity_days: oneTimeToCreate.validity_days,
          status: 'active',
          created_at: nowStr,
          currency: 'CNY',
          user_group_id: null,
        })
      }

      if (willPublish) {
        const pubProb = publishProblems({ prices: pricesForCheck, pool_ids: parsedPools.ids })
        if (pubProb) {
          return invalid(pubProb)
        }
      }

      // 生成 code 并建 plan
      const allCodes = plans.map((p) => p.code)
      const code = autoCode(basic.name, allCodes)
      const planId = randomUUID()
      const versionId = randomUUID()

      const v1: Version = {
        id: versionId,
        version: 1,
        status: willPublish ? 'published' : 'draft',
        published_at: willPublish ? nowStr : null,
        traffic_bytes: (body.traffic_bytes as number | null) ?? null,
        max_devices: (body.max_devices as number | null) ?? null,
        throttle_kbps: (body.throttle_kbps as number | null) ?? null,
        created_by_email: ctx.user.email,
        created_at: nowStr,
      }

      let resetPrice: number | null = null
      let resetLimit: number | null = null
      if (body.reset !== undefined && body.reset !== null) {
        const r = body.reset as Json
        resetPrice = Number(r.price)
        resetLimit = (r.limit as number | null) ?? null
      }

      const newPlan: Plan = {
        id: planId,
        row_version: 1,
        code,
        name: basic.name,
        description: basic.description,
        kind: kind as 'subscription' | 'one_time',
        status: willPublish ? 'active' : 'draft',
        visibility: (salesInput.visibility as 'public' | 'authenticated' | 'group' | 'hidden') || 'public',
        visible_group_ids: salesInput.visible_group_ids,
        allow_new_purchase: salesInput.allow_new_purchase,
        allow_renewal: salesInput.allow_renewal,
        allow_upgrade: salesInput.allow_upgrade,
        recommended: salesInput.recommended,
        reset_price: resetPrice,
        reset_limit: resetLimit,
        current_version_id: willPublish ? versionId : null,
        pool_ids: parsedPools.ids,
        versions: [v1],
        prices: pricesForCheck,
        created_at: nowStr,
      }

      plans.push(newPlan)
      return { status: 201, body: { plan: detail(newPlan) } }
    })
  },

  // 4. PUT /v1/plans/:id/basic
  'PUT /v1/plans/:id/basic': async (ctx) => {
    await withPlan(ctx, 'catalog_plan_basic', (p, b) => {
      const bad = unknownField(b, ['expected_row_version', 'name', 'description'])
      if (bad) return bad
      const expErr = checkExpected(b.expected_row_version)
      if (expErr) return invalid(expErr)
      const lockErr = lockEditablePlan(p, b.expected_row_version as number)
      if (lockErr) return lockErr

      const fields: Record<string, string> = {}
      const basic = prepareBasic(b.name, b.description, fields)
      if (Object.keys(fields).length > 0) return invalid(fields)

      p.name = basic.name
      p.description = basic.description
      p.row_version += 1
      return { status: 200, body: { plan: detail(p) } }
    })
  },

  // 5. PUT /v1/plans/:id/sales
  'PUT /v1/plans/:id/sales': async (ctx) => {
    await withPlan(ctx, 'catalog_plan_sales', (p, b) => {
      const bad = unknownField(b, [
        'expected_row_version',
        'visibility',
        'visible_group_ids',
        'allow_new_purchase',
        'allow_renewal',
        'allow_upgrade',
        'recommended',
      ])
      if (bad) return bad
      const expErr = checkExpected(b.expected_row_version)
      if (expErr) return invalid(expErr)
      const lockErr = lockEditablePlan(p, b.expected_row_version as number)
      if (lockErr) return lockErr

      const fields: Record<string, string> = {}
      const parsedGroups = parseUUIDList('visible_group_ids', b.visible_group_ids)
      if (parsedGroups.error) Object.assign(fields, parsedGroups.error)

      const salesInput = {
        visibility: b.visibility,
        visible_group_ids: parsedGroups.ids,
        allow_new_purchase: Boolean(b.allow_new_purchase),
        allow_renewal: Boolean(b.allow_renewal),
        allow_upgrade: Boolean(b.allow_upgrade),
        recommended: Boolean(b.recommended),
      }
      validateSales(p.kind, salesInput, fields)
      if (Object.keys(fields).length > 0) return invalid(fields)

      p.visibility = (salesInput.visibility as 'public' | 'authenticated' | 'group' | 'hidden') || 'public'
      p.visible_group_ids = p.visibility === 'group' ? salesInput.visible_group_ids : []
      p.allow_new_purchase = salesInput.allow_new_purchase
      p.allow_renewal = salesInput.allow_renewal
      p.allow_upgrade = salesInput.allow_upgrade
      p.recommended = salesInput.recommended
      p.row_version += 1
      return { status: 200, body: { plan: detail(p) } }
    })
  },

  // 6. PUT /v1/plans/:id/reset
  'PUT /v1/plans/:id/reset': async (ctx) => {
    await withPlan(ctx, 'catalog_plan_reset', (p, b) => {
      const bad = unknownField(b, ['expected_row_version', 'enabled', 'price', 'limit'])
      if (bad) return bad
      const expErr = checkExpected(b.expected_row_version)
      if (expErr) return invalid(expErr)
      const lockErr = lockEditablePlan(p, b.expected_row_version as number)
      if (lockErr) return lockErr

      const enabled = Boolean(b.enabled)
      if (enabled) {
        const fields: Record<string, string> = {}
        validateReset(b.price, b.limit, fields)
        if (Object.keys(fields).length > 0) return invalid(fields)
        p.reset_price = Number(b.price)
        p.reset_limit = (b.limit as number | null) ?? null
      } else {
        p.reset_price = null
        p.reset_limit = null
      }
      p.row_version += 1
      return { status: 200, body: { plan: detail(p) } }
    })
  },

  // 7. PUT /v1/plans/:id/prices
  'PUT /v1/plans/:id/prices': async (ctx) => {
    await withPlan(ctx, 'catalog_plan_prices', (p, b) => {
      const bad = unknownField(b, ['expected_row_version', 'prices', 'unit_amount', 'validity_days'])
      if (bad) return bad
      const expErr = checkExpected(b.expected_row_version)
      if (expErr) return invalid(expErr)
      const lockErr = lockEditablePlan(p, b.expected_row_version as number)
      if (lockErr) return lockErr

      // 请求同时带 prices 与 unit_amount 时，按 pricesUpdateFrom 优先认 prices
      const hasPrices = b.prices !== undefined && b.prices !== null
      const hasUnitAmount = !hasPrices && b.unit_amount !== undefined && b.unit_amount !== null
      if (!hasPrices && !hasUnitAmount) {
        return invalid({ prices: '请给出价格' })
      }

      const fields: Record<string, string> = {}
      if (p.kind === 'subscription') {
        if (hasUnitAmount) {
          fields.unit_amount = '订阅套餐按周期定价，不能设一次性价格'
        }
        if (hasPrices) {
          validateTierChanges(b.prices, fields)
        }
      } else {
        // one_time
        if (hasPrices) {
          fields.prices = '一次性套餐只有一个价格，不能设周期价'
        }
        if (!hasUnitAmount) {
          fields.unit_amount = '一次性套餐必须设价格'
        } else {
          validateOneTime(b, fields)
        }
      }
      if (Object.keys(fields).length > 0) return invalid(fields)

      // 计算变更
      let changes = 0
      const nowStr = new Date().toISOString()
      // 在副本上改：回 422（在售至少留一档）时不能动到原数据
      const newPricesList = p.prices.map((x) => ({ ...x }))

      if (p.kind === 'subscription') {
        const changesList = (b.prices as Json[]) || []
        for (const t of changesList) {
          const bi = str(t.billing_interval)
          const ic = Number(t.interval_count)
          const existing = newPricesList.find(
            (x) => x.status === 'active' && x.billing_interval === bi && x.interval_count === ic,
          )

          if (t.unit_amount === null || t.unit_amount === undefined) {
            if (existing) {
              existing.status = 'archived'
              changes++
            }
          } else {
            const amt = Number(t.unit_amount)
            if (existing && existing.unit_amount === amt) {
              continue
            }
            if (existing) {
              existing.status = 'archived'
            }
            newPricesList.unshift({
              id: randomUUID(),
              billing_interval: bi as 'month' | 'year',
              interval_count: ic,
              unit_amount: amt,
              validity_days: null,
              status: 'active',
              created_at: nowStr,
              currency: 'CNY',
              user_group_id: null,
            })
            changes++
          }
        }
      } else {
        const amt = Number(b.unit_amount)
        const vd = (b.validity_days as number | null) ?? null
        const existing = newPricesList.find((x) => x.status === 'active' && x.billing_interval === 'one_time')
        const same = existing && existing.unit_amount === amt && existing.validity_days === vd
        if (!same) {
          if (existing) {
            existing.status = 'archived'
          }
          newPricesList.unshift({
            id: randomUUID(),
            billing_interval: 'one_time',
            interval_count: 1,
            unit_amount: amt,
            validity_days: vd,
            status: 'active',
            created_at: nowStr,
            currency: 'CNY',
            user_group_id: null,
          })
          changes++
        }
      }

      // active 套餐检查在售价格至少留一档
      if (p.status === 'active') {
        const left = newPricesList.filter((x) => x.status === 'active')
        if (left.length === 0) {
          return invalid({ prices: '在售套餐至少保留一档价格' })
        }
      }

      if (changes > 0) {
        p.prices = newPricesList
        p.row_version += 1
      }
      return { status: 200, body: { plan: detail(p) } }
    })
  },

  // 8. PUT /v1/plans/:id/pools
  'PUT /v1/plans/:id/pools': async (ctx) => {
    await withPlan(ctx, 'catalog_plan_pools', (p, b) => {
      const bad = unknownField(b, ['expected_row_version', 'pool_ids'])
      if (bad) return bad
      const expErr = checkExpected(b.expected_row_version)
      if (expErr) return invalid(expErr)
      const lockErr = lockEditablePlan(p, b.expected_row_version as number)
      if (lockErr) return lockErr

      const parsedPools = parseUUIDList('pool_ids', b.pool_ids)
      if (parsedPools.error) return invalid(parsedPools.error)
      const poolErr = poolProblems(parsedPools.ids)
      if (poolErr) return invalid({ pool_ids: poolErr })

      const beforeSorted = [...p.pool_ids].sort().join(',')
      const nextSorted = [...parsedPools.ids].sort().join(',')
      if (beforeSorted !== nextSorted) {
        p.pool_ids = parsedPools.ids
        p.row_version += 1
      }
      return { status: 200, body: { plan: detail(p) } }
    })
  },

  // 9. PUT /v1/plans/:id/usage
  'PUT /v1/plans/:id/usage': async (ctx) => {
    await withPlan(ctx, 'catalog_plan_usage', (p, b) => {
      const bad = unknownField(b, ['expected_row_version', 'traffic_bytes', 'max_devices', 'throttle_kbps'])
      if (bad) return bad
      const expErr = checkExpected(b.expected_row_version)
      if (expErr) return invalid(expErr)
      const lockErr = lockEditablePlan(p, b.expected_row_version as number)
      if (lockErr) return lockErr

      const fields: Record<string, string> = {}
      validateUsage(b, fields)
      if (Object.keys(fields).length > 0) return invalid(fields)

      const draft = p.versions.find((v) => v.status === 'draft')
      const nowStr = new Date().toISOString()
      const tb = (b.traffic_bytes as number | null) ?? null
      const md = (b.max_devices as number | null) ?? null
      const tk = (b.throttle_kbps as number | null) ?? null

      if (draft) {
        draft.traffic_bytes = tb
        draft.max_devices = md
        draft.throttle_kbps = tk
      } else {
        const nextVerNum = Math.max(0, ...p.versions.map((v) => v.version)) + 1
        const newDraft: Version = {
          id: randomUUID(),
          version: nextVerNum,
          status: 'draft',
          published_at: null,
          traffic_bytes: tb,
          max_devices: md,
          throttle_kbps: tk,
          created_by_email: ctx.user.email,
          created_at: nowStr,
        }
        p.versions.unshift(newDraft)
      }
      p.row_version += 1
      return { status: 200, body: { plan: detail(p) } }
    })
  },

  // 10. POST /v1/plans/:id/versions/:versionID/publish
  'POST /v1/plans/:id/versions/:versionID/publish': async (ctx) => {
    if (!ctx.requireReauth()) return
    const body = await ctx.body()
    if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')

    await ctx.idempotent('catalog_plan_version_publish', () => {
      const planId = ctx.params.id ?? ''
      const versionId = ctx.params.versionID ?? ''
      if (!UUID.test(planId) || !UUID.test(versionId)) return NOT_FOUND

      const p = find(planId)
      if (!p) return NOT_FOUND

      const bad = unknownField(body, ['expected_row_version'])
      if (bad) return bad
      const expErr = checkExpected(body.expected_row_version)
      if (expErr) return invalid(expErr)
      const lockErr = lockEditablePlan(p, body.expected_row_version as number)
      if (lockErr) return lockErr

      const v = p.versions.find((x) => x.id === versionId)
      if (!v) return NOT_FOUND
      if (v.status !== 'draft') {
        return err(409, 'conflict', '只有未发布的草稿版本可以发布')
      }

      const pubErr = publishProblems(p)
      if (pubErr) return invalid(pubErr)

      const nowStr = new Date().toISOString()
      v.status = 'published'
      v.published_at = nowStr
      p.current_version_id = v.id
      if (p.status === 'draft') {
        p.status = 'active'
      }
      p.row_version += 1
      return { status: 200, body: { plan: detail(p) } }
    })
  },

  // 11. POST /v1/plans/:id/archive
  'POST /v1/plans/:id/archive': async (ctx) => {
    await withPlan(ctx, 'catalog_plan_archive', (p, b) => {
      const bad = unknownField(b, ['expected_row_version'])
      if (bad) return bad
      const expErr = checkExpected(b.expected_row_version)
      if (expErr) return invalid(expErr)
      const lockErr = lockEditablePlan(p, b.expected_row_version as number)
      if (lockErr) return lockErr

      p.status = 'archived'
      p.row_version += 1
      return { status: 200, body: { plan: detail(p) } }
    })
  },
}

export const plans_: MockModule = { routes }
export { plans_ as plans }
