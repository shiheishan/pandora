import { describe, expect, it } from 'vitest'
import { systemStatusSchema, type SystemStatus } from './api'
import { backupSummary, formatLatency, systemView } from './system'

const STATUS_BASE: SystemStatus = {
  backup: {
    dir: '/var/backups/pandora',
    readable: true,
    count: 2,
    total_bytes: 2048,
    latest: { name: 'a.dump.age', size: 1024, created_at: '2026-09-24T03:15:00Z', has_checksum: true, has_seal: true },
    latest_age_hours: 5,
    stale: false,
    missing_checksum: 0,
    missing_seal: 0,
    recent: [],
    identity_configured: true,
    offsite_configured: true,
  },
  database: { size_bytes: 3 * 1024 ** 3, connections: 23, max_connections: 200 },
  state: 'ok',
  components: [
    { key: 'postgres', state: 'ok', latency_ms: 3.2, metrics: { size_bytes: 3 * 1024 ** 3, connections: 23, max_connections: 200 } },
    { key: 'backup', state: 'ok', metrics: { latest_age_hours: 5, stale: false, identity_configured: true, offsite_configured: true } },
  ],
}

const ALL_OK_COMPONENTS: SystemStatus['components'] = [
  { key: 'postgres', state: 'ok', latency_ms: 3.2, metrics: { size_bytes: 3 * 1024 ** 3, connections: 23, max_connections: 200 } },
  { key: 'redis', state: 'ok', latency_ms: 0.4, metrics: {} },
  { key: 'node_fabric', state: 'ok', metrics: { total: 44, online: 44, config_lagging: 0 } },
  { key: 'payment_callbacks', state: 'ok', metrics: { pending: 0 } },
  { key: 'mail', state: 'ok', metrics: { queued: 0, retrying: 0, failed_total: 0 } },
  { key: 'telegram', state: 'ok', metrics: { queued: 48, retrying: 0, failed_total: 0 } },
  { key: 'sse', state: 'ok', metrics: { connections: 38 } },
  { key: 'backup', state: 'ok', metrics: { latest_age_hours: 5, stale: false, identity_configured: true, offsite_configured: true } },
]

describe('格式化', () => {
  it('formatLatency', () => {
    expect(formatLatency(0.4)).toBe('0.4 ms')
    expect(formatLatency(3.2)).toBe('3 ms')
  })
})

describe('系统状态', () => {
  it('备份摘要：读不到是未知、不是没有；多个问题一起列', () => {
    expect(backupSummary({ dir: '/x', readable: false, message: '读不到' })).toEqual({ state: 'unknown', meta: '读不到备份目录' })
    expect(backupSummary({ dir: '/x', readable: true, count: 0, stale: true, recent: [] })).toEqual({ state: 'warn', meta: '还没有备份' })
    expect(backupSummary(STATUS_BASE.backup)).toEqual({ state: 'ok', meta: '最近一份 5 小时前' })
    expect(backupSummary({ ...STATUS_BASE.backup, stale: true, identity_configured: false, offsite_configured: false, missing_checksum: 2 }).meta).toBe(
      '过期 · 未配解密私钥 · 未配异地 · 2 份缺校验',
    )
    expect(backupSummary({ ...STATUS_BASE.backup, latest_age_hours: 72 }).meta).toBe('最近一份 3 天前')
  })

  it('备份摘要：缺封条的单独说出来；有文件却没有能恢复的一份，不说「还没有」', () => {
    expect(backupSummary({ ...STATUS_BASE.backup, missing_seal: 1 })).toEqual({ state: 'warn', meta: '1 份缺封条' })
    expect(backupSummary({ ...STATUS_BASE.backup, missing_checksum: 1, missing_seal: 2 }).meta).toBe('1 份缺校验 · 2 份缺封条')
    const none = { dir: '/x', readable: true, count: 3, stale: true, missing_seal: 3, recent: [] }
    expect(backupSummary(none)).toEqual({ state: 'warn', meta: '3 份都恢复不了（缺封条或校验）' })
  })

  it('行来自 components，备份行排最后、8 行与排序（attention 在前）', () => {
    const view = systemView(STATUS_BASE)
    expect(view.rows).toHaveLength(2)
    expect(view.rows.map((r) => r.key)).toEqual(['postgres', 'backup'])
    expect(view.rows[0]).toMatchObject({ key: 'postgres', tone: 'ok', why: '正常', metric: '3 ms · 23/200 连接' })
    expect(view.rows[1]).toMatchObject({ key: 'backup', tone: 'ok', why: '正常', metric: '5 小时前', action: { kind: 'backup' } })
    expect(view).toMatchObject({ label: '全部正常', tone: 'ok', attention: 0 })
  })

  it('attention / label / tone / action / why / metric：node_fabric、mail、backup 三项 warn', () => {
    const status: SystemStatus = {
      ...STATUS_BASE,
      backup: { ...STATUS_BASE.backup, offsite_configured: false, missing_checksum: 1 },
      state: 'degraded',
      components: [
        { key: 'backup', state: 'warn', metrics: { latest_age_hours: 5, stale: false, identity_configured: true, offsite_configured: false }, message: '未配异地' },
        { key: 'postgres', state: 'ok', latency_ms: 3.2, metrics: { size_bytes: 1024 ** 3, connections: 23, max_connections: 200 } },
        { key: 'redis', state: 'ok', latency_ms: 0.4, metrics: {} },
        { key: 'node_fabric', state: 'warn', metrics: { total: 44, online: 41, config_lagging: 2 } },
        { key: 'payment_callbacks', state: 'ok', metrics: { pending: 0 } },
        { key: 'mail', state: 'down', metrics: { queued: 214, retrying: 21, failed_total: 9 } },
        { key: 'telegram', state: 'ok', metrics: { queued: 48, retrying: 0, failed_total: 0 } },
        { key: 'sse', state: 'unknown', metrics: { connections: 38 } },
      ],
    }
    const view = systemView(status)
    expect(view.rows).toHaveLength(8)
    expect(view.attention).toBe(4)
    expect(view).toMatchObject({ label: '4 项需要关注', tone: 'warn' })

    const attentionKeys = view.rows.filter((r) => r.attention).map((r) => r.key)
    expect(attentionKeys).toEqual(['node_fabric', 'mail', 'sse', 'backup'])
    // 备份行排在所属组（要关注）的最后，后面才是正常的组件
    expect(view.rows.map((r) => r.key).indexOf('backup')).toBe(3)

    expect(view.rows.find((r) => r.key === 'node_fabric')).toMatchObject({
      tone: 'warn',
      why: '3 个节点离线 · 2 个节点配置未同步',
      metric: '41/44 在线',
      action: { kind: 'go', target: { module: 'nodes', tab: 'nodes', query: { f: 'offline' } } },
    })
    expect(view.rows.find((r) => r.key === 'mail')).toMatchObject({
      tone: 'bad',
      why: '9 封失败',
      metric: '214 排队',
      action: { kind: 'go', target: { module: 'system', tab: 'notify' } },
    })
    expect(view.rows.find((r) => r.key === 'backup')).toMatchObject({
      tone: 'warn',
      why: '未配异地 · 1 份缺校验',
      metric: '5 小时前',
      action: { kind: 'backup' },
    })
    expect(view.rows.find((r) => r.key === 'postgres')!.action).toBeUndefined()
  })

  it('全部 ok 时「全部正常」', () => {
    const status: SystemStatus = { ...STATUS_BASE, components: ALL_OK_COMPONENTS }
    const view = systemView(status)
    expect(view.rows).toHaveLength(8)
    expect(view.attention).toBe(0)
    expect(view.label).toBe('全部正常')
    expect(view.tone).toBe('ok')
    expect(view.rows.every((r) => r.why === '正常')).toBe(true)
    expect(view.rows.map((r) => r.key).at(-1)).toBe('backup')
  })

  it('node_fabric 调度：无离线时去 nodes 列表且不带 query；有离线时文案说明且带 f=offline 筛选', () => {
    const laggingOnly: SystemStatus = {
      ...STATUS_BASE,
      state: 'degraded',
      components: [
        { key: 'node_fabric', state: 'warn', metrics: { total: 44, online: 44, config_lagging: 2 } },
        { key: 'backup', state: 'ok', metrics: { latest_age_hours: 5, stale: false, identity_configured: true, offsite_configured: true } },
      ],
    }
    const lagging = systemView(laggingOnly).rows.find((r) => r.key === 'node_fabric')!
    expect(lagging.why).toBe('2 个节点配置未同步')
    // toEqual：没有离线时 target 不带 query
    expect(lagging.action).toEqual({ kind: 'go', target: { module: 'nodes', tab: 'nodes' } })

    const offlineOnly: SystemStatus = {
      ...STATUS_BASE,
      state: 'degraded',
      components: [
        { key: 'node_fabric', state: 'warn', metrics: { total: 44, online: 40, config_lagging: 0 } },
        { key: 'backup', state: 'ok', metrics: { latest_age_hours: 5, stale: false, identity_configured: true, offsite_configured: true } },
      ],
    }
    const viewOffline = systemView(offlineOnly)
    expect(viewOffline.rows.find((r) => r.key === 'node_fabric')).toMatchObject({
      why: '4 个节点离线',
      action: { kind: 'go', target: { module: 'nodes', tab: 'nodes', query: { f: 'offline' } } },
    })
    // 待办给了离线节点名就点名，名字比离线数少时写「等」
    expect(systemView(offlineOnly, ['新加坡 02', '东京 03']).rows.find((r) => r.key === 'node_fabric')!.why).toBe('4 个节点离线：新加坡 02、东京 03 等')
    const one: SystemStatus = { ...offlineOnly, components: [{ key: 'node_fabric', state: 'warn', metrics: { total: 7, online: 6, config_lagging: 0 } }, offlineOnly.components[1]!] }
    expect(systemView(one, ['新加坡 02']).rows.find((r) => r.key === 'node_fabric')!.why).toBe('1 个节点离线：新加坡 02')
  })
})

describe('schema 守住契约形状', () => {
  it('系统状态：后端读不到备份目录时只有 dir / readable / message', () => {
    const components = [
      { key: 'postgres', state: 'down', metrics: {}, message: '数据库不可达' },
      { key: 'backup', state: 'unknown', metrics: {}, message: '读不到' },
    ]
    const res = systemStatusSchema.safeParse({
      backup: { dir: '/x', readable: false, message: '读不到' },
      database: { error: '读取数据库状态失败' },
      state: 'degraded',
      components,
    })
    expect(res.success).toBe(true)
    expect(systemStatusSchema.safeParse({ backup: { dir: '/x', readable: false }, database: { error: 'x' } }).success).toBe(false)
  })
})

describe('修订 R52：down / unknown 时 metrics 字段可缺', () => {
  it('schema 放行缺字段与空 metrics，界面缺失处显示 —', () => {
    const raw = {
      ...STATUS_BASE,
      state: 'degraded',
      components: [
        { key: 'postgres', state: 'down', metrics: {} },
        { key: 'node_fabric', state: 'unknown', metrics: { total: 44 } },
        { key: 'mail', state: 'down', metrics: { failed_total: 3 } },
        { key: 'sse', state: 'unknown', latency_ms: 2, metrics: {} },
        { key: 'backup', state: 'unknown', metrics: {} },
      ],
    }
    const parsed = systemStatusSchema.safeParse(raw)
    expect(parsed.success).toBe(true)
    const view = systemView(parsed.data!)
    expect(view.rows.map((r) => [r.key, r.metric])).toEqual([
      ['postgres', '—'],
      ['node_fabric', '—/44 在线'],
      ['mail', '— 排队'],
      ['sse', '2 ms'],
      ['backup', '5 小时前'],
    ])
  })

  it('数据库状态读失败而 SELECT 1 成功：postgres ok，三项指标是 null（system_components.go 照抄 database 段）', () => {
    const raw = {
      ...STATUS_BASE,
      database: { error: '读取数据库状态失败' },
      components: [{ key: 'postgres', state: 'ok', latency_ms: 1, metrics: { size_bytes: null, connections: null, max_connections: null } }],
    }
    const parsed = systemStatusSchema.safeParse(raw)
    expect(parsed.success).toBe(true)
    expect(systemView(parsed.data!).rows.find((r) => r.key === 'postgres')?.metric).toBe('1 ms · —/— 连接')
  })
})
