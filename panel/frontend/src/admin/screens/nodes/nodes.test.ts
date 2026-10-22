import { describe, expect, it } from 'vitest'
import { NODE_PROTOCOL_SCHEMAS } from '../../../../dev/mock/admin/node-schemas'
import {
  activateBlock,
  activationHint,
  bandwidthAxis,
  bandwidthBuckets,
  bandwidthPeak,
  bandwidthPeakLabel,
  basicFromRow,
  batchPlan,
  bindBlock,
  bindBody,
  bucketRangeLabel,
  canTransition,
  certAttention,
  serverAcceptsNodes,
  createBody,
  emptyBasic,
  filterNodes,
  heartbeatLabel,
  isStable,
  mapProtocolErrors,
  moveItem,
  nodeCounts,
  nodeState,
  onlineUsersView,
  orderItems,
  parseNodeFilter,
  patchBody,
  protocolChanged,
  protocolFields,
  protocolNotices,
  protocolSummary,
  enumOptions,
  fieldVisible,
  protocolDefaults,
  type ProtocolField,
  BASIC_STEP_KEYS,
  realityEnabled,
  realityKeypairToast,
  withRealityKeypair,
  VISION_FLOW,
  withProtocolDefaults,
  clearableSecret,
  insertRule,
  MATCH_KINDS,
  renameOutbound,
  routeToRow,
  rowsToOutbounds,
  rowsToRoutes,
  rulesUsing,
  toFormValues,
  toProtocolConfig,
  validateBasic,
  validateBind,
  freePort,
  portHint,
  serverPickOptions,
  usedPorts,
  NO_POOL_HINT,
  NODE_LINE_FIELDS,
  sameNodeLine,
  tagOptions,
  batchEditBody,
} from './logic'
import { nodeDetailSchema, nodeRowSchema, protocolSchemasResponse, type NodeDetail, type ProtocolSchema } from './schemas'

const miniSchema = (s: Partial<ProtocolSchema> & Pick<ProtocolSchema, 'node_type'>): ProtocolSchema => ({
  version: s.version ?? 1,
  status: s.status ?? 'stable',
  required: s.required ?? [],
  allowed_properties: s.allowed_properties ?? [],
  ...s,
})

const SCHEMAS = protocolSchemasResponse.parse(NODE_PROTOCOL_SCHEMAS).schemas
const schemaOf = (t: string) => SCHEMAS.find((s) => s.node_type === t)!

// Go 的单取行（?id=）原样形状：指针字段为 null，nil 切片为 null；列表行是它去掉编辑字段
const row = (over: Partial<Record<string, unknown>> = {}): NodeDetail =>
  nodeDetailSchema.parse({
    id: 'n1',
    node_no: 101,
    row_version: 5,
    name: '香港 01',
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
    last_heartbeat_at: '2026-09-24T10:00:00Z',
    stale: false,
    delivered_to_users: true,
    delivery_note: '',
    identity_serial: 3,
    movable: false,
    created_at: '2026-09-01T00:00:00Z',
    node_type: 'vless',
    server_host: 'hk1.example.test',
    server_port: 443,
    traffic_rate: 1,
    display_name: null,
    country_code: 'HK',
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
    granted_plans: null,
    tags: null,
    traffic: { limit_bytes: null, reset_day: null, used_bytes: 0, reset_at: '2026-10-01T00:00:00Z', exceeded_at: null },
    traffic_rate_schedule: null,
    relay_count: 0,
    ech_enabled: false,
    ...over,
  })

describe('schemas', () => {
  it('accepts the real schema dump of stable protocols only', () => {
    expect(SCHEMAS).toHaveLength(13)
    expect(SCHEMAS.filter(isStable)).toHaveLength(13)
    expect(SCHEMAS.some((s) => s.status === 'legacy-read-compatible' || s.node_type === 'v2ray' || s.node_type === 'hysteria')).toBe(false)
    // Go 的 nil required 归一成 []（mieru 没有必填项）
    expect(schemaOf('mieru').required).toEqual([])
    expect(row().granted_plans).toEqual([])
    // 标签与时段倍率：Go 的 nil 切片按 null 来也归一成 []
    expect(row().tags).toEqual([])
    expect(row().traffic_rate_schedule).toEqual([])
  })

  it('keeps the edit-form fields out of the list row shape', () => {
    const listed = nodeRowSchema.parse(row())
    for (const k of ['protocol_config', 'traffic_rate', 'protocol_schema_version', 'config_validated_at']) expect(listed).not.toHaveProperty(k)
    expect(row().protocol_config).toMatchObject({ network: 'tcp' })
  })
})

describe('node line memo', () => {
  it('ignores fields the line does not show and catches the ones it does', () => {
    const a = row()
    const beat: NodeDetail = { ...a, last_heartbeat_at: '2026-09-24T10:00:30Z', agent_version: 'core r54', row_version: 6 }
    expect(sameNodeLine(a, beat)).toBe(true)
    for (const k of NODE_LINE_FIELDS) {
      const changed = { ...a, [k]: k === 'stale' || k === 'delivered_to_users' ? !a[k] : `${String(a[k])}-x` }
      expect(sameNodeLine(a, changed)).toBe(false)
    }
  })
})

describe('state', () => {
  it('maps serving status and staleness onto the design states', () => {
    expect(nodeState({ serving_status: 'active', stale: false })).toMatchObject({ label: '在线', filter: 'online' })
    expect(nodeState({ serving_status: 'active', stale: true })).toMatchObject({ label: '离线', filter: 'offline' })
    expect(nodeState({ serving_status: 'draft', stale: true })).toMatchObject({ label: '已停用', note: '草稿' })
    expect(nodeState({ serving_status: 'retired', stale: true })).toMatchObject({ label: '已退役', filter: 'retired' })
  })

  it('filters by state and searches name, server, country, protocol, address and number', () => {
    const rows = [row(), row({ id: 'n2', node_no: 102, name: '东京 03', serving_status: 'disabled', country_code: 'JP', node_type: 'trojan', server_name: 'jp-tyo' })]
    expect(filterNodes(rows, 'disabled', '').map((n) => n.id)).toEqual(['n2'])
    expect(filterNodes(rows, 'all', 'jp').map((n) => n.id)).toEqual(['n2'])
    expect(filterNodes(rows, 'all', 'VLESS').map((n) => n.id)).toEqual(['n1'])
    expect(filterNodes(rows, 'all', '102').map((n) => n.id)).toEqual(['n2'])
    expect(filterNodes(rows, 'online', 'hk1.example').map((n) => n.id)).toEqual(['n1'])
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

describe('status transitions', () => {
  it('follows the contract edges', () => {
    expect(canTransition('draft', 'active')).toBe(true)
    expect(canTransition('draft', 'disabled')).toBe(false)
    expect(canTransition('draft', 'retired')).toBe(true)
    expect(canTransition('active', 'disabled')).toBe(true)
    expect(canTransition('active', 'draft')).toBe(false)
    expect(canTransition('disabled', 'draft')).toBe(false)
    expect(canTransition('retired', 'active')).toBe(false)
    expect(canTransition('disabled', 'active')).toBe(true)
  })

  it('batches only the nodes that can move and counts the rest', () => {
    const rows = [row({ id: 'a', serving_status: 'disabled' }), row({ id: 'b', serving_status: 'active' }), row({ id: 'c', serving_status: 'retired' })]
    expect(batchPlan(rows, 'active')).toEqual({ items: [{ id: 'a', row_version: 5 }], skipped: 2 })
    expect(batchPlan(rows, 'disabled')).toEqual({ items: [{ id: 'b', row_version: 5 }], skipped: 2 })
  })

  it('batch enable skips disabled nodes that have no server (backend would 409 the whole batch)', () => {
    const rows = [row({ id: 'a', serving_status: 'disabled' }), row({ id: 'u', serving_status: 'disabled', server_id: null })]
    expect(batchPlan(rows, 'active')).toEqual({ items: [{ id: 'a', row_version: 5 }], skipped: 1 })
    expect(batchPlan([row({ id: 'u', serving_status: 'active', server_id: null })], 'disabled').items).toHaveLength(1)
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
  const meta = miniSchema({
    node_type: 'meta',
    required: ['flow'],
    allowed_properties: ['network', 'flow', 'mtu'],
    enums: { network: ['tcp', 'ws'], flow: ['a', 'b'] },
    fields: [
      { path: 'network', label: '传输方式', default: 'tcp' },
      { path: 'flow', label: '流控', show_if: { network: ['tcp', ''] } },
      { path: 'mtu', label: 'MTU', advanced: true, show_if: { network: ['mkcp'] } },
    ],
  })

  it('orders protocolFields from fields metadata and uses labels', () => {
    const fields = protocolFields(meta)
    expect(fields.map((f) => f.path)).toEqual(['network', 'flow', 'mtu'])
    expect(fields.find((f) => f.path === 'flow')!.label).toBe('流控')
  })

  it('falls back to path labels and required-first without fields metadata', () => {
    // 去掉 fields 元数据，测退回路径（mock 的协议定义现在带 fields）
    const ss = protocolFields({ ...schemaOf('shadowsocks'), fields: undefined })
    expect(ss[0]!.path).toBe('cipher')
    expect(ss[0]!.label).toBe('cipher')
  })

  it('fieldVisible requires every show_if key to match form strings', () => {
    const fields = protocolFields(meta)
    const flow = fields.find((f) => f.path === 'flow')!
    expect(fieldVisible(flow, { network: 'tcp' })).toBe(true)
    expect(fieldVisible(flow, { network: '' })).toBe(true)
    expect(fieldVisible(flow, { network: 'ws' })).toBe(false)
  })

  it('protocolDefaults stringifies numbers and booleans', () => {
    const s = miniSchema({
      node_type: 'defs',
      allowed_properties: ['flag', 'port'],
      property_types: { port: 'number' },
      fields: [{ path: 'flag', label: '开关', default: false }, { path: 'port', label: '端口', default: 443 }],
    })
    expect(protocolDefaults(protocolFields(s))).toEqual({ flag: 'false', port: '443' })
  })

  it('enumOptions adds optional empty choice with option_labels', () => {
    const f = protocolFields(
      miniSchema({
        node_type: 'en',
        allowed_properties: ['tls'],
        enums: { tls: ['0', '2'] },
        fields: [{ path: 'tls', label: '加密', option_labels: { '': '留空', '2': 'REALITY' } }],
      }),
    )[0]!
    expect(enumOptions({ ...f, required: false })[0]).toEqual({ value: '', label: '留空' })
    expect(enumOptions({ ...f, required: true }).some((o) => o.value === '')).toBe(false)
  })

  it('toProtocolConfig skips hidden fields and writes boolean false', () => {
    const fields = protocolFields(meta)
    const { config, errors } = toProtocolConfig(fields, { network: 'ws', flow: 'a' })
    expect(config).toEqual({ network: 'ws' })
    expect(errors).toEqual({})
    const bool = protocolFields(
      miniSchema({ node_type: 'b', allowed_properties: ['x'], property_types: { x: 'boolean' }, fields: [{ path: 'x', label: 'X' }] }),
    )
    expect(toProtocolConfig(bool, { x: 'false' }).config).toEqual({ x: false })
  })

  it('protocolChanged ignores edits to hidden fields', () => {
    const fields = protocolFields(meta)
    const initial = { network: 'ws', flow: '' }
    expect(protocolChanged(fields, initial, { network: 'ws', flow: 'a' })).toBe(false)
  })

  it('derives sensitive leaves and list/json kinds from real schema', () => {
    const vless = protocolFields(schemaOf('vless'))
    expect(vless.find((f) => f.path === 'reality_settings.private_key')!.sensitive).toBe(true)
    expect(vless.find((f) => f.path === 'headers')!.kind).toBe('json')
  })

  it('flattens stored config and rebuilds nested typed config', () => {
    const s = schemaOf('vless')
    const fields = protocolFields(s)
    const values = toFormValues(fields, { network: 'tcp', tls: 2, reality_settings: { dest: 'a:443', private_key: 'SECRET' } })
    expect(values.tls).toBe('2')
    expect(values['reality_settings.private_key']).toBe('')
    const { config, errors } = toProtocolConfig(fields, { ...values, 'reality_settings.private_key': 'K', mtu: 'x' }, s.property_types)
    expect(config).toMatchObject({ network: 'tcp', tls: 2, reality_settings: { dest: 'a:443', private_key: 'K' } })
    // mtu 只在 network=mkcp 时显示：隐藏时不校验、不写入
    expect(errors).toEqual({})
    expect(config).not.toHaveProperty('mtu')
    const mkcp = toProtocolConfig(fields, { ...values, network: 'mkcp', 'reality_settings.private_key': 'K', mtu: 'x' }, s.property_types)
    expect(mkcp.errors.mtu).toBe('填数字')
  })

  it('R106: blank secrets when editing same protocol; explicit null when cleared', () => {
    const s = schemaOf('vless')
    const fields = protocolFields(s)
    const values = toFormValues(fields, { network: 'tcp', tls: 2, reality_settings: { dest: 'a:443' } })
    const kept = toProtocolConfig(fields, values, s.property_types, { keepSecrets: true })
    expect(JSON.stringify(kept.config)).not.toContain('private_key')
    const optional = fields.find((f) => clearableSecret(f))!
    const cleared = new Set([optional.path])
    expect(protocolChanged(fields, values, values, cleared)).toBe(true)
  })

  it('REALITY applies to vless/trojan with tls=2', () => {
    const fields = protocolFields(schemaOf('vless'))
    const initial = toFormValues(fields, { network: 'tcp', tls: 2 })
    expect(realityEnabled('vless', initial)).toBe(true)
    expect(realityEnabled('trojan', { tls: '2' })).toBe(true)
    expect(realityEnabled('vmess', { tls: '2' })).toBe(false)
  })

  it('withProtocolDefaults and protocolNotices stay aligned with backend rules', () => {
    expect(withProtocolDefaults('vless', 'tls', { tls: '2' }).flow).toBe(VISION_FLOW)
    expect(protocolNotices('vless', { tls: '0', network: 'ws' })[0]).toContain('需要套 CDN 或 TLS')
  })

  it('toFormValues defaults missing cert_mode to file for legacy configs', () => {
    const fields: ProtocolField[] = [
      {
        path: 'cert_mode',
        kind: 'enum',
        options: ['acme', 'file'],
        required: true,
        sensitive: false,
        label: '证书模式',
        advanced: false,
        showIf: null,
        defaultValue: 'acme',
        optionLabels: {},
      },
    ]
    expect(toFormValues(fields, { cert_path: '/etc/pandora-native/certs/a/fullchain.pem' }).cert_mode).toBe('file')
    expect(toFormValues(fields, { cert_mode: 'acme' }).cert_mode).toBe('acme')
  })

  it('maps protocol_config.* 422 keys onto form fields', () => {
    const fields = protocolFields(schemaOf('vless'))
    const out = mapProtocolErrors(fields, { 'protocol_config.tls': 'x', 'protocol_config.private_key': 'y', name: 'z' })
    expect(out.byField).toEqual({ tls: 'x', 'reality_settings.private_key': 'y' })
    expect(out.rest).toEqual({ name: 'z' })
  })
})

describe('basic info and PATCH', () => {
  const filled = {
    name: 'A',
    displayName: '',
    serverId: 's',
    poolId: 'p1',
    nodeType: 'shadowsocks',
    host: 'h',
    port: '443',
    rate: '1',
    country: 'HK',
  }

  it('validateBasic: creating requires country; editing does not', () => {
    expect(validateBasic({ ...filled, country: '' }, true).country_code).toBe('请选择地区')
    expect(validateBasic({ ...filled, country: '' }, false).country_code).toBeUndefined()
    expect(validateBasic(filled, true)).toEqual({})
  })

  it('uses new error copy for name and port', () => {
    const e = validateBasic({ ...filled, name: '', port: '70000' }, true)
    expect(e.name).toBe('必填')
    expect(e.server_host).toBeUndefined()
    expect(e.server_port).toBe('1–65535')
  })

  it('requires a pool on create only', () => {
    expect(validateBasic({ ...filled, poolId: '' }, true)).toEqual({ pool_id: NO_POOL_HINT })
    expect(validateBasic({ ...filled, poolId: '' }, false).pool_id).toBeUndefined()
  })

  it('emptyBasic defaults port to 443', () => {
    expect(emptyBasic().port).toBe('443')
  })

  it('creates with only filled optionals, uppercasing the country', () => {
    const body = createBody({ ...filled, poolId: 'p1', port: '8388', rate: '1.5', country: 'hk' }, { cipher: 'aes-256-gcm' })
    expect(body).toMatchObject({ name: 'A', server_id: 's', server_port: 8388, country_code: 'HK', pool_id: 'p1' })
  })

  it('validateBasic: unbound create allows empty server, port and host', () => {
    const base = { ...filled, serverId: '', port: '', host: '' }
    const e = validateBasic(base, true)
    expect(e.server_id).toBeUndefined()
    expect(e.server_port).toBeUndefined()
    expect(e.server_host).toBeUndefined()
    expect(validateBasic({ ...base, serverId: 's1', port: '' }, true).server_port).toBe('选了服务器就要填端口（1–65535）')
    expect(validateBasic({ ...filled, host: '' }, false, true).server_host).toBe('必填：在役节点要有对外地址')
  })

  it('createBody omits server_id and server_port when empty', () => {
    const body = createBody({ ...filled, serverId: '', port: '', host: '' }, {})
    expect(body).not.toHaveProperty('server_id')
    expect(body).not.toHaveProperty('server_port')
  })

  it('patchBody omits server_port when port cleared', () => {
    const n = row()
    const b = { ...basicFromRow(n), port: '' }
    const body = patchBody(n, b, { changed: false, config: {} })
    expect(body).not.toHaveProperty('server_port')
  })

  it('activateBlock and bindBlock', () => {
    expect(activateBlock({ server_id: null })).toBe('先在「身份与操作」里绑定服务器再上线')
    expect(activateBlock({ server_id: 's1' })).toBeNull()
    expect(bindBlock({ serving_status: 'draft', movable: true })).toBeNull()
    expect(bindBlock({ serving_status: 'disabled', movable: true })).toBeNull()
    expect(bindBlock({ serving_status: 'active', movable: false })).toBe('节点在役，先停用再改绑 / 解绑')
    expect(bindBlock({ serving_status: 'retired', movable: false })).toBe('节点已退役')
  })

  it('serverAcceptsNodes 与后端 lockServerForNodes 一致：只拒已退役', () => {
    for (const status of ['draft', 'ready', 'draining', 'maintenance'] as const) expect(serverAcceptsNodes({ status })).toBe(true)
    expect(serverAcceptsNodes({ status: 'retired' })).toBe(false)
  })

  it('validateBind and bindBody', () => {
    expect(validateBind('', '443')).toEqual({ server_id: '选择要绑定的服务器' })
    expect(validateBind('s1', 'x').server_port).toBe('1–65535')
    expect(bindBody({ row_version: 3 }, 's1', '443')).toEqual({ row_version: 3, server_id: 's1', server_port: 443 })
  })

  it('freePort skips ports taken on the same server', () => {
    const n = (id: string, server_id: string | null, server_port: number | null) => ({ id, server_id, server_port })
    expect(freePort('s1', [])).toBe(443)
    expect(freePort('s1', [n('a', 's1', 443), n('b', 's2', 8443)])).toBe(8443)
    expect(freePort('s1', [n('a', 's1', 443)], 'a')).toBe(443)
    expect(freePort('s1', [443, 8443, 2053, 2083, 2087, 2096, 10443, 20000].map((p, i) => n(String(i), 's1', p)))).toBe(20001)
  })

  it('usedPorts and portHint list ports taken on the chosen server', () => {
    const n = (id: string, server_id: string | null, server_port: number | null) => ({ id, server_id, server_port })
    const nodes = [n('a', 's1', 8389), n('b', 's1', 8388), n('c', 's1', 8388), n('d', 's2', 443), n('e', 's1', null)]
    expect(usedPorts('s1', nodes)).toEqual([8388, 8389])
    expect(usedPorts('s1', nodes, 'a')).toEqual([8388])
    expect(portHint([])).toBe('同一台服务器上端口先到先得；这台还没有别的节点占端口')
    expect(portHint([8388, 8389])).toBe('同一台服务器上端口先到先得；这台已用 8388、8389，缺省已避开')
    expect(portHint([1, 2, 3, 4, 5, 6, 7])).toBe('同一台服务器上端口先到先得；这台已用 1、2、3、4、5、6 等 7 个，缺省已避开')
  })

  it('serverPickOptions sorts by region and puts the region first', () => {
    const s = (id: string, name: string, region: string | null, extra: Partial<{ enrolled: boolean; status: 'ready' | 'draft' | 'retired' }> = {}) => ({
      id,
      name,
      region,
      node_count: 2,
      capacity_nodes: 16,
      status: extra.status ?? ('ready' as const),
      enrolled: extra.enrolled ?? true,
    })
    const opts = serverPickOptions([s('1', 'jp-tyo-edge-2', '东京'), s('2', 'jp-osa-edge-1', '大阪'), s('3', 'zz-new', null, { enrolled: false, status: 'draft' }), s('4', 'old', '香港', { status: 'retired' })])
    expect(opts.map((o) => o.value)).toEqual(['2', '1', '4', '3'])
    expect(opts[0]).toEqual({ value: '2', label: '大阪 · jp-osa-edge-1（2/16 个节点）', disabled: false })
    expect(opts[2]!.disabled).toBe(true)
    expect(opts[3]!.label).toBe('zz-new（2/16 个节点）（未接入）')
  })

  it('sends only changed fields and leaves protocol_config out unless the protocol changed', () => {
    const n = row()
    const b = { ...basicFromRow(n), name: '香港 01 · 新', country: '', poolId: 'p1' }
    expect(patchBody(n, b, { changed: false, config: { x: 1 } })).toEqual({ row_version: 5, name: '香港 01 · 新', pool_id: 'p1', country_code: null })
    expect(patchBody(n, basicFromRow(n), { changed: true, config: { network: 'ws' } })).toEqual({ row_version: 5, protocol_config: { network: 'ws' } })
  })

  it('lists basic wizard step keys', () => {
    expect(BASIC_STEP_KEYS).toContain('country_code')
    expect(BASIC_STEP_KEYS).toContain('server_port')
  })
})

describe('routing rows', () => {
  it('reads the editable aliases and the empty fallback', () => {
    expect(routeToRow({ priority: 10, matcher: { domain_suffixes: ['example.com', 'example.org'] }, outbound_tag: 'US', enabled: true, note: '' })).toMatchObject({ kind: 'domain_suffix', value: 'example.com, example.org' })
    expect(routeToRow({ priority: 15, matcher: { ip: ['10.0.0.0/8'] }, outbound_tag: 'direct', enabled: true, note: '' })).toMatchObject({ kind: 'ip_cidr', value: '10.0.0.0/8' })
    expect(routeToRow({ priority: 30, matcher: {}, outbound_tag: 'direct', enabled: true, note: '' }).kind).toBe('fallback')
  })

  it('keeps legacy matchers read-only and saves them unchanged with enabled and note', () => {
    const port = routeToRow({ priority: 20, matcher: { ports: [25, 465] }, outbound_tag: 'block', enabled: false, note: '禁止发信' })
    expect(port.legacy).toMatchObject({ label: '端口', value: '25, 465' })
    const multi = routeToRow({ priority: 25, matcher: { domain_suffix: ['example.com'], network: 'udp' }, outbound_tag: 'direct', enabled: true, note: '' })
    expect(multi.legacy?.label).toBe('域名后缀 + 网络')
    const { routes, errors } = rowsToRoutes([port, multi, { kind: 'fallback', value: '', outbound: 'direct', enabled: true, note: '' }])
    expect(errors).toEqual({})
    expect(routes[0]).toEqual({ priority: 10, matcher: { ports: [25, 465] }, outbound_tag: 'block', enabled: false, note: '禁止发信' })
    expect(routes[1]!.matcher).toEqual({ domain_suffix: ['example.com'], network: 'udp' })
    expect(MATCH_KINDS.map(([k]) => k)).toEqual(['domain_suffix', 'ip_cidr', 'fallback'])
  })

  it('builds matchers and enforces the fallback-last rule', () => {
    const { routes, errors } = rowsToRoutes([
      { kind: 'ip_cidr', value: '10.0.0.0/8, 192.168.0.0/16', outbound: 'block', enabled: true, note: ' 备注 ' },
      { kind: 'fallback', value: '', outbound: 'direct', enabled: true, note: '' },
    ])
    expect(errors).toEqual({})
    expect(routes[0]).toEqual({ priority: 10, matcher: { ip_cidr: ['10.0.0.0/8', '192.168.0.0/16'] }, outbound_tag: 'block', enabled: true, note: '备注' })
    expect(routes[1]!.matcher).toEqual({})
    const bad = rowsToRoutes([
      { kind: 'fallback', value: '', outbound: 'direct', enabled: true, note: '' },
      { kind: 'domain_suffix', value: '', outbound: '', enabled: true, note: '' },
    ])
    expect(bad.errors).toEqual({ 0: '兜底规则必须是最后一条启用的规则', 1: '填匹配值' })
  })

  it('inserts new rules before a trailing fallback', () => {
    const add = { kind: 'domain_suffix' as const, value: 'example.net', outbound: 'direct', enabled: true, note: '' }
    const fb = { kind: 'fallback' as const, value: '', outbound: 'US', enabled: true, note: '' }
    expect(insertRule([fb], add)).toEqual([add, fb])
    expect(insertRule([add], fb)).toEqual([add, fb])
    expect(insertRule([], add)).toEqual([add])
  })

  it('counts and renames rules that point at an outbound', () => {
    const rows = [
      { kind: 'domain_suffix' as const, value: 'a', outbound: 'US-LAX-01', enabled: true, note: '' },
      { kind: 'fallback' as const, value: '', outbound: 'direct', enabled: true, note: '' },
    ]
    // 按 tag 原样精确匹配（与后端 checkRouteRefs、下发和 pdnd 一致）
    expect(rulesUsing(rows, 'US-LAX-01')).toBe(1)
    expect(rulesUsing(rows, 'us-lax-01')).toBe(0)
    expect(rulesUsing(rows, 'HK')).toBe(0)
    expect(renameOutbound(rows, 'US-LAX-01', ' US-SJC ').map((r) => r.outbound)).toEqual(['US-SJC', 'direct'])
    expect(renameOutbound(rows, '', 'x')).toEqual(rows)
  })

  it('validates outbound rows like validateRoutingPayload', () => {
    const ok = rowsToOutbounds([{ tag: ' US-LAX-01 ', type: 'vless', settings: '{"server":"a"}' }, { tag: 'hk', type: 'trojan', settings: '' }])
    expect(ok.errors).toEqual({})
    expect(ok.outbounds).toEqual([
      { tag: 'US-LAX-01', type: 'vless', settings: { server: 'a' } },
      { tag: 'hk', type: 'trojan', settings: {} },
    ])
    const bad = rowsToOutbounds([
      { tag: 'Direct', type: 'vless', settings: '{}' },
      { tag: 'hk', type: 'vless', settings: '{' },
      { tag: 'HK', type: 'vless', settings: '{}' },
      { tag: '', type: 'vless', settings: '[]' },
      { tag: 'x'.repeat(65), type: 'vless', settings: '{}' },
      { tag: 'arr', type: 'vless', settings: '[1]' },
    ])
    expect(bad.errors).toEqual({ 0: '不能叫 direct 或 block', 1: 'settings 不是合法的 JSON', 2: '标签重复', 3: '标签 1–64 字', 4: '标签 1–64 字', 5: 'settings 必须是 JSON 对象' })
  })
})

describe('bandwidth', () => {
  it('averages (rx+tx)*8/1e6 per hour over the last 24 hours returning start timestamps', () => {
    const now = new Date(2026, 8, 24, 10, 30)
    const at = (h: number, m: number) => new Date(2026, 8, 24, h, m).toISOString()
    const p = (iso: string, rx: number, tx: number) => ({ at: iso, cpu_percent: 0, mem_percent: 0, load1: 0, rx_speed: rx, tx_speed: tx, tcp_conns: 0 })
    const b = bandwidthBuckets([p(at(10, 5), 1e6, 0), p(at(10, 20), 3e6, 0), p(at(9, 0), 125000, 0), p(at(8, 0), 0, 0)], now)
    expect(b).toHaveLength(24)
    expect(b[23]).toEqual({ start: new Date(2026, 8, 24, 10, 0).getTime(), mbps: 16 })
    expect(b[22]).toEqual({ start: new Date(2026, 8, 24, 9, 0).getTime(), mbps: 1 })
    expect(b[0]!.mbps).toBeNull()
    expect(b[0]!.start).toBe(new Date(2026, 8, 23, 11, 0).getTime())
  })

  it('bandwidthPeak picks max mbps and earliest start timestamp on tie', () => {
    expect(bandwidthPeak([{ start: 1000, mbps: null }, { start: 2000, mbps: null }])).toBeNull()
    expect(bandwidthPeak([{ start: 3000, mbps: 0 }])).toBeNull()
    expect(bandwidthPeak([{ start: 5000, mbps: 2 }, { start: 8000, mbps: 10 }, { start: 3000, mbps: 10 }])).toEqual({ mbps: 10, start: 3000 })
  })

  it('bucketRangeLabel formats today, yesterday, and 24:00 ending correctly', () => {
    const now = new Date(2026, 9, 21, 15, 30)
    // 今天 14:00–15:00
    const todayStart = new Date(2026, 9, 21, 14, 0).getTime()
    expect(bucketRangeLabel(todayStart, now)).toBe('今天 14:00–15:00')
    // 昨天 10:00–11:00
    const yesterdayStart = new Date(2026, 9, 20, 10, 0).getTime()
    expect(bucketRangeLabel(yesterdayStart, now)).toBe('昨天 10:00–11:00')
    // 24:00 结束整点（昨天 23:00–24:00）
    const yesterday23 = new Date(2026, 9, 20, 23, 0).getTime()
    expect(bucketRangeLabel(yesterday23, now)).toBe('昨天 23:00–24:00')
    // 更早日期 M月D日
    const olderStart = new Date(2026, 9, 15, 8, 0).getTime()
    expect(bucketRangeLabel(olderStart, now)).toBe('10月15日 08:00–09:00')
  })

  it('bandwidthPeakLabel formats peak string with day and hour or returns null', () => {
    const now = new Date(2026, 9, 21, 15, 30)
    const peak = { mbps: 393.1, start: new Date(2026, 9, 20, 23, 0).getTime() }
    expect(bandwidthPeakLabel(peak, now)).toBe('峰值 393.1 Mbps · 昨天 23:00')
    expect(bandwidthPeakLabel(null, now)).toBeNull()
  })

  it('bandwidthAxis returns 5 ticks at indices 0, 6, 12, 18, 23 with HH:00 labels', () => {
    const now = new Date(2026, 9, 21, 14, 30)
    const buckets = bandwidthBuckets([], now)
    const axis = bandwidthAxis(buckets)
    expect(axis).toHaveLength(5)
    expect(axis.map((a) => a.index)).toEqual([0, 6, 12, 18, 23])
    expect(axis[4]!.label).toBe('14:00')
    expect(axis[3]!.label).toBe('09:00')
    expect(axis[2]!.label).toBe('03:00')
    expect(axis[1]!.label).toBe('21:00')
    expect(axis[0]!.label).toBe('15:00')
  })
})

describe('onlineUsersView', () => {
  it('formats online node with users or dash when 0', () => {
    expect(onlineUsersView({ serving_status: 'active', stale: false, online_users: 12 })).toEqual({ value: '12', note: null })
    expect(onlineUsersView({ serving_status: 'active', stale: false, online_users: 0 })).toEqual({ value: '—', note: null })
  })

  it('formats offline node with previous online users note or null note when 0', () => {
    expect(onlineUsersView({ serving_status: 'active', stale: true, online_users: 8 })).toEqual({ value: '—', note: '离线前 8 人在线' })
    expect(onlineUsersView({ serving_status: 'active', stale: true, online_users: 0 })).toEqual({ value: '—', note: null })
  })

  it('formats disabled node without offline note', () => {
    expect(onlineUsersView({ serving_status: 'disabled', stale: false, online_users: 5 })).toEqual({ value: '5', note: null })
    expect(onlineUsersView({ serving_status: 'disabled', stale: false, online_users: 0 })).toEqual({ value: '—', note: null })
  })
})

describe('certAttention', () => {
  const now = new Date(2026, 9, 21, 12, 0).getTime()

  it('identifies failing certificate on active node', () => {
    const n = {
      serving_status: 'active' as const,
      cert: {
        mode: 'acme' as const,
        identifier: 'test.example.com',
        identifier_kind: 'domain' as const,
        not_after: new Date(now + 30 * 86400_000).toISOString(),
        error_code: 'acme_dns_failed',
        error_detail: 'timeout',
        next_attempt_at: null,
        reported_at: '2026-09-01T00:00:00Z',
      },
    }
    expect(certAttention(n, now)).toBe(true)
  })

  it('identifies IP cert expiring in 1 day (deadline 2 days)', () => {
    const n = {
      serving_status: 'active' as const,
      cert: {
        mode: 'acme' as const,
        identifier: '1.2.3.4',
        identifier_kind: 'ip' as const,
        not_after: new Date(now + 1 * 86400_000).toISOString(),
        error_code: null,
        error_detail: null,
        next_attempt_at: null,
        reported_at: '2026-09-01T00:00:00Z',
      },
    }
    expect(certAttention(n, now)).toBe(true)
  })

  it('identifies domain cert expiring in 10 days (deadline 14 days)', () => {
    const n = {
      serving_status: 'active' as const,
      cert: {
        mode: 'acme' as const,
        identifier: 'sub.example.com',
        identifier_kind: 'domain' as const,
        not_after: new Date(now + 10 * 86400_000).toISOString(),
        error_code: null,
        error_detail: null,
        next_attempt_at: null,
        reported_at: '2026-09-01T00:00:00Z',
      },
    }
    expect(certAttention(n, now)).toBe(true)
  })

  it('does not flag domain cert expiring in 30 days', () => {
    const n = {
      serving_status: 'active' as const,
      cert: {
        mode: 'acme' as const,
        identifier: 'sub.example.com',
        identifier_kind: 'domain' as const,
        not_after: new Date(now + 30 * 86400_000).toISOString(),
        error_code: null,
        error_detail: null,
        next_attempt_at: null,
        reported_at: '2026-09-01T00:00:00Z',
      },
    }
    expect(certAttention(n, now)).toBe(false)
  })

  it('ignores draft, disabled, or missing cert nodes even with failing/expiring status', () => {
    const draft = {
      serving_status: 'draft' as const,
      cert: {
        mode: 'acme' as const,
        identifier: '1.2.3.4',
        identifier_kind: 'ip' as const,
        not_after: new Date(now + 1 * 86400_000).toISOString(),
        error_code: 'failed',
        error_detail: null,
        next_attempt_at: null,
        reported_at: '2026-09-01T00:00:00Z',
      },
    }
    expect(certAttention(draft, now)).toBe(false)

    const disabled = { ...draft, serving_status: 'disabled' as const }
    expect(certAttention(disabled, now)).toBe(false)

    expect(certAttention({ serving_status: 'active', cert: null }, now)).toBe(false)
  })
})

describe('parseNodeFilter and nodeCounts', () => {
  it('parseNodeFilter maps known values and defaults to all', () => {
    expect(parseNodeFilter('online')).toBe('online')
    expect(parseNodeFilter('cert')).toBe('cert')
    expect(parseNodeFilter(null)).toBe('all')
    expect(parseNodeFilter('x')).toBe('all')
  })

  it('nodeCounts excludes retired and counts online via nodeState', () => {
    const rows = [
      row({ serving_status: 'active', stale: false }),
      row({ id: 'n2', serving_status: 'active', stale: true }),
      row({ id: 'n3', serving_status: 'retired', stale: false }),
      row({ id: 'n4', serving_status: 'disabled', stale: false }),
    ]
    expect(nodeCounts(rows)).toEqual({ total: 3, online: 1 })
  })
})

describe('activationHint and NO_POOL_HINT', () => {
  it('returns updated hint when node is not assigned to a pool', () => {
    expect(activationHint('未划入节点池，不服务任何用户')).toBe(
      '已上线。节点还没划入节点池，不服务任何用户——下一步在「设置」页签的「访问」里点「编辑」选一个节点池。',
    )
  })

  it('keeps pool not bound to plans hint intact', () => {
    expect(activationHint('所在节点池没有绑定任何套餐，暂时不服务任何用户')).toBe(
      '已上线。所在节点池还没绑定套餐，暂时不服务用户——下一步到「套餐」页，在套餐版本里选上这个节点池并发布。',
    )
  })

  it('appends other warnings after 已上线。', () => {
    expect(activationHint('其他警告')).toBe('已上线。其他警告')
  })

  it('NO_POOL_HINT matches standard node pool wording', () => {
    expect(NO_POOL_HINT).toBe('选择节点池：不在任何节点池里的节点不服务任何用户')
  })
})

describe('protocolSummary', () => {
  const ssSummarySchema = miniSchema({
    node_type: 'shadowsocks',
    allowed_properties: ['cipher', 'password', 'note_adv', 'flow', 'flag'],
    enums: { cipher: ['aes-256-gcm'], flow: ['plain'] },
    property_types: { flag: 'boolean' },
    fields: [
      { path: 'cipher', label: '加密', option_labels: { 'aes-256-gcm': 'AES-256-GCM' } },
      { path: 'password', label: '密码' },
      { path: 'note_adv', label: '备注', advanced: true },
      { path: 'flow', label: '流控', show_if: { cipher: ['aes-256-gcm'] } },
      { path: 'flag', label: 'UDP', default: true },
    ],
  })

  it('builds rows from schema with visibility, secrets and types', () => {
    const node = row({
      node_type: 'shadowsocks',
      server_port: 8388,
      protocol_config: { cipher: 'aes-256-gcm', password: 'x', flow: 'plain', flag: true },
    })
    const rows = protocolSummary(ssSummarySchema, node)
    expect(rows[0]).toEqual({ label: '协议', value: 'Shadowsocks' })
    expect(rows[1]).toEqual({ label: '监听端口', value: '8388' })
    expect(rows.find((r) => r.label === '密码')).toEqual({ label: '密码', value: '不回显', muted: true })
    expect(rows.find((r) => r.label === '加密')).toEqual({ label: '加密', value: 'AES-256-GCM' })
    expect(rows.find((r) => r.label === 'UDP')).toEqual({ label: 'UDP', value: '开启' })
    expect(rows.some((r) => r.label === '备注')).toBe(false)
    expect(rows.some((r) => r.label === '流控')).toBe(true)
  })

  it('hides fields when show_if fails and only two rows without schema', () => {
    const hidden = row({
      node_type: 'shadowsocks',
      server_port: null,
      protocol_config: { cipher: 'other' },
    })
    const rows = protocolSummary(ssSummarySchema, hidden)
    expect(rows.some((r) => r.label === '流控')).toBe(false)
    expect(rows[1]).toEqual({ label: '监听端口', value: '未设置', muted: true })
    expect(protocolSummary(undefined, row({ node_type: 'vless', server_port: 443 }))).toHaveLength(2)
  })
})

describe('filterNodes with tag and tagOptions', () => {
  it('filters rows by tag exactly and case-sensitively', () => {
    const n1 = row({ id: '1', tags: ['vip', 'iplc'] })
    const n2 = row({ id: '2', tags: ['VIP', 'direct'] })
    const n3 = row({ id: '3', tags: [] })

    expect(filterNodes([n1, n2, n3], 'all', '', 'vip')).toEqual([n1])
    expect(filterNodes([n1, n2, n3], 'all', '', 'VIP')).toEqual([n2])
    expect(filterNodes([n1, n2, n3], 'all', '', 'direct')).toEqual([n2])
    expect(filterNodes([n1, n2, n3], 'all', '', 'other')).toEqual([])
  })

  it('filters nodes with cert filter using certAttention', () => {
    const now = new Date(2026, 9, 21, 12, 0).getTime()
    const nFailing = row({
      id: 'c1',
      name: 'Failing Node',
      serving_status: 'active',
      cert: {
        mode: 'acme',
        identifier: 'a.com',
        identifier_kind: 'domain',
        not_after: null,
        error_code: 'err',
        error_detail: null,
        next_attempt_at: null,
        reported_at: '2026-09-01T00:00:00Z',
      },
    })
    const nExpiring = row({
      id: 'c2',
      name: 'Expiring Node',
      serving_status: 'active',
      cert: {
        mode: 'acme',
        identifier: '1.1.1.1',
        identifier_kind: 'ip',
        not_after: new Date(now + 86400_000).toISOString(),
        error_code: null,
        error_detail: null,
        next_attempt_at: null,
        reported_at: '2026-09-01T00:00:00Z',
      },
    })
    const nHealthy = row({
      id: 'c3',
      name: 'Healthy Node',
      serving_status: 'active',
      cert: {
        mode: 'acme',
        identifier: 'ok.com',
        identifier_kind: 'domain',
        not_after: new Date(now + 30 * 86400_000).toISOString(),
        error_code: null,
        error_detail: null,
        next_attempt_at: null,
        reported_at: '2026-09-01T00:00:00Z',
      },
    })
    const nDisabled = row({
      id: 'c4',
      name: 'Disabled Node',
      serving_status: 'disabled',
      cert: {
        mode: 'acme',
        identifier: 'a.com',
        identifier_kind: 'domain',
        not_after: null,
        error_code: 'err',
        error_detail: null,
        next_attempt_at: null,
        reported_at: '2026-09-01T00:00:00Z',
      },
    })

    const rows = [nFailing, nExpiring, nHealthy, nDisabled]
    expect(filterNodes(rows, 'cert', '', '', now)).toEqual([nFailing, nExpiring])
    // Still filters by search query
    expect(filterNodes(rows, 'cert', 'expiring', '', now)).toEqual([nExpiring])
  })

  it('search query does not match tags', () => {
    const n1 = row({ id: '1', name: 'HK 01', tags: ['special-route'] })
    expect(filterNodes([n1], 'all', 'special-route')).toEqual([])
  })

  it('tagOptions dedupes case-sensitively and sorts zh-Hans-CN', () => {
    const n1 = row({ tags: ['香港', 'BGP', 'iplc'] })
    const n2 = row({ tags: ['bgp', '香港', '阿里'] })
    const n3 = row({ tags: ['BGP'] })

    // 区分大小写去重；顺序按 zh-Hans-CN 的 localeCompare（具体先后由 ICU 定，这里只钉「排过序」）
    const opts = tagOptions([n1, n2, n3])
    expect([...opts].sort()).toEqual(['BGP', 'bgp', 'iplc', '阿里', '香港'].sort())
    expect(opts).toEqual([...opts].sort((a, b) => a.localeCompare(b, 'zh-Hans-CN')))
  })
})

describe('batchEditBody', () => {
  it('builds body with items row_version and trims reason', () => {
    const n1 = row({ id: '10', row_version: 3 })
    const n2 = row({ id: '20', row_version: 7 })
    const set = { traffic_rate: 1.5 }
    const res = batchEditBody([n1, n2], set, '  调整倍率  ')

    expect(res).toEqual({
      items: [
        { id: '10', row_version: 3 },
        { id: '20', row_version: 7 },
      ],
      set: { traffic_rate: 1.5 },
      reason: '调整倍率',
    })

    const emptyReason = batchEditBody([n1], set, '   ')
    expect(emptyReason.reason).toBe('')
  })
})

describe('sameNodeLine with new fields', () => {
  it('returns true when tags and traffic references are unchanged', () => {
    const tags = ['hk']
    const traffic = { limit_bytes: 100, reset_day: 1, used_bytes: 50, reset_at: '2026-10-01T00:00:00Z', exceeded_at: null }
    // row() 经 zod 解析会造新数组与新对象，所以第二行直接浅拷贝第一行，模拟结构共享留下的同一引用
    const a = row({ tags, traffic, relay_count: 2, ech_enabled: true })
    const b = { ...a }

    expect(sameNodeLine(a, b)).toBe(true)
  })

  it('returns false when traffic is replaced by a new object', () => {
    const tags = ['hk']
    const traffic1 = { limit_bytes: 100, reset_day: 1, used_bytes: 50, reset_at: '2026-10-01T00:00:00Z', exceeded_at: null }
    const traffic2 = { limit_bytes: 100, reset_day: 1, used_bytes: 60, reset_at: '2026-10-01T00:00:00Z', exceeded_at: null }
    const a = row({ tags, traffic: traffic1 })
    const b = { ...a, traffic: traffic2 }

    expect(sameNodeLine(a, b)).toBe(false)
  })
})


describe('withRealityKeypair', () => {
  const k = { private_key: 'GEN-PRIV', public_key: 'GEN-PUB', short_id: 'ab12' }

  it('keeps a short_id the admin already typed and still replaces both keys', () => {
    const { values, keptShortId } = withRealityKeypair({ tls: '2', 'reality_settings.short_id': '0123abcd', 'reality_settings.private_key': 'OLD' }, k)
    expect(keptShortId).toBe(true)
    expect(values).toMatchObject({ tls: '2', 'reality_settings.private_key': 'GEN-PRIV', 'reality_settings.public_key': 'GEN-PUB', 'reality_settings.short_id': '0123abcd' })
    expect(realityKeypairToast(keptShortId)).toContain('short_id 保留你填的值')
  })

  it('fills short_id only when it is empty or blank', () => {
    expect(withRealityKeypair({ tls: '2' }, k).values['reality_settings.short_id']).toBe('ab12')
    const blank = withRealityKeypair({ 'reality_settings.short_id': '  ' }, k)
    expect(blank.keptShortId).toBe(false)
    expect(blank.values['reality_settings.short_id']).toBe('ab12')
    expect(realityKeypairToast(false)).toBe('已生成 REALITY 密钥对，私钥保存后不再回显')
  })
})
