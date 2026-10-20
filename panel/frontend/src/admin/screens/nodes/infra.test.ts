import { describe, expect, it } from 'vitest'
import {
  canDeleteServer,
  capacityMinimum,
  createPoolBody,
  createServerBody,
  deleteServerNotice,
  emptyPoolForm,
  emptyServerForm,
  meterLevel,
  nextServerStatuses,
  nodesByServer,
  patchPoolBody,
  patchServerBody,
  planNamesLabel,
  poolDeleteBlock,
  poolFormFrom,
  poolGroupsChanged,
  poolGroupsLabel,
  quickToggle,
  serverDeleteBlock,
  serverDot,
  serverFormFrom,
  serverMeters,
  serverNodeLines,
  serverNotes,
  serverSubtitle,
  validateServerForm,
} from './infra'
import { nodeRowSchema, poolsResponse, serverSchema, serversResponse, type NodeRow, type Server } from './schemas'

// Go 的 node/handler serverResponse 原样形状：指针字段全是 null
const server = (over: Partial<Record<string, unknown>> = {}): Server =>
  serverSchema.parse({
    id: 's1',
    name: 'hk-hkg-edge-1',
    status: 'ready',
    status_reason: null,
    row_version: 4,
    region: '香港',
    hostname: null,
    public_ipv4: '192.0.2.8',
    public_ipv6: null,
    private_ipv4: null,
    architecture: null,
    os_name: null,
    agent_version: 'core r53',
    last_heartbeat_at: '2026-09-24T10:00:00Z',
    heartbeat_online: true,
    cpu_cores: 4,
    memory_mb: 4096,
    disk_gb: 80,
    capacity_nodes: 8,
    notes: null,
    node_count: 2,
    active_node_count: 2,
    serving_node_count: 1,
    never_seen_node_count: 1,
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-20T00:00:00Z',
    cpu_bp: 6234,
    mem_used_mb: 2600,
    mem_total_mb: 4096,
    disk_used_gb: null,
    disk_total_gb: null,
    metrics_at: '2026-09-24T10:00:00Z',
    ...over,
  })

const node = (over: Partial<Record<string, unknown>>): NodeRow =>
  nodeRowSchema.parse({
    id: 'n',
    node_no: 1,
    row_version: 1,
    name: 'n',
    serving_status: 'active',
    server_id: 's1',
    server_name: null,
    pool_id: null,
    pool_name: null,
    agent_version: null,
    hostname: null,
    public_ipv4: null,
    cpu_cores: null,
    memory_mb: null,
    disk_gb: null,
    last_heartbeat_at: null,
    stale: true,
    delivered_to_users: false,
    delivery_note: '',
    identity_serial: null,
    movable: true,
    created_at: '2026-09-01T00:00:00Z',
    node_type: 'vless',
    server_host: null,
    server_port: null,
    traffic_rate: 1,
    display_name: null,
    country_code: null,
    protocol_config: null,
    protocol_schema_version: 1,
    config_validated_at: null,
    sort_order: 0,
    online_users: 0,
    online_ips: 0,
    traffic_bytes_24h: 0,
    cpu_percent: null,
    mem_percent: null,
    metrics_at: null,
    granted_plans: null,
    tags: [],
    traffic: { limit_bytes: null, reset_day: null, used_bytes: 0, reset_at: '2026-10-01T00:00:00Z', exceeded_at: null },
    traffic_rate_schedule: [],
    relay_count: 0,
    ech_enabled: false,
    ...over,
  })

describe('schemas', () => {
  it('accepts Go shapes and stays strict about the rest', () => {
    expect(serversResponse.parse({ servers: [server()], total: 1 }).servers[0]!.disk_used_gb).toBeNull()
    expect(() => serversResponse.parse({ servers: null, total: 0 })).toThrow()
    expect(() => serverSchema.parse({ ...server(), status: 'online' })).toThrow()
    const pools = poolsResponse.parse({ pools: [{ id: 'p', code: 'asia', name: '亚太', region: '', status: 'active', nodes: 0, active_nodes: 0, plans: 0, members: [], plan_names: [], allowed_user_groups: [] }] })
    expect(pools.pools[0]!.members).toEqual([])
    expect(() => poolsResponse.parse({ pools: [{ ...pools.pools[0], plan_names: null }] })).toThrow()
  })
})

describe('server status', () => {
  it('colours the dot from status and heartbeat', () => {
    expect(serverDot(server())).toBe('ok')
    expect(serverDot(server({ heartbeat_online: false }))).toBe('danger')
    expect(serverDot(server({ status: 'draining', heartbeat_online: true }))).toBe('neutral')
  })

  it('maps the card toggle onto legal edges only', () => {
    expect(quickToggle('ready')).toMatchObject({ to: 'draining', label: '标记维护' })
    expect(quickToggle('draining')).toMatchObject({ to: 'ready', label: '恢复服务' })
    expect(quickToggle('maintenance')).toMatchObject({ to: 'ready' })
    expect(quickToggle('draft')).toMatchObject({ to: 'ready', label: '投入服务' })
    expect(quickToggle('retired')).toBeNull()
    for (const from of ['ready', 'draining', 'maintenance', 'draft'] as const) expect(nextServerStatuses(from)).toContain(quickToggle(from)!.to)
  })

  it('状态边表与后端 serverStatusTransitions 逐边一致', () => {
    expect(nextServerStatuses('draft')).toEqual(['ready', 'maintenance', 'retired'])
    expect(nextServerStatuses('ready')).toEqual(['draining', 'maintenance'])
    expect(nextServerStatuses('draining')).toEqual(['ready', 'maintenance', 'retired'])
    expect(nextServerStatuses('maintenance')).toEqual(['ready', 'draining', 'retired'])
    expect(nextServerStatuses('retired')).toEqual([])
  })

  it('only deletes drafts and retired servers, and says what happens to nodes', () => {
    expect(canDeleteServer(server({ status: 'draft' }))).toBe(true)
    expect(canDeleteServer(server({ status: 'retired' }))).toBe(true)
    expect(canDeleteServer(server({ status: 'maintenance' }))).toBe(false)
    expect(deleteServerNotice(server({ node_count: 3 }))).toContain('名下 3 个节点里，在役的退役；草稿和已停用的解除绑定')
    expect(deleteServerNotice(server({ node_count: 0 }))).toContain('名下没有节点')
  })

  it('serverDeleteBlock, serverSubtitle, serverNotes and serverNodeLines', () => {
    expect(serverDeleteBlock(server({ status: 'ready' }))).toBe('只有草稿或已退役的服务器能删除')
    expect(serverDeleteBlock(server({ status: 'draft' }))).toBeNull()
    expect(serverSubtitle({ region: '香港', public_ipv4: '192.0.2.8', agent_version: '1.8.2' })).toBe('香港 · 192.0.2.8 · agent 1.8.2')
    expect(serverSubtitle({ region: null, public_ipv4: null, agent_version: null })).toBe('地区待识别 · 未连接')
    const now = new Date('2026-09-24T10:05:00Z')
    const notes = serverNotes(
      {
        status: 'ready',
        status_reason: '磁盘告警',
        heartbeat_online: false,
        last_heartbeat_at: '2026-09-24T10:00:00Z',
        never_seen_node_count: 2,
      },
      now,
    )
    expect(notes).toEqual(['原因：磁盘告警', '心跳 5 分钟前，可能已断开', '2 个在役节点从未心跳（多半没装 agent）'])
    expect(serverNotes({ status: 'ready', status_reason: null, heartbeat_online: false, last_heartbeat_at: null, never_seen_node_count: 0 }, now)).toEqual([
      '还没有收到心跳',
    ])
    const lines = serverNodeLines(
      [node({ id: 'a', server_id: 's1' }), node({ id: 'b', server_id: 's1', serving_status: 'retired' }), node({ id: 'c', server_id: 's2' })],
      's1',
    )
    expect(lines.map((n) => n.id)).toEqual(['a'])
  })
})

describe('meters and node tags', () => {
  it('turns probes into three meters with the design thresholds', () => {
    expect(serverMeters(server())).toEqual([
      { label: 'CPU', percent: 62, level: 'normal' },
      { label: '内存', percent: 63, level: 'normal' },
      { label: '磁盘', percent: null, level: 'normal' },
    ])
    expect([meterLevel(65), meterLevel(66), meterLevel(85), meterLevel(86)]).toEqual(['normal', 'warm', 'warm', 'hot'])
    expect(serverMeters(server({ cpu_bp: null, mem_total_mb: 0 })).map((m) => m.percent)).toEqual([null, null, null])
  })

  it('groups live nodes by server and skips retired ones', () => {
    const map = nodesByServer([node({ id: 'a', name: '香港 01' }), node({ id: 'b', name: '台北', serving_status: 'retired' }), node({ id: 'c', name: '孤儿', server_id: null }), node({ id: 'd', name: '东京', server_id: 's2', node_type: 'hysteria2' })])
    expect(map.get('s1')).toEqual(['香港 01 · VLESS'])
    expect(map.get('s2')).toEqual(['东京 · Hysteria2'])
    expect(map.size).toBe(2)
  })
})

describe('server form', () => {
  it('validates like the Go handler', () => {
    expect(validateServerForm({ ...emptyServerForm(), name: 'edge' })).toEqual({})
    const errors = validateServerForm({ ...emptyServerForm(), name: ' ', publicIpv4: '192.0.2.256', privateIpv4: '10.0.0.1', publicIpv6: '192.0.2.4', capacity: '0', architecture: 'x'.repeat(33) })
    expect(errors).toEqual({ name: '名称必须为 1 到 120 个字符', public_ipv4: 'IP 地址格式不正确', public_ipv6: 'IP 地址格式不正确', capacity_nodes: '必须大于 0', architecture: '内容过长，最多允许 32 个字符' })
    expect(validateServerForm({ ...emptyServerForm(), name: 'edge', publicIpv6: '2001:db8::1' })).toEqual({})
  })

  it('creates with only filled fields', () => {
    expect(createServerBody({ ...emptyServerForm(), name: ' new-edge ', notes: '东京机房', capacity: '16' })).toEqual({ name: 'new-edge', capacity_nodes: 16, notes: '东京机房' })
  })

  it('patches only changed fields, clears with empty strings, never clears the name', () => {
    const s = server({ notes: '旧备注' })
    expect(patchServerBody(s, serverFormFrom(s))).toBeNull()
    expect(patchServerBody(s, { ...serverFormFrom(s), notes: '', region: '东京', capacity: '10' })).toEqual({ row_version: 4, notes: '', region: '东京', capacity_nodes: 10 })
    expect(patchServerBody(s, { ...serverFormFrom(s), name: 'renamed' })).toEqual({ row_version: 4, name: 'renamed' })
  })

  it('reads the capacity conflict minimum', () => {
    expect(capacityMinimum({ capacity_nodes: 'minimum=5' })).toBe(5)
    expect(capacityMinimum({ row_version: 'current=3' })).toBeNull()
  })
})

describe('pools', () => {
  const pool = { id: 'p', code: 'asia', name: '亚太', region: 'HK', status: 'active' as const, nodes: 0, active_nodes: 0, plans: 0, members: [], plan_names: [] as string[], allowed_user_groups: [] as Array<{ id: string; name: string }> }

  it('blocks deletion while nodes or plans hang on it', () => {
    expect(poolDeleteBlock(pool)).toBeNull()
    expect(poolDeleteBlock({ ...pool, nodes: 2 })).toBe('池里还有 2 个节点，先把它们改到别的池')
    expect(poolDeleteBlock({ ...pool, plans: 1 })).toBe('还有 1 个在售或草稿套餐绑定了它，先在套餐里解绑（已归档的不挡，删池时一起解绑）')
  })

  it('labels bound plans and the R104 user-group restriction', () => {
    expect(planNamesLabel(pool)).toBe('—')
    expect(planNamesLabel({ plan_names: ['专业版', '团队版'] })).toBe('专业版、团队版')
    expect(poolGroupsLabel(pool)).toBeNull()
    expect(poolGroupsLabel({ allowed_user_groups: [{ id: 'g1', name: '内测' }, { id: 'g2', name: 'VIP' }] })).toBe('仅限 内测、VIP')
  })

  it('R104: sends allowed_user_group_ids only when the list changes (it triggers reauth)', () => {
    const limited = { ...pool, allowed_user_groups: [{ id: 'g1', name: '内测' }] }
    expect(poolFormFrom(limited).groupIds).toEqual(['g1'])
    expect(patchPoolBody(limited, poolFormFrom(limited))).toBeNull()
    expect(patchPoolBody(limited, { ...poolFormFrom(limited), groupIds: [] })).toEqual({ allowed_user_group_ids: [] })
    expect(patchPoolBody(pool, { ...poolFormFrom(pool), groupIds: ['g2', 'g1'] })).toEqual({ allowed_user_group_ids: ['g2', 'g1'] })
    expect(poolGroupsChanged({ allowed_user_groups: [{ id: 'a', name: '' }, { id: 'b', name: '' }] }, { groupIds: ['b', 'a'] })).toBe(false)
    expect(createPoolBody({ ...emptyPoolForm(), name: '专线', groupIds: ['g1'] })).toEqual({ name: '专线', allowed_user_group_ids: ['g1'] })
    expect(poolsResponse.parse({ pools: [{ ...pool, allowed_user_groups: null }] }).pools[0]!.allowed_user_groups).toEqual([])
  })

  it('creates and patches with the handler semantics', () => {
    expect(createPoolBody({ name: ' 亚太 ', code: '', region: '', status: 'disabled', groupIds: [] })).toEqual({ name: '亚太' })
    expect(createPoolBody({ name: '欧美', code: 'eu', region: 'EU', status: 'active', groupIds: [] })).toEqual({ name: '欧美', code: 'eu', region: 'EU' })
    expect(patchPoolBody(pool, poolFormFrom(pool))).toBeNull()
    expect(patchPoolBody(pool, { ...poolFormFrom(pool), region: '', status: 'draining', code: 'x' })).toEqual({ status: 'draining' })
    expect(patchPoolBody(pool, { ...poolFormFrom(pool), name: '亚太精选' })).toEqual({ name: '亚太精选' })
  })
})
