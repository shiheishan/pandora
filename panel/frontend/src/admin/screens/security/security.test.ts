import { describe, expect, it } from 'vitest'
import {
  accessAccount,
  accessFromQuery,
  accessOutcomeLabel,
  accessQuery,
  accessToQuery,
  accessTone,
  accessUser,
  ACTION_NAMES,
  actionLabel,
  actorLabel,
  auditFromQuery,
  auditQuery,
  auditToQuery,
  authLabel,
  clockTime,
  clusterPlace,
  clusterViews,
  disableCandidates,
  disableSummary,
  EMPTY_ACCESS_FILTER,
  EMPTY_AUDIT_FILTER,
  exportQuery,
  hasAuditFilter,
  isMarkedNormal,
  objectLabel,
  reasonLabel,
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
  ip: '198.51.100.18',
  geo: '中国 广东 移动',
  network_kind: 'mobile',
  risk: 'high',
  key: 'ab12',
  accounts: 3,
  events: 21,
  first: '2026-07-01T00:00:00Z',
  last: '2026-09-25T10:00:00Z',
  emails: ['a@example.test', 'b@example.test', 'c@example.test'],
  users: [
    { id: 'u1', email: 'a@example.test', status: 'active', active_plan: '专业版' },
    { id: 'u2', email: 'b@example.test', status: 'suspended', active_plan: null },
    { id: 'u3', email: 'c@example.test', status: 'pending', active_plan: null },
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
    expect(actorLabel({ actor_email: 'a@example.test', actor_kind: 'admin' })).toBe('a@example.test')
    expect(actorLabel({ actor_email: null, actor_kind: 'system' })).toBe('系统')
    expect(actorLabel({ actor_email: null, actor_kind: 'anonymous' })).toBe('匿名')
    expect(objectLabel({ resource_label: 'PD2609-1', resource_type: 'order', resource_id: 'x' })).toBe('PD2609-1')
    expect(objectLabel({ resource_label: null, resource_type: 'plan_version', resource_id: '9c0e1a2b-4444-4444-8888-000000000000' })).toBe('套餐版本 · 9c0e1a2b')
    expect(objectLabel({ resource_label: null, resource_type: 'withdrawal', resource_id: '0192abcd-ef00-7000-8000-000000000000' })).toBe('提现 · 0192abcd')
    expect(objectLabel({ resource_label: null, resource_type: 'feature_switch', resource_id: null })).toBe('降级开关')
    expect(objectLabel({ resource_label: null, resource_type: 'foo_bar', resource_id: '12345678-abcd' })).toBe('foo_bar · 12345678')
    expect(objectLabel({ resource_label: null, resource_type: 'foo_bar', resource_id: null })).toBe('foo_bar')
    expect(objectLabel({ resource_label: null, resource_type: null, resource_id: '12345678-abcd' })).toBe('12345678')
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

  it('rejects legacy node, agent and plugin actors, accepts anonymous and rejects unknown outcomes', () => {
    const row = { id: '1', occurred_at: '2026-09-01T00:00:00Z', actor_kind: 'anonymous', actor_email: null, action: 'node.enroll', resource_type: null, resource_id: null, api_domain: 'node', outcome: 'success', reason: null, resource_label: null, auth_context: null, source_ip: null }
    expect(auditResponse.safeParse({ events: [row], total: 1 }).success).toBe(true)
    expect(auditResponse.safeParse({ events: [{ ...row, actor_kind: 'node' }], total: 1 }).success).toBe(false)
    expect(auditResponse.safeParse({ events: [{ ...row, actor_kind: 'agent' }], total: 1 }).success).toBe(false)
    expect(auditResponse.safeParse({ events: [{ ...row, actor_kind: 'plugin' }], total: 1 }).success).toBe(false)
    expect(auditResponse.safeParse({ events: [{ ...row, outcome: 'error' }], total: 1 }).success).toBe(false)
    expect(auditResponse.safeParse({ events: [{ ...row, auth_context: 'password' }], total: 1 }).success).toBe(false)
  })

  it('covers action names dictionary completely and strictly ordered', () => {
    const codes = ACTION_NAMES.map(([code]) => code)
    expect(new Set(codes).size).toBe(codes.length)
    expect(codes).toEqual([...codes].sort())
    for (const [code, name] of ACTION_NAMES) {
      expect(code).toMatch(/^[a-z_]+(\.[a-z_]+)+$/)
      expect(name.trim()).not.toBe('')
      expect(name.trim()).not.toBe(code)
      expect(name).not.toContain('.')
    }
    expect(actionLabel('user.login_failed')).toBe('登录失败')
    expect(actionLabel('plan_version.publish')).toBe('发布套餐版本')
    expect(actionLabel('x.unknown')).toBe('x.unknown')
    for (const requiredCode of [
      'user.login',
      'user.login_failed',
      'plan_version.publish',
      'plan.usage_update',
      'balance.adjusted',
      'security.source_ip_viewed',
      'subscription.fetch',
    ]) {
      expect(codes).toContain(requiredCode)
    }
  })

  it('translates reason code to Chinese text and passes through others', () => {
    expect(reasonLabel('invalid_credentials')).toBe('口令错误或账号不存在')
    expect(reasonLabel('account_inactive')).toBe('账号已停用或未激活')
    expect(reasonLabel('not_admin')).toBe('不是管理员却登后台')
    expect(reasonLabel('staff_portal_blocked')).toBe('后台账号登用户端被拒')
    expect(reasonLabel('custom reason')).toBe('custom reason')
    expect(reasonLabel(null)).toBeNull()
  })

  it('converts audit query to and from URLSearchParams', () => {
    const parsed = auditFromQuery(new URLSearchParams('q=k.liu&action=plan&actor=bogus&outcome=failure&o=50'))
    expect(parsed).toEqual({
      filter: { q: 'k.liu', action: 'plan', actor: '', outcome: 'failure' },
      offset: 50,
    })
    expect(auditFromQuery(new URLSearchParams('o=-3')).offset).toBe(0)
    expect(auditFromQuery(new URLSearchParams('o=abc')).offset).toBe(0)
    expect(auditToQuery(EMPTY_AUDIT_FILTER, 0)).toEqual({
      q: undefined,
      action: undefined,
      actor: undefined,
      outcome: undefined,
      o: undefined,
    })
    const f = { q: 'k.liu', action: 'plan', actor: 'admin' as const, outcome: 'failure' as const }
    const q = auditToQuery(f, 50)
    const sp = new URLSearchParams()
    for (const [k, v] of Object.entries(q)) {
      if (v !== undefined) sp.set(k, String(v))
    }
    expect(auditFromQuery(sp)).toEqual({ filter: f, offset: 50 })
  })
})

describe('access log', () => {
  it('maps segments to category or outcome=error and trims the two filters', () => {
    expect(accessQuery(EMPTY_ACCESS_FILTER, 0)).toEqual({ limit: 50, offset: 0, category: undefined, outcome: undefined, ip: undefined, user: undefined })
    expect(accessQuery({ view: 'error', ip: ' 192.0.2.4 ', user: '' }, 50)).toEqual({ limit: 50, offset: 50, category: undefined, outcome: 'error', ip: '192.0.2.4', user: undefined })
    expect(accessQuery({ view: 'subscribe', ip: '', user: 'k.liu' }, 0)).toMatchObject({ category: 'subscribe', outcome: undefined, user: 'k.liu' })
  })

  it('treats ok and success as fine and names subscription fetch results', () => {
    expect(accessTone({ outcome: 'ok' })).toBe('ok')
    expect(accessTone({ outcome: 'success' })).toBe('ok')
    expect(accessTone({ outcome: 'rate_limited' })).toBe('warn')
    expect(accessTone({ outcome: 'not_found' })).toBe('bad')
    expect(accessTone({ outcome: 'failure' })).toBe('bad')
    expect(accessTone({})).toBe('none')
    expect(accessOutcomeLabel({ outcome: 'failure' })).toBe('失败')
    expect(accessOutcomeLabel({ outcome: 'rate_limited' })).toBe('被限流')
    expect(accessOutcomeLabel({ outcome: 'error' })).toBe('失败')
    expect(accessOutcomeLabel({ outcome: 'revoked' })).toBe('revoked')
    expect(accessOutcomeLabel({ outcome: 'weird' })).toBe('weird')
    expect(accessOutcomeLabel({})).toBe('—')
  })

  it('puts the account in the row: email, short id, anonymous trial login or anonymous', () => {
    expect(accessUser({ user_email: 'a@example.test', user_id: 'u1' })).toBe('a@example.test')
    expect(accessAccount({ user_email: 'a@example.test', user_id: 'u1' })).toEqual({ prefix: '', label: 'a@example.test', userId: 'u1' })
    expect(accessUser({ user_id: '0123456789abcdef' })).toBe('用户 01234567')
    expect(accessAccount({ user_id: '0123456789abcdef' })).toEqual({ prefix: '', label: '用户 01234567', userId: '0123456789abcdef' })
    expect(accessUser({ target_user_email: 'k.liu@example.com', target_user_id: 't1' })).toBe('匿名 · 试登 k.liu@example.com')
    expect(accessAccount({ target_user_email: 'k.liu@example.com', target_user_id: 't1' })).toEqual({
      prefix: '匿名 · 试登 ',
      label: 'k.liu@example.com',
      userId: 't1',
    })
    expect(accessUser({ target_user_id: 'abcdef0123456789' })).toBe('匿名 · 试登 用户 abcdef01')
    expect(accessUser({})).toBe('匿名')
    expect(accessAccount({})).toEqual({ prefix: '', label: '匿名', userId: null })
  })

  it('accepts omitempty rows, target user fields and rejects unknown categories', () => {
    expect(accessResponse.safeParse({ items: [{ category: 'subscribe', occurred_at: '2026-09-25T00:00:00Z' }] }).success).toBe(true)
    expect(
      accessResponse.safeParse({
        items: [
          {
            category: 'login',
            occurred_at: '2026-09-25T00:00:00Z',
            target_user_id: '0192abcd-ef00-7000-8000-000000000000',
            target_user_email: 'k.liu@example.com',
          },
        ],
      }).success,
    ).toBe(true)
    expect(accessResponse.safeParse({ items: [{ category: 'http', occurred_at: '2026-09-25T00:00:00Z' }] }).success).toBe(false)
  })

  it('converts access query to and from URLSearchParams', () => {
    const uuid = '0192abcd-ef00-7000-8000-000000000000'
    expect(accessFromQuery(new URLSearchParams(`view=login&user=${uuid}&o=10`))).toEqual({
      filter: { view: 'login', ip: '', user: uuid },
      offset: 10,
    })
    expect(accessFromQuery(new URLSearchParams('view=nope')).filter.view).toBe('all')
    expect(accessToQuery({ view: 'all', ip: '', user: '' }, 0)).toEqual({
      view: undefined,
      ip: undefined,
      user: undefined,
      o: undefined,
    })
    const f = { view: 'subscribe' as const, ip: '192.0.2.1', user: 'k.liu' }
    const q = accessToQuery(f, 25)
    const sp = new URLSearchParams()
    for (const [k, v] of Object.entries(q)) {
      if (v !== undefined) sp.set(k, String(v))
    }
    expect(accessFromQuery(sp)).toEqual({ filter: f, offset: 25 })
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

  it('splits the segments: open from the default list, marked-normal and all from the full list', () => {
    const normal = { ...cluster, key: 'n1', review: { decision: 'normal' as const, decided_at: '2026-09-20T00:00:00Z', expires_at: '2026-10-20T00:00:00Z' } }
    const lapsed = { ...cluster, key: 'n2', review: { decision: 'normal' as const, decided_at: '2026-08-20T00:00:00Z', expires_at: '2026-09-19T00:00:00Z' } }
    expect(isMarkedNormal(normal, now)).toBe(true)
    expect(isMarkedNormal(lapsed, now)).toBe(false)
    const views = clusterViews([cluster, lapsed], [cluster, normal, lapsed], now)
    expect(views.open!.map((c) => c.key)).toEqual(['ab12', 'n2'])
    expect(views.done!.map((c) => c.key)).toEqual(['n1'])
    expect(views.all).toHaveLength(3)
    expect(clusterViews(undefined, undefined, now)).toEqual({ open: null, done: null, all: null })
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
  const row = (code: string, enabled = true, reason: string | null = null): SwitchRow => ({ code, enabled, reason })

  it('flips the polarity: enabled=false means the pause is on', () => {
    const [reg] = switchViews([row('auth.registration', false, '机器注册潮')])
    expect(reg).toMatchObject({ kind: 'toggle', title: '暂停新用户注册', degraded: true, status: '已开启', reason: '机器注册潮' })
    const [checkout] = switchViews([row('billing.checkout'), row('auth.registration'), row('marketing.giftcard.redeem'), row('admin.writes'), row('notify.email')])
    expect(checkout).toMatchObject({ kind: 'toggle', degraded: false, status: '关闭' })
  })

  it('orders switches by the dictionary order', () => {
    const views = switchViews([row('notify.email'), row('billing.checkout'), row('auth.registration'), row('marketing.giftcard.redeem'), row('admin.writes')])
    expect(views.map((v) => v.code)).toEqual(['auth.registration', 'billing.checkout', 'marketing.giftcard.redeem', 'admin.writes', 'notify.email'])
    expect(views.find((v) => v.code === 'notify.email')!.note).toContain('注册')
  })

  it('lists missing rows by their R58 default: new switches open, registration paused', () => {
    const views = switchViews([])
    const missing = Object.fromEntries(views.filter((v) => v.kind === 'missing').map((v) => [v.code, v.degraded]))
    expect(missing).toEqual({ 'auth.registration': true, 'billing.checkout': false, 'marketing.giftcard.redeem': false, 'admin.writes': false, 'notify.email': false })
  })

  it('drops the three unwired switches from the dictionary (R102)', () => {
    for (const code of ['ops.bulk_export', 'ops.reports', 'node.autoscale']) expect(SWITCH_META[code]).toBeUndefined()
    expect(Object.keys(SWITCH_META)).toHaveLength(5)
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
