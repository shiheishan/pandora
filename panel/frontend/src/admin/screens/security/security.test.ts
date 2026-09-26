/**
 * [INPUT]: 依赖 vitest，依赖 ./logic，依赖 ./schemas
 * [OUTPUT]: 无（测试文件）
 * [POS]: admin/screens/security 纯函数层与 schema 边界的单元测试：审计查询串 / 导出日期校验 / 操作人 · 对象 · 认证文字、访问日志分段与结果文字、聚类复核状态 / 可停用成员 / 停用校验与摘要 / 写后缓存补丁、降级开关视图（极性、核心项、缺行、R102 删掉的开关不在字典）与切换请求；界面交互在浏览器里对 dev 假后端验收
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it } from 'vitest'
import {
  accessOutcomeLabel,
  accessQuery,
  actorLabel,
  auditQuery,
  authLabel,
  clockTime,
  clusterPlace,
  disableCandidates,
  disableSummary,
  EMPTY_ACCESS_FILTER,
  EMPTY_AUDIT_FILTER,
  exportQuery,
  hasAuditFilter,
  ipLabel,
  isAccessError,
  objectLabel,
  reviewState,
  reviewText,
  SWITCH_META,
  switchBody,
  switchViews,
  validateDisable,
  validateExportRange,
  validateSwitchReason,
  withDisabled,
  withReview,
} from './logic'
import { accessResponse, auditResponse, clustersResponse, type Cluster, type SwitchRow } from './schemas'

const cluster: Cluster = {
  ip: '223.104.63.18',
  geo: '中国 广东 移动',
  network_kind: 'mobile',
  risk: 'high',
  key: 'ab12',
  accounts: 3,
  events: 21,
  first: '2026-07-01T00:00:00Z',
  last: '2026-09-25T10:00:00Z',
  emails: ['a@x.run', 'b@x.run', 'c@x.run'],
  users: [
    { id: 'u1', email: 'a@x.run', status: 'active', active_plan: '专业版' },
    { id: 'u2', email: 'b@x.run', status: 'suspended', active_plan: null },
    { id: 'u3', email: 'c@x.run', status: 'pending', active_plan: null },
  ],
  review: null,
}

describe('audit log', () => {
  it('builds the list and export queries from the same filter, dropping blanks', () => {
    expect(auditQuery(EMPTY_AUDIT_FILTER, 0)).toEqual({ limit: 50, offset: 0, q: undefined, action: undefined, actor_kind: undefined, outcome: undefined })
    const f = { q: ' linzhou ', action: 'order.', actor: 'admin' as const, outcome: 'failure' as const }
    expect(auditQuery(f, 100)).toEqual({ limit: 50, offset: 100, q: 'linzhou', action: 'order.', actor_kind: 'admin', outcome: 'failure' })
    expect(exportQuery(f, '2026-09-01', '')).toEqual({ q: 'linzhou', action: 'order.', actor_kind: 'admin', outcome: 'failure', from: '2026-09-01', to: undefined })
    expect(hasAuditFilter(EMPTY_AUDIT_FILTER)).toBe(false)
    expect(hasAuditFilter({ ...EMPTY_AUDIT_FILTER, q: '  ' })).toBe(false)
    expect(hasAuditFilter({ ...EMPTY_AUDIT_FILTER, outcome: 'denied' })).toBe(true)
  })

  it('validates the export range with the same keys and wording as auditExportRange', () => {
    expect(validateExportRange('', '')).toEqual({})
    expect(validateExportRange('2026-09-01', '2026-09-01')).toEqual({})
    expect(validateExportRange('2026-02-30', '')).toEqual({ from: '日期格式应为 YYYY-MM-DD' })
    expect(validateExportRange('', '9/1')).toEqual({ to: '日期格式应为 YYYY-MM-DD' })
    expect(validateExportRange('2026-09-02', '2026-09-01')).toEqual({ to: '结束日期不能早于开始日期' })
  })

  it('labels actors, objects and auth context the way the contract maps them', () => {
    expect(actorLabel({ actor_email: 'a@x.run', actor_kind: 'admin' })).toBe('a@x.run')
    expect(actorLabel({ actor_email: null, actor_kind: 'system' })).toBe('系统')
    expect(actorLabel({ actor_email: null, actor_kind: 'node' })).toBe('节点代理')
    expect(actorLabel({ actor_email: null, actor_kind: 'agent' })).toBe('客服')
    expect(objectLabel({ resource_label: 'PD2609-1', resource_type: 'order', resource_id: 'x' })).toBe('PD2609-1')
    expect(objectLabel({ resource_label: null, resource_type: 'withdrawal', resource_id: '0192abcd-ef00-7000-8000-000000000000' })).toBe('withdrawal · 0192abcd')
    expect(objectLabel({ resource_label: null, resource_type: 'feature_switch', resource_id: null })).toBe('feature_switch')
    expect(objectLabel({ resource_label: null, resource_type: null, resource_id: null })).toBe('—')
    expect(authLabel('reauth')).toEqual({ label: '二次认证', strong: true })
    expect(authLabel('session').label).toBe('会话')
    expect(authLabel(null).label).toBe('—')
  })

  it('shows the clock for today and the date otherwise', () => {
    const now = new Date(2026, 8, 25, 12, 0, 0)
    expect(clockTime(new Date(2026, 8, 25, 9, 5, 7).toISOString(), now)).toBe('09:05:07')
    expect(clockTime(new Date(2026, 8, 24, 23, 59, 0).toISOString(), now)).toBe('09-24 23:59:00')
    expect(clockTime('garbage', now)).toBe('garbage')
  })

  it('accepts legacy rows and the node actor, rejects unknown outcomes', () => {
    const row = { id: '1', occurred_at: '2026-09-01T00:00:00Z', actor_kind: 'node', actor_email: null, action: 'node.enroll', resource_type: null, resource_id: null, api_domain: 'node', outcome: 'success', reason: null, resource_label: null, auth_context: null, source_ip: null }
    expect(auditResponse.safeParse({ events: [row], total: 1 }).success).toBe(true)
    expect(auditResponse.safeParse({ events: [{ ...row, outcome: 'error' }], total: 1 }).success).toBe(false)
    expect(auditResponse.safeParse({ events: [{ ...row, auth_context: 'password' }], total: 1 }).success).toBe(false)
  })
})

describe('access log', () => {
  it('maps segments to category or outcome=error and trims the two filters', () => {
    expect(accessQuery(EMPTY_ACCESS_FILTER, 0)).toEqual({ limit: 50, offset: 0, category: undefined, outcome: undefined, ip: undefined, user: undefined })
    expect(accessQuery({ view: 'error', ip: ' 1.2.3.4 ', user: '' }, 50)).toEqual({ limit: 50, offset: 50, category: undefined, outcome: 'error', ip: '1.2.3.4', user: undefined })
    expect(accessQuery({ view: 'subscribe', ip: '', user: 'k.liu' }, 0)).toMatchObject({ category: 'subscribe', outcome: undefined, user: 'k.liu' })
  })

  it('treats ok and success as fine and names subscription fetch results', () => {
    expect(isAccessError({ outcome: 'ok' })).toBe(false)
    expect(isAccessError({ outcome: 'success' })).toBe(false)
    expect(isAccessError({})).toBe(false)
    expect(isAccessError({ outcome: 'not_found' })).toBe(true)
    expect(accessOutcomeLabel({ outcome: 'failure' })).toBe('失败')
    expect(accessOutcomeLabel({ outcome: 'rate_limited' })).toBe('限流')
    expect(accessOutcomeLabel({ outcome: 'weird' })).toBe('weird')
    expect(accessOutcomeLabel({})).toBe('—')
    expect(ipLabel({ ip: '10.0.0.1', geo: '内网地址' })).toBe('10.0.0.1 · 内网地址')
    expect(ipLabel({})).toBe('—')
  })

  it('accepts omitempty rows and rejects unknown categories', () => {
    expect(accessResponse.safeParse({ items: [{ category: 'subscribe', occurred_at: '2026-09-25T00:00:00Z' }] }).success).toBe(true)
    expect(accessResponse.safeParse({ items: [{ category: 'http', occurred_at: '2026-09-25T00:00:00Z' }] }).success).toBe(false)
  })
})

describe('risk clusters', () => {
  const now = new Date('2026-09-25T00:00:00Z')

  it('reads the review state: open, normal until, expired, disabled', () => {
    expect(reviewState(null, now)).toEqual({ kind: 'open' })
    expect(reviewState({ decision: 'normal', decided_at: '2026-09-20T00:00:00Z', expires_at: '2026-10-20T00:00:00Z' }, now)).toEqual({ kind: 'normal', until: '2026-10-20T00:00:00Z' })
    expect(reviewState({ decision: 'normal', decided_at: '2026-08-20T00:00:00Z', expires_at: '2026-09-19T00:00:00Z' }, now)).toEqual({ kind: 'expired' })
    expect(reviewState({ decision: 'disabled', decided_at: '2026-09-22T00:00:00Z', expires_at: null }, now)).toEqual({ kind: 'disabled', at: '2026-09-22T00:00:00Z' })
    expect(reviewText({ kind: 'open' })).toBeNull()
    expect(reviewText({ kind: 'expired' })).toContain('30 天')
  })

  it('offers only members that are not already suspended or banned', () => {
    expect(disableCandidates(cluster).map((u) => u.id)).toEqual(['u1', 'u3'])
    expect(clusterPlace(cluster)).toBe('中国 广东 移动 · 移动网络')
    expect(clusterPlace({ geo: '', network_kind: '' })).toBe('归属地未知')
  })

  it('validates the disable request like normalizeDisableInput', () => {
    expect(validateDisable(['u1'], '批量注册')).toEqual({ reason: '原因必须为 5 到 500 字' })
    expect(validateDisable([], '同一出口批量注册')).toEqual({ user_ids: '请选择 1 到 200 个账号' })
    expect(validateDisable(['u1'], '同一出口批量注册')).toEqual({})
    expect(validateDisable(['u1'], 'x'.repeat(501))).toHaveProperty('reason')
  })

  it('summarises the disable result, including when nothing was disabled', () => {
    expect(disableSummary({ disabled: 2, skipped: [] })).toEqual({ message: '已禁用 2 个账号', ok: true })
    expect(
      disableSummary({
        disabled: 1,
        skipped: [
          { user_id: 'a', reason: 'administrator' },
          { user_id: 'b', reason: 'administrator' },
          { user_id: 'c', reason: 'already_disabled' },
        ],
      }),
    ).toEqual({ message: '已禁用 1 个账号，跳过 3 个（后台账号 2、已停用 1）', ok: true })
    expect(disableSummary({ disabled: 0, skipped: [{ user_id: 'a', reason: 'administrator' }] })).toEqual({ message: '一个账号都没有禁用：跳过 后台账号 1', ok: false })
  })

  it('patches the cached list in place after a review or a disable', () => {
    const other = { ...cluster, key: 'cd34' }
    const reviewed = withReview([cluster, other], 'ab12', { decision: 'normal', decided_at: 'd', expires_at: 'e' })
    expect(reviewed[0]!.review).toEqual({ decision: 'normal', decided_at: 'd', expires_at: 'e' })
    expect(reviewed[1]!.review).toBeNull()

    const done = withDisabled([cluster], 'ab12', ['u1', 'u3'], { disabled: 1, skipped: [{ user_id: 'u3', reason: 'administrator' }] }, now)
    expect(done[0]!.users.map((u) => u.status)).toEqual(['suspended', 'suspended', 'pending'])
    expect(done[0]!.review).toEqual({ decision: 'disabled', decided_at: now.toISOString(), expires_at: null })
    // 一个都没停成：后端不写结论，缓存也不动
    expect(withDisabled([cluster], 'ab12', ['u1'], { disabled: 0, skipped: [{ user_id: 'u1', reason: 'administrator' }] }, now)[0]).toEqual(cluster)
  })

  it('parses the cluster view with an undecryptable ip and rejects unknown risks', () => {
    expect(clustersResponse.safeParse({ clusters: [{ ...cluster, ip: '', geo: '', network_kind: '' }] }).success).toBe(true)
    expect(clustersResponse.safeParse({ clusters: [{ ...cluster, risk: 'critical' }] }).success).toBe(false)
  })
})

describe('degradation switches', () => {
  const row = (code: string, enabled = true, essential = false, reason: string | null = null): SwitchRow => ({ code, enabled, essential, reason })

  it('flips the polarity: enabled=false means the pause is on', () => {
    const [reg] = switchViews([row('auth.registration', false, false, '机器注册潮')])
    expect(reg).toMatchObject({ kind: 'toggle', title: '暂停新用户注册', degraded: true, status: '已开启', tone: 'danger', reason: '机器注册潮' })
    const [checkout] = switchViews([row('billing.checkout'), row('auth.registration'), row('marketing.giftcard.redeem'), row('admin.writes'), row('notify.email')])
    expect(checkout).toMatchObject({ kind: 'toggle', degraded: false, status: '关闭', tone: 'muted' })
  })

  it('locks essentials and orders them last', () => {
    const views = switchViews([row('auth.login', true, true), row('billing.checkout'), row('auth.registration'), row('marketing.giftcard.redeem'), row('admin.writes'), row('notify.email')])
    expect(views.map((v) => v.code)).toEqual(['auth.registration', 'billing.checkout', 'marketing.giftcard.redeem', 'admin.writes', 'notify.email', 'auth.login'])
    expect(views.at(-1)).toMatchObject({ kind: 'essential', status: '始终开启', degraded: false })
    expect(views.find((v) => v.code === 'notify.email')!.note).toContain('注册')
  })

  it('lists missing rows by their R58 default: new switches open, registration paused', () => {
    const views = switchViews([row('auth.login', true, true)])
    const missing = Object.fromEntries(views.filter((v) => v.kind === 'missing').map((v) => [v.code, v.degraded]))
    expect(missing).toEqual({ 'auth.registration': true, 'billing.checkout': false, 'marketing.giftcard.redeem': false, 'admin.writes': false, 'notify.email': false })
  })

  it('drops the three unwired switches from the dictionary (R102)', () => {
    for (const code of ['ops.bulk_export', 'ops.reports', 'node.autoscale']) expect(SWITCH_META[code]).toBeUndefined()
    expect(Object.keys(SWITCH_META)).toHaveLength(8)
  })

  it('keeps unknown codes toggleable with the backend polarity', () => {
    const [v] = switchViews([row('portal.maintenance', false)]).filter((x) => x.kind === 'toggle' && x.code === 'portal.maintenance')
    expect(v).toMatchObject({ title: 'portal.maintenance', degraded: true })
  })

  it('requires a reason only when pausing and sends enabled with the backend polarity', () => {
    expect(validateSwitchReason(true, '  ')).not.toBe('')
    expect(validateSwitchReason(false, '')).toBe('')
    expect(switchBody(true, ' 渠道故障 ')).toEqual({ enabled: false, reason: '渠道故障' })
    expect(switchBody(false, '')).toEqual({ enabled: true, reason: '' })
  })
})
