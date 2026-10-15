import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { GROUPS } from '../dev/mock/admin/users'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin subscription domains', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => close(server))

  const call = (method: string, path: string, body?: unknown) => mockFetch(base, auth, method, path, body)
  const json = async (res: Response) => (await res.json()) as Record<string, unknown>
  const get = async () => json(await call('GET', '/v1/subscription-domains'))

  // 种子的输入形状：重置状态用（PUT 会重新生成组 id）
  const SEED_INPUT = [
    {
      name: '缺省',
      is_default: true,
      domains: [
        { base_url: 'https://sub-a.example.com', weight: 2 },
        { base_url: 'https://sub-b.example.com', weight: 1 },
      ],
      user_group_ids: [],
    },
    {
      name: 'VIP 专线',
      is_default: false,
      domains: [{ base_url: 'https://vip.example.com', weight: 1 }],
      user_group_ids: [GROUPS[0]!.id],
    },
  ]

  it('reads seeded groups with scores and limits', async () => {
    const res = await get()
    expect(res.scores_available).toBe(true)
    expect(res.limits).toEqual({ groups: 20, domains_per_group: 10, weight_max: 100 })
    expect(res.groups).toHaveLength(2)

    const def = (res.groups as Record<string, unknown>[])[0]!
    expect(def).toMatchObject({ id: 'd0a1b2c3-0000-4000-8000-000000000001', name: '缺省', is_default: true })
    expect(def.domains).toEqual([
      { base_url: 'https://sub-a.example.com', weight: 2, score: 1.6, effective_weight: 3.2 },
      { base_url: 'https://sub-b.example.com', weight: 1, score: 0.4, effective_weight: 0.4 },
    ])
    expect(def.user_group_ids).toEqual([])

    const vip = (res.groups as Record<string, unknown>[])[1]!
    expect(vip).toMatchObject({
      id: 'd0a1b2c3-0000-4000-8000-000000000002',
      name: 'VIP 专线',
      is_default: false,
      user_group_ids: [GROUPS[0]!.id],
    })
    expect(vip.domains).toEqual([{ base_url: 'https://vip.example.com', weight: 1, score: 1, effective_weight: 1 }])
  })

  it('replaces the whole config on a valid PUT and returns the same shape as GET', async () => {
    const put = await call('PUT', '/v1/subscription-domains', {
      groups: [
        {
          name: ' 缺省 ',
          is_default: true,
          domains: [
            { base_url: 'https://sub-a.example.com', weight: 2 },
            { base_url: 'https://sub-b.example.com' },
          ],
        },
        { name: 'VIP 专线', domains: [{ base_url: ' HTTPS://Sub-C.Example.com/ ' }], user_group_ids: [GROUPS[0]!.id] },
      ],
    })
    expect(put.status).toBe(200)
    const saved = await json(put)
    const vip = (saved.groups as Record<string, unknown>[])[1]!
    expect(vip.domains).toEqual([{ base_url: 'https://sub-c.example.com', weight: 1, score: 1, effective_weight: 1 }])
    expect(vip.user_group_ids).toEqual([GROUPS[0]!.id])
    expect(await get()).toEqual(saved)
  })

  it('validates the whole payload and keeps the previous state on failure', async () => {
    // 先放回一份已知状态，每个失败用例之后 GET 应仍是它
    expect((await call('PUT', '/v1/subscription-domains', { groups: SEED_INPUT })).status).toBe(200)
    const snapshot = await get()

    const cases: Array<[unknown, string]> = [
      [{ groups: [{ name: '   ', is_default: true, domains: [{ base_url: 'https://a.example.com' }] }] }, '第 1 组：名称不能为空'],
      [
        {
          groups: [
            { name: 'A', is_default: true, domains: [{ base_url: 'https://a.example.com' }] },
            { name: 'B', is_default: true, domains: [{ base_url: 'https://b.example.com' }] },
          ],
        },
        '只能有一个缺省组',
      ],
      [{ groups: [{ name: 'A', domains: [{ base_url: 'http://a.example.com' }] }] }, '第 1 组第 1 个域名：必须以 https:// 开头'],
      [{ groups: [{ name: 'A', domains: [{ base_url: 'https://a.example.com/path' }] }] }, '第 1 组第 1 个域名：只能写到域名（可带端口），不能带路径、参数'],
      [{ groups: [{ name: 'A', domains: [{ base_url: 'https://a_b.example.com' }] }] }, '第 1 组第 1 个域名：域名只能含小写字母、数字、点和短横线（中文域名请写成 xn-- 开头的形式）'],
      [{ groups: [{ name: 'A', domains: [{ base_url: 'https://a.example.com:70000' }] }] }, '第 1 组第 1 个域名：端口必须在 1–65535 之间'],
      [
        {
          groups: [{ name: 'A', domains: [{ base_url: 'https://a.example.com' }, { base_url: 'https://a.example.com/' }] }],
        },
        '第 1 组：https://a.example.com 重复了',
      ],
      [{ groups: [{ name: 'A', domains: [{ base_url: 'https://a.example.com', weight: 101 }] }] }, '第 1 组第 1 个域名：权重必须是 1–100 的整数'],
      [
        { groups: [{ name: 'A', domains: [{ base_url: 'https://a.example.com' }], user_group_ids: [GROUPS[0]!.id, GROUPS[0]!.id] }] },
        '同一个用户组只能分到一个域名组',
      ],
      [
        { groups: [{ name: 'A', domains: [{ base_url: 'https://a.example.com' }], user_group_ids: ['9c0e1a2b-2222-4b00-8000-000000000099'] }] },
        '有用户组不存在（可能刚被删除），刷新后重试',
      ],
      [{ groups: [{ name: 'A', domains: [{ base_url: 'https://a.example.com' }], user_group_ids: ['not-a-uuid'] }] }, '用户组 id 格式不正确'],
    ]
    for (const [body, message] of cases) {
      const res = await call('PUT', '/v1/subscription-domains', body)
      expect(res.status).toBe(422)
      expect(await json(res)).toMatchObject({ error: { code: 'validation_failed', fields: { groups: message } } })
      expect(await get()).toEqual(snapshot)
    }
  })

  it('clears all groups with an empty list', async () => {
    const put = await call('PUT', '/v1/subscription-domains', { groups: [] })
    expect(put.status).toBe(200)
    expect(await json(put)).toMatchObject({ groups: [] })
    expect(await get()).toMatchObject({ groups: [] })
  })

  it('returns reauth_required on PUT without recent reauth', async () => {
    await fetch(`${base}/__mock/expire-reauth`, { method: 'POST' })
    const res = await call('PUT', '/v1/subscription-domains', { groups: [] })
    expect(res.status).toBe(403)
    expect(await json(res)).toMatchObject({ error: { code: 'reauth_required' } })
  })
})
