/**
 * [INPUT]: 依赖 vitest，依赖 ./infra 的全部纯函数，依赖 ./schemas 的服务器 / 节点池 / 节点行 schema（核对 Go 的 null 指针字段）
 * [OUTPUT]: 对外提供服务器与节点池纯逻辑的单元测试
 * [POS]: admin/screens/nodes 第 ③ 步的单元测试：schema 接住 Go 原样形状、服务器圆点与快捷状态切换、合法状态边、删除资格与后果文案、三条占用与三档色、节点按服务器分组、服务器表单校验 / 新建体 / PATCH 差量 / 容量冲突；节点池删除资格、绑定套餐文字、新建体与编辑差量；界面交互在浏览器里对 dev/mock/admin/nodes-infra.ts 验收
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it } from 'vitest'
import {
  canDeleteServer,
  capacityMinimum,
  createPoolBody,
  createServerBody,
  deleteServerNotice,
  emptyServerForm,
  meterLevel,
  nextServerStatuses,
  nodesByServer,
  patchPoolBody,
  patchServerBody,
  planNamesLabel,
  poolDeleteBlock,
  poolFormFrom,
  quickToggle,
  serverDot,
  serverFormFrom,
  serverMeters,
  validateServerForm,
} from './infra'
import { nodeRowSchema, poolsResponse, serverSchema, serversResponse, type NodeRow, type Server } from './schemas'

// Go 的 nodefabric.Server 原样形状：指针字段全是 null
const server = (over: Partial<Record<string, unknown>> = {}): Server =>
  serverSchema.parse({
    id: 's1',
    name: 'hk-hkg-edge-1',
    status: 'ready',
    status_reason: null,
    row_version: 4,
    region: '香港',
    hostname: null,
    public_ipv4: '103.151.12.8',
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
    control_node_id: null,
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
    status: 'active',
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
    health_score: null,
    applied_config_version: null,
    desired_config_version: null,
    last_heartbeat_at: null,
    stale: true,
    delivered_to_users: false,
    delivery_note: '',
    identity_serial: null,
    created_at: '2026-09-01T00:00:00Z',
    node_type: 'vless',
    server_host: null,
    server_port: null,
    traffic_rate: 1,
    display_name: null,
    country_code: null,
    kernel: 'auto',
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
    traffic_bytes: 0,
    granted_plans: null,
    ...over,
  })

describe('schemas', () => {
  it('accepts Go shapes and stays strict about the rest', () => {
    expect(serversResponse.parse({ servers: [server()], total: 1 }).servers[0]!.disk_used_gb).toBeNull()
    expect(() => serversResponse.parse({ servers: null, total: 0 })).toThrow()
    expect(() => serverSchema.parse({ ...server(), status: 'online' })).toThrow()
    const pools = poolsResponse.parse({ pools: [{ id: 'p', code: 'asia', name: '亚太', region: '', status: 'active', nodes: 0, active_nodes: 0, plans: 0, members: [], plan_names: [] }] })
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
    expect(quickToggle('unhealthy')).toBeNull()
    expect(quickToggle('retired')).toBeNull()
    for (const from of ['ready', 'draining', 'maintenance', 'draft'] as const) expect(nextServerStatuses(from)).toContain(quickToggle(from)!.to)
    expect(nextServerStatuses('ready')).not.toContain('maintenance')
    expect(nextServerStatuses('retired')).toEqual([])
  })

  it('only deletes drafts and retired servers, and says what happens to nodes', () => {
    expect(canDeleteServer(server({ status: 'draft' }))).toBe(true)
    expect(canDeleteServer(server({ status: 'retired' }))).toBe(true)
    expect(canDeleteServer(server({ status: 'maintenance' }))).toBe(false)
    expect(deleteServerNotice(server({ node_count: 3 }))).toContain('名下 3 个节点会一起下线')
    expect(deleteServerNotice(server({ node_count: 0 }))).toContain('名下没有节点')
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
    const errors = validateServerForm({ ...emptyServerForm(), name: ' ', publicIpv4: '1.2.3.256', privateIpv4: '10.0.0.1', publicIpv6: '1.2.3.4', capacity: '0', architecture: 'x'.repeat(33) })
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
  const pool = { id: 'p', code: 'asia', name: '亚太', region: 'HK', status: 'active' as const, nodes: 0, active_nodes: 0, plans: 0, members: [], plan_names: [] as string[] }

  it('blocks deletion while nodes or plans hang on it', () => {
    expect(poolDeleteBlock(pool)).toBeNull()
    expect(poolDeleteBlock({ ...pool, nodes: 2 })).toContain('2 个节点')
    expect(poolDeleteBlock({ ...pool, plans: 1 })).toContain('1 个套餐版本')
  })

  it('labels bound plans without the undecided user-group part', () => {
    expect(planNamesLabel(pool)).toBe('—')
    expect(planNamesLabel({ plan_names: ['专业版', '团队版'] })).toBe('专业版、团队版')
  })

  it('creates and patches with the handler semantics', () => {
    expect(createPoolBody({ name: ' 亚太 ', code: '', region: '', status: 'disabled' })).toEqual({ name: '亚太' })
    expect(createPoolBody({ name: '欧美', code: 'eu', region: 'EU', status: 'active' })).toEqual({ name: '欧美', code: 'eu', region: 'EU' })
    expect(patchPoolBody(pool, poolFormFrom(pool))).toBeNull()
    expect(patchPoolBody(pool, { ...poolFormFrom(pool), region: '', status: 'draining', code: 'x' })).toEqual({ status: 'draining' })
    expect(patchPoolBody(pool, { ...poolFormFrom(pool), name: '亚太精选' })).toEqual({ name: '亚太精选' })
  })
})
