/**
 * [INPUT]: 依赖 vitest，依赖 ./logic 的全部纯函数，依赖 ./schemas 的 schema（核对 Go 的 null 切片与指针字段），依赖 ../../../../dev/mock/admin/node-schemas 的真实 schema 夹具
 * [OUTPUT]: 对外提供节点页纯逻辑的单元测试
 * [POS]: admin/screens/nodes 的单元测试：状态映射与筛选搜索、心跳文案、迁移资格（保留规则 5）与 409 资产清单、合法状态边与批量取舍、排序提交项、协议表单（由真实 schema 推字段、拍平 / 还原、敏感字段、REALITY、422 键映射）、PATCH 差量不回写协议、路由规则互转与兜底校验、带宽分桶；界面交互在浏览器里对 dev/mock/admin/nodes.ts 验收
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it } from 'vitest'
import { NODE_PROTOCOL_SCHEMAS } from '../../../../dev/mock/admin/node-schemas'
import {
  bandwidthBuckets,
  basicFromRow,
  batchPlan,
  blankSensitive,
  canMove,
  canTransition,
  createBody,
  filterNodes,
  heartbeatLabel,
  isStable,
  mapProtocolErrors,
  moveBlockers,
  moveItem,
  nodeState,
  orderItems,
  patchBody,
  protocolChanged,
  protocolFields,
  realityEnabled,
  routeToRow,
  rowsToRoutes,
  toFormValues,
  toProtocolConfig,
  validateBasic,
} from './logic'
import { nodeRowSchema, protocolSchemasResponse, type NodeRow } from './schemas'

const SCHEMAS = protocolSchemasResponse.parse(NODE_PROTOCOL_SCHEMAS).schemas
const schemaOf = (t: string) => SCHEMAS.find((s) => s.node_type === t)!

// Go 的列表行原样形状：指针字段为 null，nil 切片为 null
const row = (over: Partial<Record<string, unknown>> = {}): NodeRow =>
  nodeRowSchema.parse({
    id: 'n1',
    node_no: 101,
    row_version: 5,
    name: '香港 01',
    status: 'active',
    serving_status: 'active',
    server_id: 's1',
    server_name: 'hk-hkg-edge-1',
    pool_id: null,
    pool_name: null,
    agent_version: null,
    hostname: null,
    public_ipv4: null,
    cpu_cores: null,
    memory_mb: null,
    disk_gb: null,
    health_score: null,
    applied_config_version: null,
    desired_config_version: null,
    last_heartbeat_at: '2026-09-24T10:00:00Z',
    stale: false,
    delivered_to_users: true,
    delivery_note: '',
    identity_serial: 3,
    created_at: '2026-09-01T00:00:00Z',
    node_type: 'vless',
    server_host: 'hk1.pandora.run',
    server_port: 443,
    traffic_rate: 1,
    display_name: null,
    country_code: 'HK',
    kernel: 'auto',
    protocol_config: { network: 'tcp', tls: 2, reality_settings: { dest: 'www.microsoft.com:443', public_key: 'PUB' } },
    protocol_schema_version: 1,
    config_validated_at: null,
    sort_order: 10,
    online_users: 12,
    online_ips: 15,
    traffic_bytes_24h: 0,
    cpu_percent: null,
    mem_percent: null,
    metrics_at: null,
    traffic_bytes: 0,
    granted_plans: null,
    ...over,
  })

describe('schemas', () => {
  it('accepts the real schema dump with null arrays and legacy entries', () => {
    expect(SCHEMAS).toHaveLength(15)
    expect(SCHEMAS.filter(isStable)).toHaveLength(13)
    expect(schemaOf('socks').required).toEqual([])
    expect(schemaOf('v2ray').allowed_properties).toEqual([])
    expect(row().granted_plans).toEqual([])
  })
})

describe('state', () => {
  it('maps serving status and staleness onto the design states', () => {
    expect(nodeState({ serving_status: 'active', stale: false })).toMatchObject({ label: '在线', filter: 'online' })
    expect(nodeState({ serving_status: 'active', stale: true })).toMatchObject({ label: '离线', filter: 'offline' })
    expect(nodeState({ serving_status: 'draining', stale: false })).toMatchObject({ label: '在线', note: '排空中' })
    expect(nodeState({ serving_status: 'draft', stale: true })).toMatchObject({ label: '已停用', note: '草稿' })
    expect(nodeState({ serving_status: 'retired', stale: true })).toMatchObject({ label: '已退役', filter: 'retired' })
  })

  it('filters by state and searches name, server, country, protocol, address and number', () => {
    const rows = [row(), row({ id: 'n2', node_no: 102, name: '东京 03', serving_status: 'disabled', country_code: 'JP', node_type: 'trojan', server_name: 'jp-tyo' })]
    expect(filterNodes(rows, 'disabled', '').map((n) => n.id)).toEqual(['n2'])
    expect(filterNodes(rows, 'all', 'jp').map((n) => n.id)).toEqual(['n2'])
    expect(filterNodes(rows, 'all', 'VLESS').map((n) => n.id)).toEqual(['n1'])
    expect(filterNodes(rows, 'all', '102').map((n) => n.id)).toEqual(['n2'])
    expect(filterNodes(rows, 'online', 'hk1.pandora').map((n) => n.id)).toEqual(['n1'])
    const withRetired = [...rows, row({ id: 'n3', serving_status: 'retired' })]
    expect(filterNodes(withRetired, 'all', '').map((n) => n.id)).toEqual(['n1', 'n2'])
    expect(filterNodes(withRetired, 'retired', '').map((n) => n.id)).toEqual(['n3'])
  })

  it('labels heartbeats', () => {
    const now = new Date('2026-09-24T10:05:00Z')
    expect(heartbeatLabel(null)).toBe('从未')
    expect(heartbeatLabel('2026-09-24T10:04:30Z', now)).toBe('刚刚')
    expect(heartbeatLabel('2026-09-24T10:00:00Z', now)).toBe('5 分钟前')
  })
})

describe('reserved rule 5: move', () => {
  it('only allows never-deployed drafts', () => {
    expect(canMove({ serving_status: 'draft', last_heartbeat_at: null, identity_serial: null })).toBe(true)
    expect(canMove({ serving_status: 'disabled', last_heartbeat_at: null, identity_serial: null })).toBe(true)
    expect(canMove({ serving_status: 'draft', last_heartbeat_at: '2026-09-24T10:00:00Z', identity_serial: null })).toBe(false)
    expect(canMove({ serving_status: 'draft', last_heartbeat_at: null, identity_serial: 1 })).toBe(false)
    expect(canMove({ serving_status: 'active', last_heartbeat_at: null, identity_serial: null })).toBe(false)
  })

  it('lists the non-zero blockers from the 409', () => {
    expect(moveBlockers({ control_node: '0', active_identities: '1', traffic_reports: '1440', row_version: 'x' })).toEqual(['有效身份 1', '流量上报 1440'])
  })
})

describe('status transitions', () => {
  it('follows the contract edges', () => {
    expect(canTransition('draft', 'active')).toBe(true)
    expect(canTransition('active', 'draft')).toBe(false)
    expect(canTransition('retired', 'active')).toBe(false)
    expect(canTransition('disabled', 'active')).toBe(true)
  })

  it('batches only the nodes that can move and counts the rest', () => {
    const rows = [row({ id: 'a', serving_status: 'disabled' }), row({ id: 'b', serving_status: 'active' }), row({ id: 'c', serving_status: 'retired' })]
    expect(batchPlan(rows, 'active')).toEqual({ items: [{ id: 'a', row_version: 5 }], skipped: 2 })
    expect(batchPlan(rows, 'disabled')).toEqual({ items: [{ id: 'b', row_version: 5 }], skipped: 2 })
  })
})

describe('ordering', () => {
  it('swaps neighbours and submits only changed sort orders in steps of 10', () => {
    const list = [row({ id: 'a', sort_order: 10 }), row({ id: 'b', sort_order: 20 }), row({ id: 'c', sort_order: 30 })]
    const moved = moveItem(list, 2, -1)
    expect(moved.map((n) => n.id)).toEqual(['a', 'c', 'b'])
    expect(orderItems(moved)).toEqual([
      { id: 'c', row_version: 5, sort_order: 20 },
      { id: 'b', row_version: 5, sort_order: 30 },
    ])
    expect(moveItem(list, 0, -1).map((n) => n.id)).toEqual(['a', 'b', 'c'])
  })
})

describe('protocol form', () => {
  it('derives fields from the real schema: enums, numbers, sensitive leaves, shadowsocks methods', () => {
    const vless = protocolFields(schemaOf('vless'))
    const tls = vless.find((f) => f.path === 'tls')!
    expect(tls).toMatchObject({ kind: 'enum', options: ['0', '2'] })
    expect(vless.find((f) => f.path === 'reality_settings.private_key')!.sensitive).toBe(true)
    expect(vless.find((f) => f.path === 'mtu')!.kind).toBe('number')
    expect(vless.find((f) => f.path === 'headers')!.kind).toBe('json')
    expect(vless.find((f) => f.path === 'network_settings.mode')).toMatchObject({ kind: 'enum', options: ['auto', 'packet-up', 'stream-up', 'stream-one', 'stream-down'] })
    const ss = protocolFields(schemaOf('shadowsocks'))
    expect(ss).toEqual([{ path: 'cipher', kind: 'enum', options: ['aes-128-gcm', 'aes-256-gcm', 'chacha20-ietf-poly1305'], required: true, sensitive: false }])
    // hysteria2 的 obfs.password 按叶子名是敏感的；必填排在前面
    const hy = protocolFields(schemaOf('hysteria2'))
    expect(hy.find((f) => f.path === 'obfs.password')!.sensitive).toBe(true)
    expect(hy.slice(0, 2).map((f) => f.path)).toEqual(['cert_path', 'key_path'])
  })

  it('flattens stored config (never echoing secrets) and rebuilds nested, typed config', () => {
    const s = schemaOf('vless')
    const fields = protocolFields(s)
    const values = toFormValues(fields, { network: 'tcp', tls: 2, reality_settings: { dest: 'a:443', private_key: 'SECRET' } })
    expect(values.tls).toBe('2')
    expect(values['reality_settings.dest']).toBe('a:443')
    expect(values['reality_settings.private_key']).toBe('')
    const { config, errors } = toProtocolConfig(fields, { ...values, 'reality_settings.private_key': 'K', mtu: 'x' }, s.property_types)
    expect(config).toMatchObject({ network: 'tcp', tls: 2, reality_settings: { dest: 'a:443', private_key: 'K' } })
    expect(errors).toEqual({ mtu: '填数字' })
    expect(toProtocolConfig(protocolFields(schemaOf('shadowsocks')), {}).errors).toEqual({ cipher: '必填' })
  })

  it('knows when REALITY applies, what changed and which secrets would be cleared', () => {
    const fields = protocolFields(schemaOf('vless'))
    const initial = toFormValues(fields, { network: 'tcp', tls: 2 })
    expect(realityEnabled('vless', initial)).toBe(true)
    expect(realityEnabled('vmess', { tls: '2' })).toBe(false)
    expect(protocolChanged(fields, initial, initial)).toBe(false)
    expect(protocolChanged(fields, initial, { ...initial, network: 'ws' })).toBe(true)
    expect(protocolChanged(fields, initial, { ...initial, 'reality_settings.private_key': 'K' })).toBe(true)
    expect(blankSensitive(fields, initial)).toEqual(['mask_password', 'reality_settings.private_key'].sort((a, b) => fields.findIndex((f) => f.path === a) - fields.findIndex((f) => f.path === b)))
  })

  it('maps protocol_config.* 422 keys onto form fields by path, then by leaf', () => {
    const fields = protocolFields(schemaOf('vless'))
    const out = mapProtocolErrors(fields, { 'protocol_config.tls': 'x', 'protocol_config.private_key': 'y', name: 'z' })
    expect(out.byField).toEqual({ tls: 'x', 'reality_settings.private_key': 'y' })
    expect(out.rest).toEqual({ name: 'z' })
  })
})

describe('basic info and PATCH', () => {
  it('validates basic fields', () => {
    const e = validateBasic({ name: '', displayName: '', serverId: '', poolId: '', nodeType: '', host: '', port: '70000', kernel: 'auto', rate: '0', country: 'CHN' }, true)
    expect(Object.keys(e).sort()).toEqual(['country_code', 'name', 'node_type', 'server_host', 'server_id', 'server_port', 'traffic_rate'])
  })

  it('creates with only filled optionals, uppercasing the country', () => {
    const body = createBody({ name: 'A', displayName: '', serverId: 's', poolId: '', nodeType: 'shadowsocks', host: 'h', port: '8388', kernel: 'auto', rate: '1.5', country: 'hk' }, { cipher: 'aes-256-gcm' })
    expect(body).toEqual({ name: 'A', server_id: 's', node_type: 'shadowsocks', server_host: 'h', server_port: 8388, protocol_config: { cipher: 'aes-256-gcm' }, kernel: 'auto', traffic_rate: 1.5, country_code: 'HK' })
  })

  it('sends only changed fields and leaves protocol_config out unless the protocol changed', () => {
    const n = row()
    const b = { ...basicFromRow(n), name: '香港 01 · 新', country: '', poolId: 'p1' }
    expect(patchBody(n, b, { changed: false, config: { x: 1 } })).toEqual({ row_version: 5, name: '香港 01 · 新', pool_id: 'p1', country_code: null })
    expect(patchBody(n, basicFromRow(n), { changed: true, config: { network: 'ws' } })).toEqual({ row_version: 5, protocol_config: { network: 'ws' } })
    expect(patchBody(n, { ...basicFromRow(n), poolId: '' }, { changed: false, config: {} })).toEqual({ row_version: 5 })
  })
})

describe('routing rows', () => {
  it('reads every matcher alias and the empty fallback', () => {
    expect(routeToRow({ priority: 10, matcher: { domain_suffixes: ['a.com', 'b.com'] }, outbound_tag: 'US', enabled: true, note: '' })).toMatchObject({ kind: 'domain_suffix', value: 'a.com, b.com' })
    expect(routeToRow({ priority: 20, matcher: { ports: [25, 465] }, outbound_tag: 'block', enabled: true, note: '' })).toMatchObject({ kind: 'port', value: '25, 465' })
    expect(routeToRow({ priority: 30, matcher: {}, outbound_tag: 'direct', enabled: true, note: '' }).kind).toBe('fallback')
  })

  it('builds matchers with numeric ports and enforces the fallback-last rule', () => {
    const { routes, errors } = rowsToRoutes([
      { kind: 'port', value: '25, 465 8000-9000', outbound: 'block', enabled: true, note: '' },
      { kind: 'fallback', value: '', outbound: 'direct', enabled: true, note: '' },
    ])
    expect(errors).toEqual({})
    expect(routes[0]).toEqual({ priority: 10, matcher: { port: [25, 465, '8000-9000'] }, outbound_tag: 'block', enabled: true, note: '' })
    expect(routes[1]!.matcher).toEqual({})
    const bad = rowsToRoutes([
      { kind: 'fallback', value: '', outbound: 'direct', enabled: true, note: '' },
      { kind: 'domain', value: '', outbound: '', enabled: true, note: '' },
    ])
    expect(bad.errors).toEqual({ 0: '兜底规则必须是最后一条启用的规则', 1: '填匹配值' })
  })
})

describe('bandwidth', () => {
  it('averages (rx+tx)*8/1e6 per hour over the last 24 hours', () => {
    const now = new Date('2026-09-24T10:30:00')
    const at = (h: number, m: number) => new Date(2026, 8, 24, h, m).toISOString()
    const p = (iso: string, rx: number, tx: number) => ({ at: iso, cpu_percent: 0, mem_percent: 0, load1: 0, rx_speed: rx, tx_speed: tx, tcp_conns: 0 })
    const b = bandwidthBuckets([p(at(10, 5), 1e6, 0), p(at(10, 20), 3e6, 0), p(at(9, 0), 125000, 0), p(at(8, 0), 0, 0)], now)
    expect(b).toHaveLength(24)
    expect(b[23]).toEqual({ hour: 10, mbps: 16 })
    expect(b[22]).toEqual({ hour: 9, mbps: 1 })
    expect(b[0]!.mbps).toBeNull()
  })
})
