import { describe, expect, it } from 'vitest'
import {
  addDomainBody,
  defaultScopeValue,
  domainScopeOptions,
  flattenDomains,
  parseHost,
  removeDomainBody,
  scopeForGroup,
  type DomainConfig,
} from './domainGroups'

const limits = { groups: 2, domains_per_group: 3, weight_max: 100 }

const G_DEFAULT = 'd0a1b2c3-0000-4000-8000-000000000001'
const G_VIP = 'd0a1b2c3-0000-4000-8000-000000000002'
const G_SOLO = 'd0a1b2c3-0000-4000-8000-000000000003'
const U_VIP = 'a0a1b2c3-0000-4000-8000-000000000010'
const U_PRO = 'a0a1b2c3-0000-4000-8000-000000000011'
const U_GONE = 'a0a1b2c3-0000-4000-8000-000000000099'

const userGroups = [
  { id: U_VIP, name: 'VIP' },
  { id: U_PRO, name: '专业版' },
]

function cfg(groups: DomainConfig['groups']): DomainConfig {
  return { groups, scores_available: true, limits }
}

const multiGroupCfg = cfg([
  {
    id: G_DEFAULT,
    name: '缺省',
    is_default: true,
    domains: [{ base_url: 'https://sub-a.example.com', weight: 2, score: 1, effective_weight: 2 }],
    user_group_ids: [],
  },
  {
    id: G_VIP,
    name: 'VIP 专线',
    is_default: false,
    domains: [{ base_url: 'https://vip.example.com', weight: 1, score: 1, effective_weight: 1 }],
    user_group_ids: [U_VIP],
  },
])

describe('scopeForGroup / flattenDomains', () => {
  it('缺省组且无用户组：仅一组为「所有用户」，多组为「其余用户」', () => {
    const solo = cfg([
      {
        id: G_SOLO,
        name: '缺省',
        is_default: true,
        domains: [{ base_url: 'https://sub.example.com', weight: 1, score: 1, effective_weight: 1 }],
        user_group_ids: [],
      },
    ])
    expect(scopeForGroup(solo.groups[0]!, solo.groups, userGroups)).toBe('所有用户')
    expect(flattenDomains(solo, userGroups)[0]!.scope).toBe('所有用户')
    expect(flattenDomains(solo, userGroups)[0]!.groupName).toBe('缺省')

    const def = multiGroupCfg.groups[0]!
    expect(scopeForGroup(def, multiGroupCfg.groups, userGroups)).toBe('其余用户')
    expect(flattenDomains(multiGroupCfg, userGroups)[0]!.scope).toBe('其余用户')
  })

  it('缺省组且分配了用户组：「名称与其余用户」', () => {
    const c = cfg([
      {
        id: G_DEFAULT,
        name: '缺省',
        is_default: true,
        domains: [{ base_url: 'https://sub.example.com', weight: 1, score: 1, effective_weight: 1 }],
        user_group_ids: [U_VIP, U_PRO],
      },
    ])
    expect(scopeForGroup(c.groups[0]!, c.groups, userGroups)).toBe('VIP、专业版与其余用户')
  })

  it('非缺省组有用户组：「仅 名称」', () => {
    const g = multiGroupCfg.groups[1]!
    expect(scopeForGroup(g, multiGroupCfg.groups, userGroups)).toBe('仅 VIP')
    expect(flattenDomains(multiGroupCfg, userGroups)[1]!.scope).toBe('仅 VIP')
  })

  it('非缺省组无用户组：「未分配用户组（不会用到）」', () => {
    const c = cfg([
      {
        id: G_VIP,
        name: '空组',
        is_default: false,
        domains: [{ base_url: 'https://orphan.example.com', weight: 1, score: 1, effective_weight: 1 }],
        user_group_ids: [],
      },
    ])
    expect(scopeForGroup(c.groups[0]!, c.groups, userGroups)).toBe('未分配用户组（不会用到）')
  })

  it('用户组 id 找不到时显示「已删除的用户组」', () => {
    const c = cfg([
      {
        id: G_VIP,
        name: '旧分配',
        is_default: false,
        domains: [{ base_url: 'https://old.example.com', weight: 1, score: 1, effective_weight: 1 }],
        user_group_ids: [U_GONE],
      },
    ])
    expect(scopeForGroup(c.groups[0]!, c.groups, userGroups)).toBe('仅 已删除的用户组')
  })
})

describe('parseHost', () => {
  it('接受常见写法', () => {
    expect(parseHost('Sub.Example.com ')).toBe('sub.example.com')
    expect(parseHost('https://sub.example.com/')).toBe('sub.example.com')
  })

  it('拒绝 IP、无点、端口、路径', () => {
    expect(parseHost('1.2.3.4')).toBeNull()
    expect(parseHost('sub')).toBeNull()
    expect(parseHost('https://sub.example.com:8443')).toBeNull()
    expect(parseHost('sub.example.com/path')).toBeNull()
  })
})

describe('addDomainBody', () => {
  const withDefault = cfg([
    {
      id: G_DEFAULT,
      name: '缺省',
      is_default: true,
      domains: [{ base_url: 'https://sub-a.example.com', weight: 2, score: 1, effective_weight: 2 }],
      user_group_ids: [],
    },
    {
      id: G_VIP,
      name: 'VIP 专线',
      is_default: false,
      domains: [{ base_url: 'https://vip.example.com', weight: 3, score: 1, effective_weight: 3 }],
      user_group_ids: [U_VIP],
    },
  ])

  it('加到缺省组末尾且 weight 为 1，其他组原样', () => {
    const out = addDomainBody(withDefault, 'new.example.com')
    expect('error' in out).toBe(false)
    if ('error' in out) return
    expect(out.host).toBe('new.example.com')
    expect(out.body.groups[0]!.domains).toEqual([
      { base_url: 'https://sub-a.example.com', weight: 2 },
      { base_url: 'https://new.example.com', weight: 1 },
    ])
    expect(out.body.groups[1]).toEqual({
      name: 'VIP 专线',
      is_default: false,
      domains: [{ base_url: 'https://vip.example.com', weight: 3 }],
      user_group_ids: [U_VIP],
    })
  })

  it('没有缺省组时新建「缺省」组', () => {
    const noDefault = cfg([
      {
        id: G_VIP,
        name: 'VIP 专线',
        is_default: false,
        domains: [{ base_url: 'https://vip.example.com', weight: 1, score: 1, effective_weight: 1 }],
        user_group_ids: [],
      },
    ])
    const out = addDomainBody(noDefault, 'sub.example.com')
    expect('error' in out).toBe(false)
    if ('error' in out) return
    expect(out.body.groups).toHaveLength(2)
    expect(out.body.groups[1]).toEqual({
      name: '缺省',
      is_default: true,
      domains: [{ base_url: 'https://sub.example.com', weight: 1 }],
      user_group_ids: [],
    })
  })

  it('已有「缺省」组名时新建「缺省 2」', () => {
    const named = cfg([
      {
        id: G_VIP,
        name: '缺省',
        is_default: false,
        domains: [{ base_url: 'https://vip.example.com', weight: 1, score: 1, effective_weight: 1 }],
        user_group_ids: [],
      },
    ])
    const out = addDomainBody(named, 'sub.example.com')
    expect('error' in out).toBe(false)
    if ('error' in out) return
    const newG = out.body.groups.find((g) => g.is_default)
    expect(newG?.name).toBe('缺省 2')
  })

  it('重复域名、缺省组已满、组数已满', () => {
    expect(addDomainBody(withDefault, 'sub-a.example.com')).toEqual({ error: '这个域名已经在列表里' })

    const fullDefault = cfg([
      {
        id: G_DEFAULT,
        name: '缺省',
        is_default: true,
        domains: [
          { base_url: 'https://a.example.com', weight: 1, score: 1, effective_weight: 1 },
          { base_url: 'https://b.example.com', weight: 1, score: 1, effective_weight: 1 },
          { base_url: 'https://c.example.com', weight: 1, score: 1, effective_weight: 1 },
        ],
        user_group_ids: [],
      },
    ])
    expect(addDomainBody(fullDefault, 'd.example.com')).toEqual({ error: '缺省组最多 3 个域名' })

    const twoGroupsNoDefault = cfg([
      {
        id: G_VIP,
        name: '甲',
        is_default: false,
        domains: [{ base_url: 'https://a.example.com', weight: 1, score: 1, effective_weight: 1 }],
        user_group_ids: [],
      },
      {
        id: G_SOLO,
        name: '乙',
        is_default: false,
        domains: [{ base_url: 'https://b.example.com', weight: 1, score: 1, effective_weight: 1 }],
        user_group_ids: [],
      },
    ])
    expect(addDomainBody(twoGroupsNoDefault, 'sub.example.com')).toEqual({ error: '订阅域名组最多 2 个' })
  })

  it('指定组 id 时加进该组；该组满了报错；空串走缺省', () => {
    const out = addDomainBody(withDefault, 'vip2.example.com', G_VIP)
    expect('error' in out).toBe(false)
    if ('error' in out) return
    expect(out.body.groups[1]!.domains.map((d) => d.base_url)).toContain('https://vip2.example.com')

    const fullVip = cfg([
      {
        id: G_DEFAULT,
        name: '缺省',
        is_default: true,
        domains: [{ base_url: 'https://a.example.com', weight: 1, score: 1, effective_weight: 1 }],
        user_group_ids: [],
      },
      {
        id: G_VIP,
        name: 'VIP 专线',
        is_default: false,
        domains: [
          { base_url: 'https://v1.example.com', weight: 1, score: 1, effective_weight: 1 },
          { base_url: 'https://v2.example.com', weight: 1, score: 1, effective_weight: 1 },
          { base_url: 'https://v3.example.com', weight: 1, score: 1, effective_weight: 1 },
        ],
        user_group_ids: [U_VIP],
      },
    ])
    expect(addDomainBody(fullVip, 'x.example.com', G_VIP)).toEqual({ error: '「VIP 专线」组最多 3 个域名' })

    const viaEmpty = addDomainBody(withDefault, 'def-new.example.com', '')
    expect('error' in viaEmpty).toBe(false)
    if ('error' in viaEmpty) return
    expect(viaEmpty.body.groups[0]!.domains.some((d) => d.base_url === 'https://def-new.example.com')).toBe(true)
  })
})

describe('domainScopeOptions / defaultScopeValue', () => {
  it('缺省组只显示组名；非缺省带 scope；无缺省组时前置「缺省（新建）」', () => {
    const opts = domainScopeOptions(multiGroupCfg, userGroups)
    expect(opts).toEqual([
      { value: G_DEFAULT, label: '缺省' },
      { value: G_VIP, label: 'VIP 专线 · 仅 VIP' },
    ])
    expect(defaultScopeValue(multiGroupCfg)).toBe(G_DEFAULT)

    const noDefault = cfg([
      {
        id: G_VIP,
        name: 'VIP 专线',
        is_default: false,
        domains: [{ base_url: 'https://vip.example.com', weight: 1, score: 1, effective_weight: 1 }],
        user_group_ids: [U_VIP],
      },
    ])
    expect(domainScopeOptions(noDefault, userGroups)[0]).toEqual({ value: '', label: '缺省（新建）' })
    expect(defaultScopeValue(noDefault)).toBe('')
  })
})

describe('removeDomainBody', () => {
  const c = cfg([
    {
      id: G_DEFAULT,
      name: '缺省',
      is_default: true,
      domains: [
        { base_url: 'https://sub-a.example.com', weight: 1, score: 1, effective_weight: 1 },
        { base_url: 'https://sub-b.example.com', weight: 1, score: 1, effective_weight: 1 },
      ],
      user_group_ids: [],
    },
    {
      id: G_VIP,
      name: 'VIP 专线',
      is_default: false,
      domains: [{ base_url: 'https://vip.example.com', weight: 1, score: 1, effective_weight: 1 }],
      user_group_ids: [U_VIP],
    },
  ])

  it('普通移除只删一条域名', () => {
    const out = removeDomainBody(c, G_DEFAULT, 'https://sub-a.example.com')
    expect(out.dropsGroup).toBe(false)
    expect(out.body.groups[0]!.domains).toEqual([{ base_url: 'https://sub-b.example.com', weight: 1 }])
    expect(out.body.groups).toHaveLength(2)
  })

  it('组里最后一个域名时 dropsGroup 且整组删除', () => {
    const out = removeDomainBody(c, G_VIP, 'https://vip.example.com')
    expect(out.dropsGroup).toBe(true)
    expect(out.groupName).toBe('VIP 专线')
    expect(out.body.groups).toHaveLength(1)
    expect(out.body.groups[0]!.name).toBe('缺省')
  })
})
