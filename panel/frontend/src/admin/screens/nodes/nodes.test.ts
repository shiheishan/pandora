import { describe, expect, it } from 'vitest'
import { NODE_PROTOCOL_SCHEMAS } from '../../../../dev/mock/admin/node-schemas'
import {
  bandwidthBuckets,
  basicFromRow,
  batchPlan,
  activationHint,
  canActivate,
  canMove,
  clearableSecret,
  clearedSecrets,
  canTransition,
  createBody,
  filterNodes,
  heartbeatLabel,
  isStable,
  KERNELS,
  legacyKernelNote,
  mapProtocolErrors,
  moveBlockers,
  moveItem,
  nodeState,
  orderItems,
  patchBody,
  protocolChanged,
  protocolFields,
  protocolNotices,
  realityEnabled,
  splitList,
  VISION_FLOW,
  withProtocolDefaults,
  insertRule,
  renameOutbound,
  routeToRow,
  rowsToOutbounds,
  rowsToRoutes,
  rulesUsing,
  toFormValues,
  toProtocolConfig,
  validateBasic,
  NO_POOL_HINT,
  NODE_LINE_FIELDS,
  sameNodeLine,
} from './logic'
import { nodeDetailSchema, nodeRowSchema, protocolSchemasResponse, type NodeDetail } from './schemas'

const SCHEMAS = protocolSchemasResponse.parse(NODE_PROTOCOL_SCHEMAS).schemas
const schemaOf = (t: string) => SCHEMAS.find((s) => s.node_type === t)!

// Go 的单取行（?id=）原样形状：指针字段为 null，nil 切片为 null；列表行是它去掉编辑字段
const row = (over: Partial<Record<string, unknown>> = {}): NodeDetail =>
  nodeDetailSchema.parse({
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
  it('accepts the real schema dump of stable protocols only', () => {
    expect(SCHEMAS).toHaveLength(13)
    expect(SCHEMAS.filter(isStable)).toHaveLength(13)
    expect(SCHEMAS.some((s) => s.status === 'legacy-read-compatible' || s.node_type === 'v2ray' || s.node_type === 'hysteria')).toBe(false)
    expect(schemaOf('socks').required).toEqual([])
    expect(row().granted_plans).toEqual([])
  })

  it('keeps the edit-form fields out of the list row shape', () => {
    const listed = nodeRowSchema.parse(row())
    for (const k of ['protocol_config', 'kernel', 'traffic_rate', 'protocol_schema_version', 'config_validated_at', 'traffic_bytes']) expect(listed).not.toHaveProperty(k)
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

  it('R106 / R107: blank secrets are left out when editing the same protocol, and clearing sends an explicit null', () => {
    const s = schemaOf('vless')
    const fields = protocolFields(s)
    const values = toFormValues(fields, { network: 'tcp', tls: 2, reality_settings: { dest: 'a:443', server_name: 'a' } })
    // 编辑同一协议：留空的敏感字段不带键、必填的也不算缺
    const kept = toProtocolConfig(fields, values, s.property_types, { keepSecrets: true })
    expect(JSON.stringify(kept.config)).not.toContain('private_key')
    expect(Object.keys(kept.errors).filter((k) => fields.find((f) => f.path === k)?.sensitive)).toEqual([])
    // mask_password 进了抹敏名单，按敏感字段处理（R107）
    expect(fields.find((f) => f.path.endsWith('mask_password'))?.sensitive).toBe(true)
    // 选填的敏感字段点了「清空」：显式 null，并列入保存前确认
    const optional = fields.find((f) => clearableSecret(f))!
    const cleared = new Set([optional.path])
    const out = toProtocolConfig(fields, values, s.property_types, { keepSecrets: true, cleared })
    expect(JSON.stringify(out.config)).toContain(`"${optional.path.split('.').pop()}":null`)
    expect(clearedSecrets(fields, values, cleared)).toEqual([optional.path])
    expect(clearedSecrets(fields, { ...values, [optional.path]: 'new' }, cleared)).toEqual([])
    expect(protocolChanged(fields, values, values, cleared)).toBe(true)
    // 必填的敏感字段不给清空
    const required = fields.filter((f) => f.sensitive && f.required)
    required.forEach((f) => expect(clearableSecret(f)).toBe(false))
    // 换协议（keepSecrets=false）：必填照常要填
    const hy = protocolFields(schemaOf('hysteria2'))
    expect(toProtocolConfig(hy, {}, undefined, { keepSecrets: false }).errors).toMatchObject({ cert_path: '必填' })
  })

  it('R108: offers 上线 only while the lifecycle sits between attesting and canary', () => {
    for (const status of ['attesting', 'installing', 'validating', 'standby', 'canary']) expect(canActivate({ status })).toBe(true)
    for (const status of ['draft', 'bootstrapping', 'active', 'retired', 'destroyed', 'bootstrap_failed', 'quarantined']) expect(canActivate({ status })).toBe(false)
  })

  it('R113: activation warnings read as next-step hints, unknown ones pass through', () => {
    expect(activationHint('所在节点池没有绑定任何套餐，暂时不服务任何用户')).toContain('下一步到「套餐」页')
    expect(activationHint('未划入节点池，不服务任何用户')).toContain('下一步在「协议参数」里')
    expect(activationHint('别的提示')).toBe('已上线。别的提示')
  })

  it('knows when REALITY applies and what changed', () => {
    const fields = protocolFields(schemaOf('vless'))
    const initial = toFormValues(fields, { network: 'tcp', tls: 2 })
    expect(realityEnabled('vless', initial)).toBe(true)
    expect(realityEnabled('vmess', { tls: '2' })).toBe(false)
    expect(protocolChanged(fields, initial, initial)).toBe(false)
    expect(protocolChanged(fields, initial, { ...initial, network: 'ws' })).toBe(true)
    expect(protocolChanged(fields, initial, { ...initial, 'reality_settings.private_key': 'K' })).toBe(true)
  })

  it('w4proto: VLESS + REALITY + tcp defaults to Vision, other transports drop it, a cleared flow stays cleared', () => {
    const on = withProtocolDefaults('vless', 'tls', { tls: '2' })
    expect(on.flow).toBe(VISION_FLOW)
    expect(withProtocolDefaults('vless', 'network', { tls: '2', network: 'tcp', flow: '' }).flow).toBe(VISION_FLOW)
    // 管理员自己选的 udp443 变体不被覆盖
    expect(withProtocolDefaults('vless', 'tls', { tls: '2', flow: 'xtls-rprx-vision-udp443' }).flow).toBe('xtls-rprx-vision-udp443')
    // 换到 grpc：Vision 不能用，清掉
    expect(withProtocolDefaults('vless', 'network', { ...on, network: 'grpc' }).flow).toBe('')
    // 改别的字段（包括手动清空 flow）不触发联动
    expect(withProtocolDefaults('vless', 'flow', { tls: '2', network: 'tcp', flow: '' }).flow).toBe('')
    // 不是 REALITY、不是 vless：不动
    expect(withProtocolDefaults('vless', 'tls', { tls: '0' }).flow).toBeUndefined()
    expect(withProtocolDefaults('trojan', 'tls', { tls: '2' }).flow).toBeUndefined()
    // schema 里 flow 是枚举，默认值在可选项里
    expect(protocolFields(schemaOf('vless')).find((f) => f.path === 'flow')).toMatchObject({ kind: 'enum', options: [VISION_FLOW, 'xtls-rprx-vision-udp443'] })
  })

  it('w4proto: warns that unencrypted CDN transports need CDN or TLS, and that bare tcp will be refused', () => {
    expect(protocolNotices('vless', { tls: '0', network: 'ws' })[0]).toContain('需要套 CDN 或 TLS')
    expect(protocolNotices('vmess', { tls: '0', network: 'grpc' })[0]).toContain('需要套 CDN 或 TLS')
    expect(protocolNotices('vless', { tls: '0' })[0]).toContain('会被拒绝')
    expect(protocolNotices('vmess', { tls: '0' })[0]).toContain('容易被识别')
    expect(protocolNotices('vless', { tls: '2', network: 'tcp' })).toEqual([])
    expect(protocolNotices('trojan', { tls: '1', network: 'ws' })).toEqual([])
    expect(protocolNotices('vless', { tls: '0', network: 'mkcp' })).toEqual([])
  })

  it('w4proto: carries schema hints (fallback, cert directory) and REALITY list fields', () => {
    for (const t of ['trojan', 'anytls', 'naive']) {
      expect(protocolFields(schemaOf(t)).find((f) => f.path === 'fallback')?.hint).toContain('明文 HTTP 站点')
    }
    expect(protocolFields(schemaOf('hysteria2')).find((f) => f.path === 'cert_path')?.hint).toContain('/etc/pandora-native/certs/')
    expect(protocolFields(schemaOf('anytls')).find((f) => f.path === 'utls')).toMatchObject({ kind: 'enum' })
    const s = schemaOf('vless')
    const fields = protocolFields(s)
    expect(fields.find((f) => f.path === 'reality_settings.short_id')!.kind).toBe('list')
    // 存量数组 → 逗号分隔；一个值存字符串，多个存数组
    const values = toFormValues(fields, { tls: 2, reality_settings: { server_name: ['a.example.com', 'b.example.com'], short_id: '0a1b' } })
    expect(values['reality_settings.server_name']).toBe('a.example.com, b.example.com')
    const { config } = toProtocolConfig(fields, { ...values, 'reality_settings.short_id': '0a1b，2c3d  ' }, s.property_types)
    expect(config).toMatchObject({ reality_settings: { server_name: ['a.example.com', 'b.example.com'], short_id: ['0a1b', '2c3d'] } })
    expect(toProtocolConfig(fields, { 'reality_settings.server_name': ' a.example.com ' }, s.property_types).config).toMatchObject({ reality_settings: { server_name: 'a.example.com' } })
    expect(splitList(' , ')).toEqual([])
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
    expect(Object.keys(e).sort()).toEqual(['country_code', 'name', 'node_type', 'pool_id', 'server_host', 'server_id', 'server_port', 'traffic_rate'])
  })

  it('requires a pool on create and edit, since a pool-less node serves nobody', () => {
    const filled = { name: 'A', displayName: '', serverId: 's', poolId: '', nodeType: 'shadowsocks', host: 'h', port: '8388', kernel: 'auto', rate: '1', country: '' }
    expect(validateBasic(filled, true)).toEqual({ pool_id: NO_POOL_HINT })
    expect(validateBasic(filled, false)).toEqual({ pool_id: NO_POOL_HINT })
    expect(validateBasic({ ...filled, poolId: 'p1' }, true)).toEqual({})
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

  it('w4proto: only native kernels are offered; a stored sing-box / xray-core reads as auto and is replaced when the protocol is revalidated', () => {
    expect(KERNELS.map(([v]) => v)).toEqual(['auto', 'pandora-native'])
    const legacy = row({ kernel: 'sing-box' })
    expect(basicFromRow(legacy).kernel).toBe('auto')
    expect(legacyKernelNote(legacy)).toContain('sing-box')
    expect(legacyKernelNote(row())).toBeUndefined()
    // 只改名字：不碰协议，库里的旧值原样留着
    expect(patchBody(legacy, { ...basicFromRow(legacy), name: 'x' }, { changed: false, config: {} })).toEqual({ row_version: 5, name: 'x' })
    // 改端口或协议：后端会重新校验，内核一起换成表单值
    expect(patchBody(legacy, { ...basicFromRow(legacy), port: '8443' }, { changed: false, config: {} })).toEqual({ row_version: 5, server_port: 8443, kernel: 'auto' })
    expect(patchBody(legacy, basicFromRow(legacy), { changed: true, config: { network: 'ws' } })).toEqual({ row_version: 5, protocol_config: { network: 'ws' }, kernel: 'auto' })
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

  it('inserts new rules before a trailing fallback', () => {
    const add = { kind: 'domain' as const, value: 'x.com', outbound: 'direct', enabled: true, note: '' }
    const fb = { kind: 'fallback' as const, value: '', outbound: 'US', enabled: true, note: '' }
    expect(insertRule([fb], add)).toEqual([add, fb])
    expect(insertRule([add], fb)).toEqual([add, fb])
    expect(insertRule([], add)).toEqual([add])
  })

  it('counts and renames rules that point at an outbound', () => {
    const rows = [
      { kind: 'domain' as const, value: 'a', outbound: 'US-LAX-01', enabled: true, note: '' },
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
