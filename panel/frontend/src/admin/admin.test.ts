import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '../core/api'
import { classifyFailure, handleFailure } from './actions'
import { createIntentKey } from '../core/intent'
import { passwordStrength } from './ChangePasswordDialog'
import { hasOwnHead, isDetailRoute, isFullBleed, MODULES, modulePath, NAV_GROUPS, paletteGroups, resolveRoute } from './modules'
import { PAGE_HELP, pageHelp } from './page-help'
import { createReauthController } from './reauth'
import { documentSiteName } from './site-name'
import { formatWait, navBadges, taskCount, tasksSchema, ticketAlert } from './tasks'

describe('documentSiteName', () => {
  it('reads the server-written application-name meta and falls back to Pandora', () => {
    const doc = (content: string | null) => ({ querySelector: () => (content === null ? null : { getAttribute: () => content }) }) as unknown as Pick<Document, 'querySelector'>
    expect(documentSiteName(doc(' 夜海加速 '))).toBe('夜海加速')
    expect(documentSiteName(doc('   '))).toBe('Pandora')
    expect(documentSiteName(doc(null))).toBe('Pandora')
  })
})

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

describe('isDetailRoute', () => {
  it('详情页：rest 非空且在登记表里', () => {
    expect(isDetailRoute('users', 'list', ['u1'])).toBe(true)
    expect(isDetailRoute('billing', 'orders', ['PD1'])).toBe(true)
    expect(isDetailRoute('nodes', 'nodes', ['n1'])).toBe(true)
    expect(isDetailRoute('nodes', 'servers', ['s1'])).toBe(true)
    expect(isDetailRoute('plans', null, ['p1'])).toBe(true)
    expect(isDetailRoute('plans', null, ['p1', 'prices'])).toBe(true)
  })

  it('列表或筛选：rest 为空或不在登记表', () => {
    expect(isDetailRoute('users', 'list', [])).toBe(false)
    expect(isDetailRoute('nodes', 'routing', ['g1'])).toBe(false)
    expect(isDetailRoute('content', 'kb', ['a'])).toBe(false)
    expect(isDetailRoute('tickets', null, ['TK1'])).toBe(false)
    expect(isDetailRoute('users', 'groups', ['g'])).toBe(false)
  })
})

describe('nav groups', () => {
  it('NAV_GROUPS 覆盖全部模块各一次，且分组标题与 MODULES.group 一致', () => {
    const keys = NAV_GROUPS.flatMap(([, mods]) => mods)
    expect(new Set(keys)).toEqual(new Set(Object.keys(MODULES) as typeof keys))
    expect(keys.length).toBe(Object.keys(MODULES).length)
    expect(NAV_GROUPS[0]![0]).toBe(null)
    for (const [title, mods] of NAV_GROUPS) {
      if (title === null) continue
      for (const k of mods) expect(MODULES[k].group).toBe(title)
    }
  })

  it('全幅仅工单', () => {
    expect(isFullBleed('tickets')).toBe(true)
    expect(isFullBleed('users')).toBe(false)
  })

  it('自画页头仅仪表盘', () => {
    const own = (Object.keys(MODULES) as (keyof typeof MODULES)[]).filter(hasOwnHead)
    expect(own).toEqual(['dash'])
  })
})

describe('paletteGroups', () => {
  const flat = (q: string) => paletteGroups(q).flatMap((g) => g.items)

  it('lists every module under 快速跳转 when the query is empty', () => {
    const groups = paletteGroups('')
    expect(groups.map((g) => g.label)).toEqual(['快速跳转'])
    expect(groups[0]!.items.map((i) => i.module)).toEqual(Object.keys(MODULES))
    expect(groups[0]!.items.every((i) => i.kind === '模块' && i.tab === null)).toBe(true)
  })

  it('lists 模块 › 页签 and deep links under 页面 when there is a query', () => {
    expect(paletteGroups('礼品').map((g) => g.label)).toEqual(['页面'])
    expect(flat('礼品').map((i) => [i.module, i.tab, i.title])).toEqual([['marketing', 'gifts', '营销 › 礼品卡']])
    expect(flat('reality').map((i) => [i.module, i.tab, i.title])).toEqual([['nodes', 'nodes', '节点与服务器 › 节点']])
    expect(flat('设备策略').map((i) => [i.module, i.tab])).toEqual([['security', 'risk']])
    expect(paletteGroups('完全不存在')).toEqual([])
  })

  it('reaches every module and tab (the admin console has no per-page permissions)', () => {
    for (const [key, def] of Object.entries(MODULES)) {
      const pages: Array<readonly [string | null, string]> = def.tabs ? def.tabs.map(([tab, title]) => [tab, `${def.title} › ${title}`] as const) : [[null, def.title]]
      for (const [tab, title] of pages) expect(flat(title).some((i) => i.module === key && i.tab === tab), `${key}/${tab}`).toBe(true)
    }
  })

  it('drops the tabs the 10-16 redesign removed', () => {
    expect(resolveRoute('/users/resets').canonical).toBe('/users/list')
    expect(resolveRoute('/plans/packs')).toMatchObject({ module: 'plans', tab: null, rest: ['packs'] })
    expect(flat('流量包')).toEqual([])
    expect(flat('挂账')).toEqual([])
  })
})

describe('PAGE_HELP', () => {
  it('only registers pages that exist', () => {
    for (const key of Object.keys(PAGE_HELP)) {
      const [module = '', tab] = key.split(':')
      const route = resolveRoute(tab ? `/${module}/${tab}` : `/${module}`)
      expect(route.canonical, key).toBe(tab ? `/${module}/${tab}` : `/${module}`)
      expect(pageHelp(route.module, route.tab), key).toBe(PAGE_HELP[key])
    }
  })

  it('carries the four node sections and the generic entries from the design', () => {
    expect(pageHelp('nodes', 'nodes')?.sections.map((s) => s.title)).toEqual(['新建节点 · 6 步', '已停用 vs 已退役'])
    expect(pageHelp('nodes', 'servers')?.label).toBe('服务器页怎么用')
    expect(pageHelp('nodes', 'pools')?.label).toBe('节点池怎么用')
    expect(pageHelp('nodes', 'routing')?.label).toBe('路由怎么用')
    expect(pageHelp('users', 'list')).toBeUndefined()
    expect(JSON.stringify(PAGE_HELP)).not.toMatch(/专属价|挂账|门户/)
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

describe('passwordStrength', () => {
  it('fills one bar per 4 characters, capped at 4', () => {
    expect([0, 3, 4, 11, 12, 16, 40].map((n) => passwordStrength('x'.repeat(n)))).toEqual([0, 0, 1, 2, 3, 4, 4])
  })
})

describe('dashboard/tasks', () => {
  const payload = {
    as_of: '2026-09-24T08:00:00Z',
    items: [
      { kind: 'tickets_open', count: 7, high_priority: 2, oldest_wait_seconds: null },
      { kind: 'withdrawals_pending', count: 3, amount: 128000 },
      { kind: 'notifications_backlog', queued: 214, failed_total: 9, backlog_state: 'backlogged' },
    ],
  }

  it('侧栏与仪表盘共用的严格 schema：未知 kind、缺字段都判为不符约定', () => {
    expect(tasksSchema.safeParse(payload).success).toBe(true)
    expect(tasksSchema.safeParse({ ...payload, items: [{ kind: 'mystery', count: 1 }] }).success).toBe(false)
    expect(tasksSchema.safeParse({ ...payload, items: [{ kind: 'tickets_open', count: 7 }] }).success).toBe(false)
  })

  it('数据巡检条目：每项 code / count / checked_at，旧的 ledger_drift 不再认', () => {
    const integrity = { kind: 'integrity', checks: [{ code: 'ledger_drift', count: 2, checked_at: '2026-10-20T08:00:00Z' }] }
    expect(tasksSchema.safeParse({ ...payload, items: [integrity] }).success).toBe(true)
    expect(tasksSchema.safeParse({ ...payload, items: [{ kind: 'integrity', checks: [] }] }).success).toBe(true)
    expect(tasksSchema.safeParse({ ...payload, items: [{ kind: 'integrity', checks: [{ code: 'ledger_drift', count: -1, checked_at: 'x' }] }] }).success).toBe(false)
    expect(tasksSchema.safeParse({ ...payload, items: [{ kind: 'integrity', checks: [{ code: 'ledger_drift', count: 1 }] }] }).success).toBe(false)
    expect(tasksSchema.safeParse({ ...payload, items: [{ kind: 'ledger_drift', count: 0 }] }).success).toBe(false)
  })

  it('徽标取数：条目缺失（无权限）为 0', () => {
    const items = tasksSchema.parse(payload).items
    expect(taskCount(items, 'tickets_open')).toBe(7)
    expect(taskCount(items, 'withdrawals_pending')).toBe(3)
    expect(taskCount(items, 'nodes_offline')).toBe(0)
    expect(taskCount(undefined, 'tickets_open')).toBe(0)
  })

  it('navBadges：侧栏徽标映射与为 0 省略', () => {
    const badgeItems = tasksSchema.parse({
      as_of: '2026-09-24T08:00:00Z',
      items: [
        { kind: 'tickets_open', count: 7, high_priority: 0, oldest_wait_seconds: null },
        { kind: 'orders_pending_stale', count: 2, threshold_seconds: 3600 },
        { kind: 'withdrawals_pending', count: 0, amount: 0 },
        { kind: 'nodes_offline', count: 3, sample: [], longest_offline_seconds: null },
      ],
    }).items
    expect(navBadges(badgeItems)).toEqual({
      tickets: { count: 7, hot: true },
      billing: { count: 2 },
      nodes: { count: 3 },
    })
    expect(navBadges(undefined)).toEqual({})
  })

  it('ticketAlert：无工单为 null，有工单带最久等待', () => {
    const zero = tasksSchema.parse({
      as_of: '2026-09-24T08:00:00Z',
      items: [{ kind: 'tickets_open', count: 0, high_priority: 0, oldest_wait_seconds: null }],
    }).items
    expect(ticketAlert(zero)).toBe(null)
    expect(ticketAlert(undefined)).toBe(null)
    const items = tasksSchema.parse({
      as_of: '2026-09-24T08:00:00Z',
      items: [{ kind: 'tickets_open', count: 7, high_priority: 0, oldest_wait_seconds: 10800 }],
    }).items
    expect(ticketAlert(items)).toEqual({ count: 7, oldestWaitSeconds: 10800 })
  })

  it('formatWait 向下取整', () => {
    expect(formatWait(30)).toBe('不到 1 分钟')
    expect(formatWait(125)).toBe('2 分钟')
    expect(formatWait(10800)).toBe('3 小时')
    expect(formatWait(200000)).toBe('2 天')
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
