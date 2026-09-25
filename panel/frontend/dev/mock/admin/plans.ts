/**
 * [INPUT]: 依赖 node:crypto 的 randomUUID，依赖 ../types 的 Json / MockContext / MockModule / MockResult / MockRoute，依赖 ./plans-store 的存储与规则，依赖 ./nodes-infra 的 pools（只读），依赖 ./plans-packs 的 packRoutes
 * [OUTPUT]: 对外提供 plans 模块的假接口 MockModule；转出 plans-store 的 setSalesEnabled 给测试用
 * [POS]: dev/mock/admin 的「套餐（后台-04）」假接口，归后台前端一：列表、详情、只建壳、向导新建（单事务，R65）与编辑（流量 / 价格 / 线路 null = 不动，设备与限速三态、卖点与推荐缺省不动，R1 / R99 / R100；额度 / 线路变了开新版本并立即发布，价格只同步出现过的币种）、销售设置（含卖点与推荐整体覆盖）、版本新建 / 编辑 / 发布、价格新增 / 归档、归档套餐、节点池绑定候选与替换；流量包四接口在 plans-packs.ts，数据与校验在 plans-store.ts。权限、reauth、幂等 scope、校验键名与文案照 api-contract.md 与 domain/adminops 的 catalog.go、plan_wizard*.go、api/admin/pools.go；按 DisallowUnknownFields 拒绝未知字段；销售开关关着时 catalog.publish 类写回 503。超额策略只收 suspend、限速与策略解耦（R99）。R92 的后端现状也照做（后端三 ② 修好后同步删）：编辑向导会清掉上架时间窗、新版本的高级设置回到默认
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { randomUUID } from 'node:crypto'
import type { Json, MockContext, MockModule, MockResult, MockRoute } from '../types.ts'
import { pools } from './nodes-infra.ts'
import { packRoutes } from './plans-packs.ts'
import {
  activeNodes,
  applyBasics,
  applySalesPoints,
  applySemantics,
  blankVersion,
  currentOf,
  detail,
  draftOf,
  DUP,
  err,
  find,
  GiB,
  invalid,
  isInt,
  listRow,
  newPrice,
  NOT_FOUND,
  planFieldProblems,
  plans,
  poolProblems,
  priceKey,
  priceProblems,
  priceRow,
  publish,
  publishProblems,
  quotasFor,
  sales,
  SALES_OFF,
  salesPointProblems,
  seedPlan,
  seedPrice,
  semanticsProblems,
  stale,
  str,
  touch,
  trafficOf,
  unknownField,
  UUID,
  wizardPriceProblems,
  type Plan,
} from './plans-store.ts'

export { setSalesEnabled } from './plans-store.ts'

// ---------------------------------------------------------------------------
// 向导
// ---------------------------------------------------------------------------
const WIZARD_KEYS = [
  'code',
  'name',
  'description',
  'visibility',
  'sort_order',
  'allow_new_purchase',
  'allow_renewal',
  'allow_upgrade',
  'visible_group_ids',
  'purchase_limit_per_user',
  'stock_total',
  'traffic_gb',
  'max_devices',
  'throttle_kbps',
  'highlights',
  'recommended',
]

function createComplete(ctx: MockContext, b: Json): MockResult {
  const bad = unknownField(b, [...WIZARD_KEYS, 'quota_reset_strategy', 'quota_reset_day', 'pool_ids', 'prices', 'publish'])
  if (bad) return bad
  const priceList = Array.isArray(b.prices) ? (b.prices as Json[]) : []
  const poolIds = Array.isArray(b.pool_ids) ? (b.pool_ids as string[]) : []
  const publishNow = b.publish === true
  // validateWizardInput
  const f: Record<string, string> = {}
  if (!str(b.code).trim()) f.code = '请填写套餐代码'
  if (!str(b.name).trim()) f.name = '请填写套餐名称'
  if (publishNow && !priceList.length) f.prices = '要上架就至少得有一档价格，否则用户看得到却买不了'
  if (publishNow && !poolIds.length) f.pool_ids = '要上架就得选节点分组，否则买了也没有线路可用'
  if (b.traffic_gb != null && !(isInt(b.traffic_gb) && b.traffic_gb >= 0)) f.traffic_gb = '流量不能是负数；不限流量请留空'
  if (b.max_devices != null && !(isInt(b.max_devices) && b.max_devices >= 0)) f.max_devices = '设备数不能是负数；不限请留空'
  Object.assign(f, salesPointProblems(b), wizardPriceProblems(priceList))
  if (Object.keys(f).length) return invalid(f)
  if ((priceList.length || publishNow) && !sales()) return SALES_OFF
  // 之后是单事务里的各步（R65）：先全部算好、校验好，最后一次写入
  const basics = { ...b, visibility: str(b.visibility) || 'public' }
  const pf = planFieldProblems(basics)
  if (Object.keys(pf).length) return invalid(pf)
  if (plans.some((p) => p.code === str(b.code).trim())) return DUP
  const version = blankVersion(1, ctx.user.email)
  const semantics = {
    ...version,
    quota_reset_strategy: str(b.quota_reset_strategy) || 'billing_cycle',
    quota_reset_day: b.quota_reset_day ?? null,
    max_devices: b.max_devices ?? null,
    throttle_kbps: b.throttle_kbps ?? null,
    quotas: quotasFor((b.traffic_gb as number | null) ?? null, (b.max_devices as number | null) ?? null),
  }
  const sf = semanticsProblems(semantics as unknown as Json)
  if (Object.keys(sf).length) return invalid(sf)
  applySemantics(version, semantics as unknown as Json)
  if (poolIds.length) {
    const pp = poolProblems(poolIds)
    if (pp) return invalid({ pool_ids: pp })
    version.pool_ids = [...poolIds]
  }
  const plan = seedPlan(randomUUID(), '', '', '', 'draft', 0, [version], priceList.map(newPrice), { row_version: 1, created_at: new Date().toISOString(), current_version_id: null })
  applyBasics(plan, basics)
  applySalesPoints(plan, b)
  plan.allow_new_purchase = b.allow_new_purchase !== false
  plan.allow_renewal = b.allow_renewal !== false
  plan.allow_upgrade = b.allow_upgrade !== false
  if (publishNow) {
    const pub = publishProblems(plan, version)
    if (pub) return invalid(pub)
    publish(plan, version)
  }
  plans.push(plan)
  return { status: 201, body: { plan: detail(plan), version_id: version.id, price_ids: plan.prices.map((x) => x.id), published: publishNow } }
}

function updateComplete(ctx: MockContext, p: Plan, b: Json): MockResult {
  const bad = unknownField(b, [...WIZARD_KEYS, 'expected_row_version', 'prices', 'pool_ids'])
  if (bad) return bad
  if (p.status === 'archived') return err(409, 'conflict', '已归档套餐不能恢复或编辑')
  if (b.expected_row_version !== p.row_version) return stale(p.row_version)
  const pf = { ...planFieldProblems(b), ...salesPointProblems(b) }
  if (Object.keys(pf).length) return invalid(pf)
  const priceList = Array.isArray(b.prices) ? (b.prices as Json[]) : []
  if (priceList.length) {
    const f = wizardPriceProblems(priceList)
    if (Object.keys(f).length) return invalid(f)
    if (!sales()) return SALES_OFF
  }
  // R99 三态：max_devices / throttle_kbps 缺省 = 不动、null = 清为不限、正整数 = 设置（traffic_gb 仍是 null = 不动、0 = 不限）
  const has = (k: string) => Object.prototype.hasOwnProperty.call(b, k)
  if (b.max_devices != null && !(isInt(b.max_devices) && b.max_devices > 0)) return invalid({ max_devices: '必须为正整数' })
  if (b.throttle_kbps != null && !(isInt(b.throttle_kbps) && b.throttle_kbps > 0)) return invalid({ throttle_kbps: '必须为正整数' })
  const cur = currentOf(p)
  const curGB = trafficOf(cur) === null ? -1 : Math.floor(trafficOf(cur)! / GiB)
  const devicesChanged = has('max_devices') && (b.max_devices ?? null) !== (cur?.max_devices ?? null)
  const throttleChanged = has('throttle_kbps') && (b.throttle_kbps ?? null) !== (cur?.throttle_kbps ?? null)
  const quotaChanged = (b.traffic_gb != null && b.traffic_gb !== curGB) || devicesChanged || throttleChanged
  const poolsIn = Array.isArray(b.pool_ids) ? (b.pool_ids as string[]) : null
  const poolsChanged = poolsIn !== null && [...poolsIn].sort().join() !== [...(cur?.pool_ids ?? [])].sort().join()
  if (poolsIn) {
    const pp = poolProblems(poolsIn)
    if (pp) return invalid({ pool_ids: pp })
  }
  if ((quotaChanged || poolsChanged) && !sales()) return SALES_OFF

  // 价格按「周期 + 币种」同步，只动清单里出现过的币种的在售公开价
  const snapshot = structuredClone(p)
  const changed = ['套餐资料已更新']
  applyBasics(p, b)
  applySalesPoints(p, b)
  // 后端现状（R92 ③，后端三 ② 修）：UpdatePlanInput 不带时间窗，编辑向导会把它清空
  p.visible_from = null
  p.visible_until = null
  if (typeof b.allow_new_purchase === 'boolean') p.allow_new_purchase = b.allow_new_purchase
  if (typeof b.allow_renewal === 'boolean') p.allow_renewal = b.allow_renewal
  if (typeof b.allow_upgrade === 'boolean') p.allow_upgrade = b.allow_upgrade
  touch(p)
  let priceChanges = 0
  if (priceList.length) {
    const currencies = new Set(priceList.map((x) => str(x.currency)))
    const want = new Map(priceList.map((x) => [priceKey(x), x]))
    for (const x of p.prices) {
      if (x.status !== 'active' || x.user_group_id !== null || !currencies.has(x.currency)) continue
      const w = want.get(priceKey(x))
      if (w && w.unit_amount === x.unit_amount && (w.trial_days ?? 0) === x.trial_days) {
        want.delete(priceKey(x))
        continue
      }
      x.status = 'archived'
      touch(x)
      priceChanges++
    }
    for (const w of want.values()) {
      p.prices.unshift(newPrice(w))
      priceChanges++
    }
  }
  if (quotaChanged || poolsChanged) {
    const ver = draftOf(p) ?? blankVersion(Math.max(0, ...p.versions.map((v) => v.version)) + 1, ctx.user.email)
    const gb = b.traffic_gb != null ? (b.traffic_gb as number) : curGB
    const devices = has('max_devices') ? ((b.max_devices as number | null) ?? null) : (cur?.max_devices ?? null)
    // 后端现状（R92 ②，后端三 ② 修）：新版本只沿用重置策略与超额策略，宽限、权益等回到默认
    const fresh = blankVersion(ver.version, ver.created_by_email)
    Object.assign(ver, { ...fresh, id: ver.id, row_version: ver.row_version + 1, created_at: ver.created_at })
    ver.quota_reset_strategy = cur?.quota_reset_strategy ?? 'billing_cycle'
    ver.quota_reset_day = cur?.quota_reset_day ?? null
    ver.overage_policy = cur?.overage_policy ?? 'suspend'
    ver.throttle_kbps = has('throttle_kbps') ? ((b.throttle_kbps as number | null) ?? null) : (cur?.throttle_kbps ?? null)
    ver.max_devices = devices
    ver.quotas = quotasFor(gb > 0 ? gb : null, devices)
    ver.pool_ids = poolsIn ?? [...(cur?.pool_ids ?? [])]
    if (!p.versions.includes(ver)) p.versions.unshift(ver)
    const pub = publishProblems(p, ver)
    if (pub) {
      Object.assign(p, snapshot)
      return invalid(pub)
    }
    publish(p, ver)
    if (quotaChanged) changed.push('流量、设备数与限速已更新；新购买的用户按新额度，已经买了的用户仍按原额度')
    if (poolsChanged) changed.push('可用线路已更新；新购买的用户立即拿到，已经买了的用户要到续费时才切过来')
  }
  if (priceChanges) changed.push('价格已更新，只影响之后的新购与续费；已成交的订单不变')
  return { status: 200, body: { plan: detail(p), changed } }
}

// ---------------------------------------------------------------------------
// 路由
// ---------------------------------------------------------------------------
async function withPlan(ctx: MockContext, run: (p: Plan, body: Json) => MockResult | Promise<MockResult>, scope: string | null): Promise<void> {
  const body = await ctx.body()
  if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
  const exec = () => {
    const p = find(ctx.params.id ?? '')
    return p ? run(p, body) : NOT_FOUND
  }
  if (scope === null) {
    const r = await exec()
    return ctx.send(r.status, r.body)
  }
  await ctx.idempotent(scope, exec)
}

const routes: Record<string, MockRoute> = {
  'GET /v1/plans': (ctx) => {
    if (!ctx.requirePermission('catalog.read')) return
    const rows = [...plans].sort((a, b) => a.sort_order - b.sort_order || a.created_at.localeCompare(b.created_at)).map(listRow)
    ctx.send(200, { plans: rows })
  },

  'POST /v1/plans/complete': async (ctx) => {
    if (!ctx.requirePermission('catalog.publish') || !ctx.requireReauth()) return
    const body = await ctx.body()
    if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
    await ctx.idempotent('catalog_plan_create_complete', () => createComplete(ctx, body))
  },

  // 只建壳：设计不用，照契约挂上
  'POST /v1/plans': async (ctx) => {
    if (!ctx.requirePermission('catalog.write') || !ctx.requireReauth()) return
    const body = await ctx.body()
    if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
    await ctx.idempotent('catalog_plan_create', () => {
      const bad = unknownField(body, [
        'code',
        'name',
        'description',
        'visibility',
        'visible_group_ids',
        'visible_from',
        'visible_until',
        'allow_new_purchase',
        'allow_renewal',
        'allow_upgrade',
        'purchase_limit_per_user',
        'stock_total',
        'sort_order',
        'highlights',
        'recommended',
      ])
      if (bad) return bad
      const b: Json = { ...body, visibility: str(body.visibility) || 'public' }
      const f = { ...planFieldProblems(b), ...salesPointProblems(b) }
      if (Object.keys(f).length) return invalid(f)
      if (plans.some((p) => p.code === str(b.code).trim())) return DUP
      const plan = seedPlan(randomUUID(), '', '', '', 'draft', 0, [], [], { row_version: 1, created_at: new Date().toISOString() })
      applyBasics(plan, b)
      applySalesPoints(plan, b)
      plan.visible_from = (b.visible_from as string | null) ?? null
      plan.visible_until = (b.visible_until as string | null) ?? null
      plans.push(plan)
      return { status: 201, body: { plan: detail(plan) } }
    })
  },

  'GET /v1/plans/:id': (ctx) => {
    if (!ctx.requirePermission('catalog.read')) return
    const p = find(ctx.params.id ?? '')
    if (!p) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
    ctx.send(200, { plan: detail(p) })
  },

  'PUT /v1/plans/:id/complete': async (ctx) => {
    if (!ctx.requirePermission('catalog.publish') || !ctx.requireReauth()) return
    await withPlan(ctx, (p, b) => updateComplete(ctx, p, b), 'catalog_plan_update_complete')
  },

  // 销售设置：整体覆盖，不动版本与价格
  'PUT /v1/plans/:id': async (ctx) => {
    if (!ctx.requirePermission('catalog.publish') || !ctx.requireReauth()) return
    await withPlan(
      ctx,
      (p, b) => {
        const bad = unknownField(b, [
          'expected_row_version',
          'code',
          'name',
          'description',
          'visibility',
          'visible_group_ids',
          'visible_from',
          'visible_until',
          'allow_new_purchase',
          'allow_renewal',
          'allow_upgrade',
          'purchase_limit_per_user',
          'stock_total',
          'sort_order',
          'highlights',
          'recommended',
        ])
        if (bad) return bad
        if (p.status === 'archived') return err(409, 'conflict', '已归档套餐不能恢复或编辑')
        if (b.expected_row_version !== p.row_version) return stale(p.row_version)
        // R100：整体覆盖，没带就是空列表与 false（Go 结构体零值），所以前端必须回填当前值
        const full: Json = { ...b, highlights: b.highlights ?? [], recommended: b.recommended ?? false }
        const f = { ...planFieldProblems(b), ...salesPointProblems(full) }
        if (isInt(b.stock_total) && b.stock_total < p.stock_reserved) f.stock_total = '不能低于已预留库存'
        if (Object.keys(f).length) return invalid(f)
        const reopened = (['allow_new_purchase', 'allow_renewal', 'allow_upgrade'] as const).some((k) => !p[k] && b[k] === true)
        if (p.status === 'active' && reopened && !sales()) return SALES_OFF
        applyBasics(p, b)
        applySalesPoints(p, full)
        p.visible_from = (b.visible_from as string | null) ?? null
        p.visible_until = (b.visible_until as string | null) ?? null
        p.allow_new_purchase = b.allow_new_purchase === true
        p.allow_renewal = b.allow_renewal === true
        p.allow_upgrade = b.allow_upgrade === true
        touch(p)
        return { status: 200, body: { ok: true, row_version: p.row_version } }
      },
      'catalog_plan_update',
    )
  },

  'POST /v1/plans/:id/archive': async (ctx) => {
    if (!ctx.requirePermission('catalog.publish') || !ctx.requireReauth()) return
    await withPlan(
      ctx,
      (p, b) => {
        const bad = unknownField(b, ['expected_row_version'])
        if (bad) return bad
        if (!(isInt(b.expected_row_version) && b.expected_row_version > 0)) return invalid({ expected_row_version: '必须为正整数' })
        if (p.status === 'archived') return err(409, 'conflict', '套餐已经归档')
        if (b.expected_row_version !== p.row_version) return stale(p.row_version)
        p.status = 'archived'
        p.allow_new_purchase = false
        touch(p)
        return { status: 200, body: { ok: true, row_version: p.row_version } }
      },
      'catalog_plan_archive',
    )
  },

  // 新建草稿版本：不解码请求体，语义是库默认值，entitlements / quotas / pool_ids 回 null
  'POST /v1/plans/:id/versions': async (ctx) => {
    if (!ctx.requirePermission('catalog.write')) return
    await ctx.idempotent('catalog_plan_version_create', () => {
      const p = find(ctx.params.id ?? '')
      if (!p) return NOT_FOUND
      if (p.status === 'archived') return err(409, 'conflict', '已归档套餐不能新建版本')
      if (draftOf(p)) return DUP
      const v = blankVersion(Math.max(0, ...p.versions.map((x) => x.version)) + 1, ctx.user.email)
      p.versions.unshift(v)
      return { status: 201, body: { version: { ...v, entitlements: null, quotas: null, pool_ids: null } } }
    })
  },

  'PUT /v1/plans/:id/versions/:vid': async (ctx) => {
    if (!ctx.requirePermission('catalog.write')) return
    await withPlan(
      ctx,
      (p, b) => {
        const v = p.versions.find((x) => x.id === ctx.params.vid)
        if (!v) return NOT_FOUND
        if ('pool_ids' in b) return invalid({ pool_ids: '节点分组请用 POST v1/plans/{id}/pools 修改' })
        const bad = unknownField(b, [
          'expected_row_version',
          'quota_reset_strategy',
          'quota_reset_day',
          'grace_period_hours',
          'grace_keeps_service',
          'renewal_extends_period',
          'renewal_resets_quota',
          'renewal_keeps_addons',
          'max_devices',
          'max_concurrent',
          'device_release_hours',
          'overage_policy',
          'throttle_kbps',
          'notes',
          'entitlements',
          'quotas',
        ])
        if (bad) return bad
        const f = semanticsProblems(b)
        if (Object.keys(f).length) return invalid(f)
        if (v.status !== 'draft') return err(409, 'conflict', '只有未发布草稿版本可以编辑')
        if (b.expected_row_version !== v.row_version) return stale(v.row_version, '版本')
        applySemantics(v, b)
        touch(v)
        return { status: 200, body: { ok: true, row_version: v.row_version } }
      },
      null,
    )
  },

  'POST /v1/plans/:id/versions/:vid/publish': async (ctx) => {
    if (!ctx.requirePermission('catalog.publish') || !ctx.requireReauth()) return
    await withPlan(
      ctx,
      (p, b) => {
        const bad = unknownField(b, ['expected_plan_row_version', 'expected_version_row_version'])
        if (bad) return bad
        const v = p.versions.find((x) => x.id === ctx.params.vid)
        if (!v) return NOT_FOUND
        if (!sales()) return SALES_OFF
        if (p.status === 'archived') return err(409, 'conflict', '已归档套餐不能发布版本')
        if (v.status !== 'draft') return err(409, 'conflict', '只有草稿版本可以发布')
        if (b.expected_plan_row_version !== p.row_version) return stale(p.row_version)
        if (b.expected_version_row_version !== v.row_version) return stale(v.row_version, '版本')
        const pub = publishProblems(p, v)
        if (pub) return invalid(pub)
        publish(p, v)
        return { status: 200, body: { ok: true, plan_row_version: p.row_version, version_row_version: v.row_version } }
      },
      'catalog_plan_version_publish',
    )
  },

  'POST /v1/plans/:id/prices': async (ctx) => {
    if (!ctx.requirePermission('catalog.publish') || !ctx.requireReauth()) return
    await withPlan(
      ctx,
      (p, b) => {
        const bad = unknownField(b, ['currency', 'unit_amount', 'billing_interval', 'interval_count', 'trial_days', 'user_group_id', 'valid_from', 'valid_until'])
        if (bad) return bad
        const f = priceProblems(b)
        if (Object.keys(f).length) return invalid(f)
        if (!sales()) return SALES_OFF
        if (p.status === 'archived') return err(409, 'conflict', '已归档套餐不能新增价格')
        const group = (b.user_group_id as string | null) ?? null
        if (p.prices.some((x) => x.status === 'active' && priceKey(x) === priceKey(b) && x.user_group_id === group)) return DUP
        const row = seedPrice(str(b.currency), b.unit_amount as number, str(b.billing_interval), b.interval_count as number, 0, {
          trial_days: isInt(b.trial_days) ? b.trial_days : 0,
          user_group_id: group,
          valid_from: (b.valid_from as string | null) ?? null,
          valid_until: (b.valid_until as string | null) ?? null,
          created_at: new Date().toISOString(),
        })
        p.prices.unshift(row)
        return { status: 201, body: { price: priceRow(row) } }
      },
      'catalog_price_create',
    )
  },

  'POST /v1/plans/:id/prices/:pid/archive': async (ctx) => {
    if (!ctx.requirePermission('catalog.publish') || !ctx.requireReauth()) return
    await withPlan(
      ctx,
      (p, b) => {
        const bad = unknownField(b, ['expected_row_version'])
        if (bad) return bad
        const x = p.prices.find((y) => y.id === ctx.params.pid)
        if (!x) return NOT_FOUND
        if (!(isInt(b.expected_row_version) && b.expected_row_version > 0)) return invalid({ expected_row_version: '必须为正整数' })
        if (x.status === 'archived') return err(409, 'conflict', '价格已经归档')
        if (b.expected_row_version !== x.row_version) return stale(x.row_version, '价格')
        x.status = 'archived'
        touch(x)
        return { status: 200, body: { ok: true, row_version: x.row_version } }
      },
      'catalog_price_archive',
    )
  },

  // 节点池绑定候选：有草稿看草稿（可改），否则看当前发布版本（只读）；只列未禁用的池
  'GET /v1/plans/:id/pools': (ctx) => {
    if (!ctx.requirePermission('catalog.read')) return
    const p = find(ctx.params.id ?? '')
    if (!p) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
    const v = draftOf(p) ?? currentOf(p)
    ctx.send(200, {
      version_id: v?.id ?? '',
      version_status: v?.status ?? '',
      row_version: v?.row_version ?? 0,
      editable: v?.status === 'draft',
      pools: pools.filter((x) => x.status !== 'disabled').map((x) => ({ id: x.id, name: x.name, active_nodes: activeNodes(x.id), bound: v?.pool_ids.includes(x.id) ?? false })),
    })
  },

  'POST /v1/plans/:id/pools': async (ctx) => {
    if (!ctx.requirePermission('catalog.publish') || !ctx.requireReauth()) return
    await withPlan(
      ctx,
      (p, b) => {
        const bad = unknownField(b, ['version_id', 'expected_version_row_version', 'pool_ids'])
        if (bad) return bad
        const f: Record<string, string> = {}
        if (!UUID.test(str(b.version_id))) f.version_id = '必须是 UUID'
        if (!(isInt(b.expected_version_row_version) && b.expected_version_row_version > 0)) f.expected_version_row_version = '必须为正整数'
        const pp = poolProblems(b.pool_ids)
        if (pp) f.pool_ids = pp
        if (Object.keys(f).length) return invalid(f)
        const v = p.versions.find((x) => x.id === b.version_id)
        if (!v) return NOT_FOUND
        if (v.status !== 'draft') return err(409, 'conflict', '只有未发布的草稿版本可以修改节点分组')
        if (b.expected_version_row_version !== v.row_version) return stale(v.row_version, '版本')
        v.pool_ids = [...(b.pool_ids as string[])]
        touch(v)
        return { status: 200, body: { bound: v.pool_ids.length, row_version: v.row_version, version_id: v.id } }
      },
      'catalog_plan_pools_update',
    )
  },

  ...packRoutes(sales),
}

export const plans_: MockModule = { routes }
export { plans_ as plans }
