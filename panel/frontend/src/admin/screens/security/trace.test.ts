import { describe, expect, it } from 'vitest'
import {
  accountsText,
  datacenterShare,
  distinctIpText,
  networkLabel,
  sortByRegions,
  traceAccountsResponse,
  traceIPsResponse,
  traceQuery,
  traceRevealResponse,
  traceSubscribersResponse,
  traceUserLabel,
  type TraceAccount,
  type TraceIP,
  type TraceOverview,
  type TraceSubscriber,
} from './trace'

const overview: TraceOverview = {
  distinct_ips: 1234,
  distinct_ips_approx: false,
  analyzed_ips: 8,
  datacenter_ips: 1,
  fetches: 540,
  fetch_failures: 12,
  logins: 90,
  login_failures: 3,
  top_regions: [{ region: '中国 广东', ips: 40, hits: 210 }],
}

const ip: TraceIP = {
  token: 'tok-ab12',
  ip_masked: '198.51.*.*',
  geo: '中国 广东 移动',
  network_kind: 'mobile',
  hits: 61,
  fetches: 52,
  events: 9,
  accounts: 3,
  accounts_capped: false,
  ua_kinds: 2,
  last_seen: '2026-09-25T10:00:00Z',
  users: [{ id: '0192abcd-ef00-7000-8000-000000000001', email: 'a@example.test', status: 'active' }],
}

const subscriber: TraceSubscriber = {
  user_id: '0192abcd-ef00-7000-8000-000000000001',
  email: 'a@example.test',
  status: 'active',
  fetches: 120,
  failures: 4,
  ips: 6,
  ua_kinds: 2,
  regions: 3,
  families: ['clash'],
  last_fetch: '2026-09-25T09:00:00Z',
}

const account: TraceAccount = {
  user_id: '0192abcd-ef00-7000-8000-000000000001',
  email: 'a@example.test',
  status: 'active',
  linked_ips: 7,
  logins: 20,
  login_ips: 4,
  fetch_ips: 5,
  shared_ips: 2,
  ua_kinds: 3,
  datacenter_hits: 1,
  last_seen: '2026-09-25T10:00:00Z',
}

describe('trace schemas', () => {
  it('parses the three list responses', () => {
    expect(traceIPsResponse.safeParse({ window: '24h', since: '2026-09-24T10:00:00Z', overview, ips: [ip] }).success).toBe(true)
    expect(traceSubscribersResponse.safeParse({ window: '7d', since: '2026-09-18T10:00:00Z', sort: 'fetches', subscribers: [subscriber] }).success).toBe(true)
    expect(traceAccountsResponse.safeParse({ window: '30d', since: '2026-08-26T10:00:00Z', sort: 'linked_ips', accounts: [account] }).success).toBe(true)
    expect(traceRevealResponse.safeParse({ ip: '198.51.100.18', geo: '中国 广东 移动', network_kind: 'mobile' }).success).toBe(true)
  })

  it('requires every field: a row without accounts_capped is rejected', () => {
    const partial: Record<string, unknown> = { ...ip }
    delete partial.accounts_capped
    expect(traceIPsResponse.safeParse({ window: '24h', since: '2026-09-24T10:00:00Z', overview, ips: [partial] }).success).toBe(false)
  })
})

describe('trace wording', () => {
  it('marks the distinct IP count as approximate when a source was capped', () => {
    expect(distinctIpText(overview)).toBe('1,234')
    expect(distinctIpText({ ...overview, distinct_ips: 5000, distinct_ips_approx: true })).toBe('约 5,000')
  })

  it('shows the datacenter share with one decimal and drops .0', () => {
    expect(datacenterShare({ ...overview, analyzed_ips: 0, datacenter_ips: 0 })).toBe('—')
    expect(datacenterShare({ ...overview, analyzed_ips: 2, datacenter_ips: 1 })).toBe('50%')
    expect(datacenterShare({ ...overview, analyzed_ips: 8, datacenter_ips: 1 })).toBe('12.5%')
  })

  it('writes ≥ when the linked accounts hit the detail cap', () => {
    expect(accountsText(ip)).toBe('3')
    expect(accountsText({ ...ip, accounts: 20, accounts_capped: true })).toBe('≥20')
  })

  it('names network kinds, hiding the unknown ones', () => {
    expect(networkLabel('datacenter')).toBe('机房')
    expect(networkLabel('residential')).toBe('家宽')
    expect(networkLabel('mobile')).toBe('移动网络')
    expect(networkLabel('education')).toBe('教育网')
    expect(networkLabel('private')).toBe('内网')
    expect(networkLabel('loopback')).toBe('本机')
    expect(networkLabel('satellite')).toBe('')
  })

  it('labels deleted accounts by their short id', () => {
    expect(traceUserLabel({ id: '0192abcd-ef00-7000-8000-000000000001', email: 'a@example.test', status: 'active' })).toBe('a@example.test')
    expect(traceUserLabel({ id: '0192abcdef007000', email: 'gone@example.test', status: 'deleted' })).toBe('已注销 #0192abcd')
  })
})

describe('trace sorting and query', () => {
  it('sorts by region count in the page without touching the source array', () => {
    const rows: TraceSubscriber[] = [
      { ...subscriber, user_id: 'u1', regions: 2, fetches: 10 },
      { ...subscriber, user_id: 'u2', regions: 5, fetches: 3 },
      { ...subscriber, user_id: 'u3', regions: 2, fetches: 30 },
    ]
    const sorted = sortByRegions(rows)
    expect(sorted.map((r) => r.user_id)).toEqual(['u2', 'u3', 'u1'])
    expect(rows.map((r) => r.user_id)).toEqual(['u1', 'u2', 'u3'])
  })

  it('leaves the default window out of the query string', () => {
    expect(traceQuery('24h')).toEqual({})
    expect(traceQuery('7d', 'ips')).toEqual({ window: '7d', sort: 'ips' })
    expect(traceQuery('30d')).toEqual({ window: '30d' })
  })
})
