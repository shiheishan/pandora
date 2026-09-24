/**
 * [INPUT]: 依赖 vitest，依赖 ./modules、./reauth、./me、./ChangePasswordDialog 的 passwordStrength、./EventsCapsule 的 describeEvent
 * [OUTPUT]: 对外提供 admin 外框纯逻辑的单元测试
 * [POS]: admin 的单元测试：路由规范化与标签回落、⌘K 筛选、reauth 桥的单次弹框与结算、身份文字的契约映射与回退、强度条、实时事件条目；界面交互在浏览器里对 dev/mock-api 验收
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it, vi } from 'vitest'
import { passwordStrength } from './ChangePasswordDialog'
import { describeEvent } from './EventsCapsule'
import { identityLabels } from './me'
import { modulePath, paletteItems, resolveRoute } from './modules'
import { createReauthController } from './reauth'

describe('resolveRoute', () => {
  it('keeps valid module/tab paths', () => {
    expect(resolveRoute('/users/groups')).toEqual({ module: 'users', tab: 'groups', canonical: '/users/groups' })
    expect(resolveRoute('/tickets')).toEqual({ module: 'tickets', tab: null, canonical: '/tickets' })
  })

  it('falls back to the first tab, drops tabs on tabless modules, and sends unknown paths to the dashboard', () => {
    expect(resolveRoute('/users').canonical).toBe('/users/list')
    expect(resolveRoute('/users/nope').canonical).toBe('/users/list')
    expect(resolveRoute('/plans/extra').canonical).toBe('/plans')
    expect(resolveRoute('/').canonical).toBe('/dash')
    expect(resolveRoute('/toString').canonical).toBe('/dash')
    expect(resolveRoute('/a/b/c').canonical).toBe('/dash')
  })

  it('modulePath builds canonical links', () => {
    expect(modulePath('billing')).toBe('/billing/orders')
    expect(modulePath('billing', 'arrears')).toBe('/billing/arrears')
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
