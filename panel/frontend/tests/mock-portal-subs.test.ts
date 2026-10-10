import { readFileSync } from 'node:fs'
import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { subscriptionsSchema } from '../src/portal/subscription-schema'
import { renameSchema } from '../src/portal/screens/subs/schemas'
import { giftCardSchema } from '../src/portal/screens/wallet/schemas'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

// 转移接口的几句文案以 Go 为准：从源码里取，假后端与 Go 任何一边改了这里就红
const goSource = (path: string) => readFileSync(new URL(`../../internal/${path}`, import.meta.url), 'utf8')
const pick = (src: string, re: RegExp, what: string): string => {
  const m = re.exec(src)
  if (!m?.[1]) throw new Error(`Go 源码里找不到 ${what}`)
  return m[1]
}
const transferGo = goSource('domain/billing/traffic_pack_transfer.go')
const goTransfer = {
  source: pick(transferGo, /errTransferSource\s*=\s*httpx\.New\(httpx\.CodeConflict,\s*"([^"]+)"\)/, 'errTransferSource'),
  sameTarget: pick(transferGo, /"to_subscription_id":\s*"([^"]+)"/, '同一份的 422 文案'),
  invalid: pick(goSource('platform/httpx/httpx.go'), /func Invalid\(fields map\[string\]string\) \*Error \{\s*return &Error\{Code: CodeValidationFailed, Message: "([^"]+)"/, 'httpx.Invalid 的 message'),
}

// 设计稿 2.5 / 2.7 / 2.8 / 2.9：订阅列表新字段、改名、按份换新链接、礼品卡落点、流量包转移、原型场景
describe('mock api · portal subscriptions (purchase model)', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  let n = 0
  const call = (method: string, path: string, body?: unknown, idem = false) => mockFetch(base, auth, method, path, body, idem ? `s-${++n}` : undefined)
  const scenario = async (name: string) => expect((await call('POST', '/v1/__mock/portal-scenario', { name })).status).toBe(200)
  const subs = async () => subscriptionsSchema.parse(await (await call('GET', '/v1/me/subscriptions')).json())

  beforeAll(async () => {
    ;({ server, base } = await serve('portal'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.portal)).access_token)
  })
  afterAll(() => close(server))

  it('列表带备注名、配置名、续到哪天与这一份自己的流量包；顶层有未分配的余量', async () => {
    await scenario('proto-s2')
    const list = await subs()
    expect(list.unattached_pack_bytes).toBe(0)
    expect(list.subscriptions.map((s) => [s.label, s.client_name, s.changeable])).toEqual([
      ['我的', 'Pandora · 我的', true],
      ['妈妈的 iPad', 'Pandora · 妈妈的 iPad', true],
    ])
    expect(list.subscriptions.every((s) => s.renew_until !== null)).toBe(true)
    await scenario('default')
    expect((await subs()).subscriptions[0]!.pack_remaining_bytes).toBe(30 * 1024 ** 3)
  })

  it('改名：同一用户下不分大小写唯一（409），超长 422，空串清除', async () => {
    await scenario('proto-s2')
    const [mine, mom] = (await subs()).subscriptions
    expect((await call('PATCH', `/v1/me/subscriptions/${mom!.id}`, { label: '我的' })).status).toBe(409)
    expect((await call('PATCH', `/v1/me/subscriptions/${mom!.id}`, { label: '一二三四五六七八九十一二三四五六七' })).status).toBe(422)
    const ok = renameSchema.parse(await (await call('PATCH', `/v1/me/subscriptions/${mom!.id}`, { label: ' 妈妈 ' })).json())
    expect(ok).toEqual({ label: '妈妈', client_name: 'Pandora · 妈妈' })
    expect(renameSchema.parse(await (await call('PATCH', `/v1/me/subscriptions/${mine!.id}`, { label: null })).json())).toEqual({ label: null, client_name: 'Pandora · 标准版' })
  })

  it('换新链接按份限频：换了 A 马上换 B 可以，10 分钟内再换 A 回 429 并写剩余分钟', async () => {
    await scenario('proto-s6')
    const [a, b] = (await subs()).subscriptions
    expect((await call('POST', `/v1/me/subscriptions/${a!.id}/rotate`)).status).toBe(200)
    expect((await call('POST', `/v1/me/subscriptions/${b!.id}/rotate`)).status).toBe(200)
    const again = await call('POST', `/v1/me/subscriptions/${a!.id}/rotate`)
    expect(again.status).toBe(429)
    expect(((await again.json()) as { error: { message: string } }).error.message).toMatch(/^操作太频繁，请 \d+ 分钟后再试$/)
  })

  it('礼品卡落点：同款卡预选同款（S5b），不同款卡不预选；choice 不在选项里回 422 且卡没被用掉', async () => {
    await scenario('proto-s5b')
    const preview = async (code: string) => giftCardSchema.parse(((await (await call('POST', '/v1/gift-cards/preview', { code })).json()) as { card: unknown }).card)
    const std = await preview('A7K2-STD1-M9QX')
    expect(std.placement?.options.map((o) => [o.kind, o.badge ?? ''])).toEqual([
      ['renew', 'same_plan'],
      ['new', ''],
      ['change', ''],
    ])
    expect(std.placement?.default_key).toBe(std.placement?.options[0]?.key)
    const pro = await preview('B3P8-PRO1-W4RT')
    expect(pro.placement?.default_key).toBe('')
    const days = await preview('C5D3-D030-H8NE')
    expect(days.placement?.options[0]).toMatchObject({ kind: 'extend_days', badge: 'soonest_expiry', label: '妈妈的 iPad' })
    expect((await preview('GC-0923-H2K9-7QPA')).placement).toBeNull()
    const stale = await call('POST', '/v1/gift-cards/redeem', { code: 'B3P8-PRO1-W4RT', choice: { kind: 'renew', subscription_id: 'nope' } }, true)
    expect(stale.status).toBe(422)
    const none = await call('POST', '/v1/gift-cards/redeem', { code: 'B3P8-PRO1-W4RT' }, true)
    expect(none.status).toBe(422)
    const ok = await call('POST', '/v1/gift-cards/redeem', { code: 'B3P8-PRO1-W4RT', choice: { kind: 'new' } }, true)
    expect(ok.status).toBe(200)
    expect((await subs()).subscriptions).toHaveLength(3)
  })

  it('流量包转移：只能从未分配或彻底停用的那份转到在用的那份；重复转 moved_bytes=0', async () => {
    // multi：第一份挂着 30G 流量包、第二份待续费（也算在用）
    await scenario('multi')
    const [first, second] = (await subs()).subscriptions
    expect(first!.pack_remaining_bytes).toBe(30 * 1024 ** 3)
    // 从在用的那份转出一律 409（用户 10-09 删掉了「升级前旧包挪一次」），两边余量不动
    const refused = await call('POST', '/v1/me/traffic-packs/transfer', { from_subscription_id: first!.id, to_subscription_id: second!.id })
    expect(refused.status).toBe(409)
    // 文案与 Go 的 billing.errTransferSource 逐字一致（从 Go 源码里取，不手抄）
    expect((await refused.json()) as { error: { code: string; message: string } }).toMatchObject({ error: { code: 'conflict', message: goTransfer.source } })
    // 来源与目标是同一份：和 Go 的 TransferTrafficPacks 入口一样先回 422，字段与文案一致
    const same = await call('POST', '/v1/me/traffic-packs/transfer', { from_subscription_id: first!.id, to_subscription_id: first!.id })
    expect(same.status).toBe(422)
    expect((await same.json()) as { error: { code: string; message: string; fields: Record<string, string> } }).toMatchObject({
      error: { code: 'validation_failed', message: goTransfer.invalid, fields: { to_subscription_id: goTransfer.sameTarget } },
    })
    expect((await subs()).subscriptions.map((s) => s.pack_remaining_bytes)).toEqual([30 * 1024 ** 3, second!.pack_remaining_bytes])
    const res = await call('POST', '/v1/me/traffic-packs/transfer', { from_subscription_id: null, to_subscription_id: second!.id })
    expect(await res.json()).toEqual({ moved_bytes: 0 })
  })

  it('每一份不再带「可挪一次的旧流量包」字段（用户 10-09 删掉），与 Go 的 MySubscription 一致', async () => {
    for (const s of ['default', 'multi', 'proto-s2'] as const) {
      await scenario(s)
      const raw = (await (await call('GET', '/v1/me/subscriptions')).json()) as { subscriptions: Array<Record<string, unknown>> }
      expect(raw.subscriptions.length).toBeGreaterThan(0)
      for (const sub of raw.subscriptions) expect(Object.keys(sub)).not.toContain('legacy_movable_pack_bytes')
    }
  })

  it('原型场景入口：切场景并跳到起始页', async () => {
    const res = await fetch(`${base}/v1/__mock/proto?s=proto-s5a`, { redirect: 'manual' })
    expect(res.status).toBe(302)
    expect(res.headers.get('location')).toBe('/#/wallet/redeem')
    expect((await fetch(`${base}/v1/__mock/proto?s=default`, { redirect: 'manual' })).status).toBe(422)
  })
})
