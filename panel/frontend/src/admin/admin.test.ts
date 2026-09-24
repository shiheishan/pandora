/**
 * [INPUT]: 依赖 vitest，依赖 ./modules、./reauth、./me、./ChangePasswordDialog 的 passwordStrength、./EventsCapsule 的 describeEvent
 * [OUTPUT]: 对外提供 admin 外框纯逻辑的单元测试
 * [POS]: admin 的单元测试：路由规范化、标签回落与 rest 子路由、读权限表与按权限取舍、⌘K 筛选与隐藏、reauth 桥的单次弹框与结算、身份文字的契约映射与回退、强度条、实时事件条目；界面交互在浏览器里对 dev/mock-api 验收
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it, vi } from 'vitest'
import { passwordStrength } from './ChangePasswordDialog'
import { describeEvent } from './EventsCapsule'
import { identityLabels } from './me'
import { MODULES, canRead, canReadModule, modulePath, paletteItems, resolveRoute, visibleTabs, type Permissions } from './modules'
import { createReauthController } from './reauth'

const ALL: Permissions = new Set(
  Object.values(MODULES).flatMap((def) => (def.read === null ? [] : typeof def.read === 'string' ? [def.read] : Object.values(def.read))),
)

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

  it('fills a missing or unknown tab with the first readable one when permissions are known', () => {
    const viewer: Permissions = new Set(['ops.notification.read'])
    expect(resolveRoute('/system', viewer).canonical).toBe('/system/templates')
    expect(resolveRoute('/system/nope/x', viewer).canonical).toBe('/system/templates')
    expect(resolveRoute('/system/notify', viewer).canonical).toBe('/system/notify')
    expect(resolveRoute('/security', viewer).canonical).toBe('/security/audit')
  })

  it('modulePath builds canonical links and prefers the first readable tab', () => {
    expect(modulePath('billing')).toBe('/billing/orders')
    expect(modulePath('billing', 'arrears')).toBe('/billing/arrears')
    expect(modulePath('dash', 'x')).toBe('/dash')
    expect(modulePath('users', null, new Set(['metering.reset.read']))).toBe('/users/resets')
    expect(modulePath('users', 'groups', new Set(['metering.reset.read']))).toBe('/users/groups')
  })
})

describe('read permissions', () => {
  it('declares a permission for every tab and nothing else', () => {
    for (const [key, def] of Object.entries(MODULES)) {
      if (def.tabs && def.read !== null && typeof def.read === 'object') {
        expect(Object.keys(def.read).sort(), key).toEqual(def.tabs.map(([tab]) => tab).sort())
      } else {
        expect(def.tabs, key).toBeUndefined()
      }
    }
  })

  it('checks module and tab permissions against GET v1/me', () => {
    const viewer: Permissions = new Set(['ops.ticket.read', 'node.read', 'marketing.giftcard.read'])
    expect(canRead('dash', null, new Set())).toBe(true)
    expect(canRead('tickets', null, viewer)).toBe(true)
    expect(canRead('users', 'list', viewer)).toBe(false)
    expect(canRead('marketing', 'gifts', viewer)).toBe(true)
    expect(canRead('marketing', 'coupons', viewer)).toBe(false)
    expect(canRead('marketing', null, viewer)).toBe(false)
    expect(canRead('marketing', 'toString', ALL)).toBe(false)
    expect(canReadModule('marketing', viewer)).toBe(true)
    expect(canReadModule('security', viewer)).toBe(false)
    expect(visibleTabs('marketing', viewer).map(([tab]) => tab)).toEqual(['gifts'])
    expect(visibleTabs('dash', ALL)).toEqual([])
  })
})

describe('paletteItems', () => {
  it('lists 12 entries by default and filters on title + path', () => {
    expect(paletteItems('', ALL)).toHaveLength(12)
    const gift = paletteItems('礼品', ALL)
    expect(gift.map((i) => [i.module, i.tab])).toEqual([['marketing', 'gifts']])
    expect(paletteItems('reality', ALL).some((i) => i.module === 'nodes')).toBe(true)
    expect(paletteItems('完全不存在', ALL)).toEqual([])
  })

  it('hides modules, tabs and deep links the admin cannot read', () => {
    const viewer: Permissions = new Set(['node.read'])
    const items = paletteItems('', viewer)
    expect(new Set(items.map((i) => i.module))).toEqual(new Set(['dash', 'nodes']))
    expect(paletteItems('礼品', viewer)).toEqual([])
    expect(paletteItems('reality', viewer).map((i) => i.tab)).toEqual(['nodes'])
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
  it('maps roles / display_name / email per the contract', () => {
    const me = { user_id: 'abcdef12-3456', permissions: [], reauthed: true, email: 'linzhou@pandora.run', display_name: '林舟', roles: [{ code: 'ops', name: '运维' }] }
    expect(identityLabels(me)).toEqual({ title: '运维 · 林舟', subtitle: 'linzhou@pandora.run', initial: '运' })
    expect(identityLabels({ ...me, display_name: null }).title).toBe('运维 · linzhou')
  })

  it('falls back while the backend has not added the fields yet', () => {
    expect(identityLabels({ user_id: 'abcdef12-3456', permissions: [], reauthed: false })).toEqual({ title: '管理员', subtitle: 'abcdef12', initial: '管' })
    expect(identityLabels(undefined).subtitle).toBe('')
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
})
