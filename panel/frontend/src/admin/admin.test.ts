import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '../core/api'
import { classifyFailure, createIntentKey, handleFailure } from './actions'
import { passwordStrength } from './ChangePasswordDialog'
import { describeEvent } from './EventsCapsule'
import { identityLabels } from './me'
import { MODULES, modulePath, paletteItems, resolveRoute } from './modules'
import { createReauthController } from './reauth'
import { taskCount, tasksSchema } from './tasks'

describe('resolveRoute', () => {
  it('keeps valid module/tab paths', () => {
    expect(resolveRoute('/users/groups')).toEqual({ module: 'users', tab: 'groups', rest: [], canonical: '/users/groups' })
    expect(resolveRoute('/tickets')).toEqual({ module: 'tickets', tab: null, rest: [], canonical: '/tickets' })
  })

  it('falls back to the first tab and sends unknown paths to the dashboard', () => {
    expect(resolveRoute('/users').canonical).toBe('/users/list')
    expect(resolveRoute('/users/nope').canonical).toBe('/users/list')
    expect(resolveRoute('/').canonical).toBe('/dash')
    expect(resolveRoute('/toString').canonical).toBe('/dash')
    expect(resolveRoute('/a/b/c')).toEqual({ module: 'dash', tab: null, rest: [], canonical: '/dash' })
  })

  it('hands the segments after module and tab to the page as rest', () => {
    expect(resolveRoute('/users/list/0b6c-uuid')).toEqual({ module: 'users', tab: 'list', rest: ['0b6c-uuid'], canonical: '/users/list/0b6c-uuid' })
    expect(resolveRoute('/tickets/TK20260923-ABCDEFGH')).toEqual({ module: 'tickets', tab: null, rest: ['TK20260923-ABCDEFGH'], canonical: '/tickets/TK20260923-ABCDEFGH' })
    expect(resolveRoute('/nodes/servers/abc/edit').rest).toEqual(['abc', 'edit'])
  })

  it('decodes rest, re-encodes it in the canonical path and drops empty segments', () => {
    const route = resolveRoute('/content/kb/%E5%B8%AE%E5%8A%A9%2Fa')
    expect(route.rest).toEqual(['帮助/a'])
    expect(route.canonical).toBe('/content/kb/%E5%B8%AE%E5%8A%A9%2Fa')
    expect(resolveRoute('/users/list//abc').canonical).toBe('/users/list/abc')
    expect(resolveRoute('/users/list/%E0%A4%A')).toEqual({ module: 'users', tab: 'list', rest: [], canonical: '/users/list' })
  })

  it('drops the rest that follows an unknown tab', () => {
    expect(resolveRoute('/users/nope/abc')).toEqual({ module: 'users', tab: 'list', rest: [], canonical: '/users/list' })
  })

  it('treats inherited object keys as unknown tabs', () => {
    expect(resolveRoute('/marketing/toString').canonical).toBe('/marketing/coupons')
  })

  it('modulePath builds canonical links and falls back to the first tab', () => {
    expect(modulePath('billing')).toBe('/billing/orders')
    expect(modulePath('billing', 'arrears')).toBe('/billing/arrears')
    expect(modulePath('billing', 'nope')).toBe('/billing/orders')
    expect(modulePath('dash', 'x')).toBe('/dash')
  })
})

describe('paletteItems', () => {
  it('lists 12 entries by default and filters on title + path', () => {
    expect(paletteItems('')).toHaveLength(12)
    const gift = paletteItems('礼品')
    expect(gift.map((i) => [i.module, i.tab])).toEqual([['marketing', 'gifts']])
    expect(paletteItems('reality').some((i) => i.module === 'nodes')).toBe(true)
    expect(paletteItems('完全不存在')).toEqual([])
  })

  it('lists every module and tab (the admin console has no per-page permissions)', () => {
    for (const [key, def] of Object.entries(MODULES)) {
      const pages: Array<readonly [string | null, string]> = def.tabs ? [...def.tabs] : [[null, def.title]]
      for (const [tab, title] of pages) expect(paletteItems(title).some((i) => i.module === key && i.tab === tab), `${key}/${tab}`).toBe(true)
    }
  })
})

describe('createReauthController', () => {
  it('shares one pending prompt and settles every waiter', async () => {
    const reauth = createReauthController()
    const notify = vi.fn()
    reauth.subscribe(notify)
    const a = reauth.request()
    const b = reauth.request()
    expect(reauth.isPending()).toBe(true)
    expect(notify).toHaveBeenCalledTimes(1)
    reauth.resolve(true)
    await expect(Promise.all([a, b])).resolves.toEqual([true, true])
    expect(reauth.isPending()).toBe(false)
    const c = reauth.request()
    reauth.resolve(false)
    await expect(c).resolves.toBe(false)
  })
})

describe('identityLabels', () => {
  it('always says 管理员 and takes the avatar from the name', () => {
    const me = { user_id: 'abcdef12-3456', reauthed: true, email: 'linzhou@pandora.run', display_name: '林舟' }
    expect(identityLabels(me)).toEqual({ title: '管理员 · 林舟', subtitle: 'linzhou@pandora.run', initial: '林' })
    expect(identityLabels({ ...me, display_name: null })).toEqual({ title: '管理员 · linzhou', subtitle: 'linzhou@pandora.run', initial: 'l' })
  })

  it('falls back to 管理员 before v1/me loads', () => {
    expect(identityLabels(undefined)).toEqual({ title: '管理员', subtitle: '', initial: '管' })
  })
})

describe('passwordStrength', () => {
  it('fills one bar per 4 characters, capped at 4', () => {
    expect([0, 3, 4, 11, 12, 16, 40].map((n) => passwordStrength('x'.repeat(n)))).toEqual([0, 0, 1, 2, 3, 4, 4])
  })
})

describe('describeEvent', () => {
  it('turns a table-change event into a generic, navigable item', () => {
    const item = describeEvent({ event: 'orders.changed', data: '{"table":"orders","op":"INSERT","id":"0192a1b2-c3d4"}' }, 1)
    expect(item).toMatchObject({ title: '订单变更 · 新建', body: 'orders · 0192a1b2', module: 'billing', tab: 'orders', tone: 'ok' })
  })

  it('handles ticket replies, unknown topics and bad payloads', () => {
    expect(describeEvent({ event: 'ticket.updated', data: '{"ticket_id":"t-123456789"}' }, 2)).toMatchObject({ title: '工单回复变更', body: 't-123456', module: 'tickets' })
    expect(describeEvent({ event: 'data.changed', data: 'not json' }, 3)).toMatchObject({ title: '数据变更', body: '', module: null })
  })

  it('describes switch toggles by code and direction', () => {
    expect(describeEvent({ event: 'switches.changed', data: '{"code":"billing.checkout","enabled":false}' }, 4)).toMatchObject({ title: '降级开关变更 · 已暂停', body: 'billing.checkout', module: 'security', tab: 'switches', tone: 'warn' })
    expect(describeEvent({ event: 'switches.changed', data: '{"code":"admin.writes","enabled":true}' }, 5)).toMatchObject({ title: '降级开关变更 · 已恢复', body: 'admin.writes' })
  })
})

describe('dashboard/tasks', () => {
  const payload = {
    as_of: '2026-09-24T08:00:00Z',
    items: [
      { kind: 'tickets_open', count: 7, high_priority: 2, oldest_wait_seconds: null },
      { kind: 'withdrawals_pending', count: 3, amounts: [{ currency: 'CNY', amount: 128000 }] },
      { kind: 'notifications_backlog', queued: 214, failed_total: 9, backlog_state: 'backlogged' },
    ],
  }

  it('侧栏与仪表盘共用的严格 schema：未知 kind、缺字段都判为不符约定', () => {
    expect(tasksSchema.safeParse(payload).success).toBe(true)
    expect(tasksSchema.safeParse({ ...payload, items: [{ kind: 'mystery', count: 1 }] }).success).toBe(false)
    expect(tasksSchema.safeParse({ ...payload, items: [{ kind: 'tickets_open', count: 7 }] }).success).toBe(false)
  })

  it('徽标取数：条目缺失（无权限）为 0', () => {
    const items = tasksSchema.parse(payload).items
    expect(taskCount(items, 'tickets_open')).toBe(7)
    expect(taskCount(items, 'withdrawals_pending')).toBe(3)
    expect(taskCount(items, 'nodes_offline')).toBe(0)
    expect(taskCount(undefined, 'tickets_open')).toBe(0)
  })
})

describe('actions', () => {
  it('classifyFailure：reauth 取消静默；有 fields 且能标表单才标；其余 Toast 服务端文案', () => {
    const reauth = new ApiError({ status: 403, code: 'reauth_required', message: '需要重新验证' })
    const invalid = new ApiError({ status: 422, code: 'validation_failed', message: '请求参数校验未通过', fields: { body: '内容需在 1–5000 字之间' } })
    const conflict = new ApiError({ status: 409, code: 'conflict', message: '已被他人修改' })
    expect(classifyFailure(reauth, true)).toEqual({ kind: 'silent' })
    expect(classifyFailure(invalid, true)).toEqual({ kind: 'fields', fields: { body: '内容需在 1–5000 字之间' } })
    expect(classifyFailure(invalid, false)).toEqual({ kind: 'toast', message: '请求参数校验未通过' })
    expect(classifyFailure(conflict, true)).toEqual({ kind: 'toast', message: '已被他人修改' })
    expect(classifyFailure(new Error('boom'), true)).toEqual({ kind: 'toast', message: '操作失败，请稍后重试' })
  })

  // 契约 1.5 与 R85：后端只重放 2xx，4xx 业务拒绝后同 key 会重新执行或回 409
  const failure = (status: number, code: ConstructorParameters<typeof ApiError>[0]['code'], fields?: Record<string, string>) =>
    new ApiError({ status, code, message: `失败 ${status}`, ...(fields ? { fields } : {}) })

  it('handleFailure：传了 intent 时 4xx 丢弃键、其余保留；回调与选项两种写法等价', () => {
    let n = 0
    const intent = createIntentKey(() => `k${++n}`)
    const toasts: string[] = []
    const toast = (m: string) => void toasts.push(m)
    const key = intent.keyFor('pay')

    // 断网与 5xx：Toast，键保留，重试回放同一把
    expect(handleFailure(failure(0, 'network_error'), { intent }, toast)).toBe(false)
    expect(handleFailure(failure(503, 'service_unavailable'), { intent }, toast)).toBe(false)
    expect(intent.keyFor('pay')).toBe(key)
    // reauth 取消：静默，键保留
    expect(handleFailure(failure(403, 'reauth_required'), { intent }, toast)).toBe(true)
    expect(intent.keyFor('pay')).toBe(key)
    // 409：Toast，键丢弃，下一次是新键
    expect(handleFailure(failure(409, 'conflict'), { intent }, toast)).toBe(false)
    const next = intent.keyFor('pay')
    expect(next).not.toBe(key)
    // 422 标表单：键同样丢弃
    let marked: Record<string, string> = {}
    expect(handleFailure(failure(422, 'validation_failed', { note: '太短' }), { fields: (f) => (marked = f), intent }, toast)).toBe(true)
    expect(marked).toEqual({ note: '太短' })
    expect(intent.keyFor('pay')).not.toBe(next)
    // 只传回调（无幂等键的写操作）行为不变
    marked = {}
    expect(handleFailure(failure(422, 'validation_failed', { name: '重复' }), (f) => (marked = f), toast)).toBe(true)
    expect(marked).toEqual({ name: '重复' })
    expect(handleFailure(failure(422, 'validation_failed', { name: '重复' }), undefined, toast)).toBe(false)
    expect(toasts).toEqual(['失败 0', '失败 503', '失败 409', '失败 422'])
  })
})
