import { describe, expect, it } from 'vitest'
import { bodyFrom, effectiveText, formFrom, newGroup, type DomainConfig, type GroupForm } from './domainGroups'

const limits = { groups: 2, domains_per_group: 3, weight_max: 100 }

const cfg: DomainConfig = {
  groups: [
    {
      id: 'g-1',
      name: '缺省',
      is_default: true,
      domains: [
        { base_url: 'https://sub-a.example.com', weight: 2, score: 1.6, effective_weight: 3.2 },
        { base_url: 'https://sub-b.example.com', weight: 1, score: 0.5, effective_weight: 0.5 },
      ],
      user_group_ids: ['u-1', 'u-2'],
    },
    {
      id: 'g-2',
      name: '备用',
      is_default: false,
      domains: [{ base_url: 'https://sub-c.example.com', weight: 1, score: 1, effective_weight: 1 }],
      user_group_ids: [],
    },
  ],
  scores_available: true,
  limits,
}

describe('formFrom', () => {
  it('round-trips config into form', () => {
    const form = formFrom(cfg)
    expect(form).toHaveLength(2)
    expect(form[0]).toEqual({
      key: 'g-1',
      name: '缺省',
      isDefault: true,
      domains: [
        { baseUrl: 'https://sub-a.example.com', weight: '2' },
        { baseUrl: 'https://sub-b.example.com', weight: '1' },
      ],
      userGroupIds: ['u-1', 'u-2'],
    })
  })
})

describe('newGroup', () => {
  it('is not default when a default already exists', () => {
    const g = newGroup(formFrom(cfg))
    expect(g.isDefault).toBe(false)
  })

  it('is default when no default exists yet', () => {
    const withoutDefault = formFrom(cfg).map((g) => ({ ...g, isDefault: false }))
    const g = newGroup(withoutDefault)
    expect(g.isDefault).toBe(true)
    expect(g.name).toBe('')
    expect(g.domains).toEqual([{ baseUrl: '', weight: '1' }])
    expect(g.userGroupIds).toEqual([])
  })

  it('increments the new-<n> key', () => {
    const g = newGroup(formFrom(cfg))
    expect(g.key).toBe('new-1')
    expect(newGroup([...formFrom(cfg), g]).key).toBe('new-2')
  })
})

describe('bodyFrom', () => {
  const group = (patch: Partial<GroupForm> = {}): GroupForm => ({
    key: 'g-1',
    name: '缺省',
    isDefault: true,
    domains: [{ baseUrl: 'https://sub-a.example.com', weight: '1' }],
    userGroupIds: [],
    ...patch,
  })

  it('errors when too many groups', () => {
    expect(bodyFrom([group(), group(), group()], limits)).toEqual({ error: '订阅域名组最多 2 个' })
  })

  it('errors when a name is empty', () => {
    expect(bodyFrom([group({ name: '  ' })], limits)).toEqual({ error: '第 1 组：名称不能为空' })
  })

  it('errors when a name exceeds 40 chars', () => {
    expect(bodyFrom([group({ name: '组'.repeat(41) })], limits)).toEqual({ error: '第 1 组：名称最多 40 个字' })
  })

  it('errors on duplicate names', () => {
    expect(bodyFrom([group({ name: '同' }), group({ name: ' 同 ', key: 'g-2', isDefault: false })], limits)).toEqual({
      error: '第 2 组：名称和第 1 组重复',
    })
  })

  it('errors when more than one default group', () => {
    expect(bodyFrom([group(), group({ key: 'g-2', isDefault: true, name: '另一组' })], limits)).toEqual({
      error: '只能有一个缺省组',
    })
  })

  it('errors when a group has no domain (empty rows ignored)', () => {
    expect(bodyFrom([group({ domains: [{ baseUrl: '   ', weight: '1' }] })], limits)).toEqual({
      error: '第 1 组：至少要有一个域名',
    })
  })

  it('errors when too many domains in a group', () => {
    const domains = Array.from({ length: 4 }, (_, i) => ({ baseUrl: `https://sub${i}.example.com`, weight: '1' }))
    expect(bodyFrom([group({ domains })], limits)).toEqual({ error: '第 1 组：域名最多 3 个' })
  })

  it('errors when a domain does not start with https://', () => {
    const domains = [
      { baseUrl: 'https://ok.example.com', weight: '1' },
      { baseUrl: 'http://bad.example.com', weight: '1' },
    ]
    expect(bodyFrom([group({ domains })], limits)).toEqual({ error: '第 1 组第 2 个域名：必须以 https:// 开头' })
  })

  it('errors on weight 0', () => {
    expect(bodyFrom([group({ domains: [{ baseUrl: 'https://a.example.com', weight: '0' }] })], limits)).toEqual({
      error: '第 1 组第 1 个域名：权重必须是 1–100 的整数',
    })
  })

  it('errors on weight over the max', () => {
    expect(bodyFrom([group({ domains: [{ baseUrl: 'https://a.example.com', weight: '101' }] })], limits)).toEqual({
      error: '第 1 组第 1 个域名：权重必须是 1–100 的整数',
    })
  })

  it('errors on non-integer weight', () => {
    expect(bodyFrom([group({ domains: [{ baseUrl: 'https://a.example.com', weight: '1.5' }] })], limits)).toEqual({
      error: '第 1 组第 1 个域名：权重必须是 1–100 的整数',
    })
  })

  it('errors when a user group is assigned twice', () => {
    const form = [
      group({ userGroupIds: ['u-1', 'u-1'] }),
      group({ key: 'g-2', name: '另一组', isDefault: false }),
    ]
    expect(bodyFrom(form, limits)).toEqual({ error: '同一个用户组只能分到一个域名组' })
  })

  it('builds the replacement body: trims, drops empty rows, weight as number', () => {
    const form = [
      {
        key: 'g-1',
        name: ' 缺省 ',
        isDefault: true,
        domains: [
          { baseUrl: ' https://sub-a.example.com ', weight: ' 2 ' },
          { baseUrl: '', weight: '9' },
        ],
        userGroupIds: ['u-1'],
      },
    ]
    expect(bodyFrom(form, limits)).toEqual({
      body: {
        groups: [
          {
            name: '缺省',
            is_default: true,
            domains: [{ base_url: 'https://sub-a.example.com', weight: 2 }],
            user_group_ids: ['u-1'],
          },
        ],
      },
    })
  })
})

describe('effectiveText', () => {
  const d = { weight: 2, score: 1.6, effective_weight: 3.2 }
  it('shows effective weight when scores are available', () => {
    expect(effectiveText(d, true)).toBe('当前有效权重 3.2（基础 2 × 分数 1.6）')
  })
  it('falls back to base weight when scores are unavailable', () => {
    expect(effectiveText(d, false)).toBe('当前按基础权重 2（暂时读不到分数）')
  })
})
