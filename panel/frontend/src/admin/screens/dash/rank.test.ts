import { describe, expect, it } from 'vitest'
import { nodeTrafficSchema, userTrafficSchema } from './api'
import { RANK_SHOWN, rankView } from './rank'

const GiB = 1024n ** 3n

describe('rankView - 节点排行', () => {
  const nodeRaw = {
    range: '24h',
    snapshot_at: '2026-10-16T12:00:00.000000Z',
    from: '2026-10-15T12:00:00.000000Z',
    to: '2026-10-16T12:00:00.000000Z',
    basis: 'strict_raw_report_entries',
    items: [
      {
        node_id: 'node-1',
        name: 'hk-hkg-01',
        display_name: '香港 01',
        upload_bytes: String(100n * GiB),
        download_bytes: String(900n * GiB),
        total_bytes: String(1000n * GiB),
        last_report_at: '2026-10-16T11:58:00.000000Z',
      },
      {
        node_id: 'node-2',
        name: 'jp-tyo-01',
        display_name: '东京 01',
        upload_bytes: String(300n * GiB),
        download_bytes: String(300n * GiB),
        total_bytes: String(600n * GiB),
        last_report_at: '2026-10-16T11:55:00.000000Z',
      },
      {
        node_id: 'node-3',
        name: 'sg-sin-01',
        display_name: null,
        upload_bytes: String(100n * GiB),
        download_bytes: String(100n * GiB),
        total_bytes: String(200n * GiB),
        last_report_at: '2026-10-16T11:50:00.000000Z',
      },
      {
        node_id: 'node-4',
        name: 'us-lax-01',
        display_name: '洛杉矶 01',
        upload_bytes: String(120n * GiB),
        download_bytes: String(80n * GiB),
        total_bytes: String(200n * GiB),
        last_report_at: '2026-10-16T11:40:00.000000Z',
      },
    ],
    totals: {
      reported_bytes: String(2500n * GiB),
      attributed_bytes: String(2300n * GiB),
      unattributed_bytes: String(200n * GiB),
    },
    ranking: {
      returned_bytes: String(2000n * GiB),
      other_node_bytes: String(500n * GiB),
    },
    quality: {
      invalid_report_count: 0,
      invalid_entry_count: 3,
    },
  }

  it('仍能被 nodeTrafficSchema 解析', () => {
    const parsed = nodeTrafficSchema.safeParse(nodeRaw)
    expect(parsed.success).toBe(true)
  })

  const nodeData = nodeTrafficSchema.parse(nodeRaw)

  it('名字回退逻辑（display_name 为 null 时回退到 name）与 path 格式', () => {
    const view = rankView(nodeData)
    expect(view.kind).toBe('nodes')
    expect(view.rows).toHaveLength(4)

    // display_name 有值
    expect(view.rows[0]!.name).toBe('香港 01')
    expect(view.rows[0]!.path).toBe('/nodes/nodes/node-1')

    // display_name 为 null
    const sg = view.rows.find((r) => r.key === 'node-3')!
    expect(sg.name).toBe('sg-sin-01')
    expect(sg.path).toBe('/nodes/nodes/node-3')
  })

  it('share 与 barPct 计算（第一名 100，其余按比例，很小时为 4）', () => {
    const view = rankView(nodeData)
    // 分母 = 2500 GiB
    // node-1: 1000 GiB -> 1000 / 2500 = 40.0%
    expect(view.rows[0]!.share).toBe('40.0%')
    expect(view.rows[0]!.barPct).toBe(100)

    // node-2: 600 GiB -> 600 / 2500 = 24.0%
    // barPct = 600 / 1000 * 100 = 60.0%
    expect(view.rows[1]!.share).toBe('24.0%')
    expect(view.rows[1]!.barPct).toBe(60)

    // node-3: 200 GiB -> 200 / 2500 = 8.0%
    // barPct = 200 / 1000 * 100 = 20.0%
    expect(view.rows[2]!.share).toBe('8.0%')
    expect(view.rows[2]!.barPct).toBe(20)
  })

  it('按 total_bytes 降序排列，相等时保持接口原始顺序', () => {
    const view = rankView(nodeData)
    // node-3 与 node-4 的 total_bytes 相同 (200 GiB)，接口中 node-3 在前
    expect(view.rows.map((r) => r.key)).toEqual(['node-1', 'node-2', 'node-3', 'node-4'])
  })

  it('节点页脚（带 nodesOnline 与不带 nodesOnline）和 more', () => {
    const withoutOnline = rankView(nodeData)
    expect(withoutOnline.footer).toBe('合计 2.44 TB')
    expect(withoutOnline.more).toEqual({
      label: '全部节点',
      target: { module: 'nodes', tab: 'nodes' },
    })

    const withOnline = rankView(nodeData, { online: 41, total: 44 })
    expect(withOnline.footer).toBe('合计 2.44 TB · 41/44 节点在线')
  })

  it('接口给 10 名时只显示前 RANK_SHOWN 名（后端 limit 只收 5 / 10 / 20）', () => {
    const many = Array.from({ length: 10 }, (_, i) => ({
      ...nodeRaw.items[0]!,
      node_id: `node-x${i}`,
      total_bytes: String(BigInt(100 - i) * GiB),
    }))
    const view = rankView(nodeTrafficSchema.parse({ ...nodeRaw, items: many }))
    expect(RANK_SHOWN).toBe(8)
    expect(view.rows.map((r) => r.key)).toEqual(many.slice(0, 8).map((m) => m.node_id))
  })
})

describe('rankView - 用户排行', () => {
  const userRaw = {
    range: '24h',
    snapshot_at: '2026-10-16T12:00:00.000000Z',
    from: '2026-10-15T12:00:00.000000Z',
    to: '2026-10-16T12:00:00.000000Z',
    basis: 'strict_raw_report_entries',
    items: [
      {
        user_id: 'usr-1',
        email_masked: 'a***@example.test',
        subscription_count: 2,
        upload_bytes: String(50n * GiB),
        download_bytes: String(150n * GiB),
        total_bytes: String(200n * GiB),
        last_report_at: '2026-10-16T11:59:30.000000Z',
      },
      {
        user_id: 'usr-2',
        email_masked: 'b***@example.test',
        subscription_count: 1,
        upload_bytes: String(20n * GiB),
        download_bytes: String(80n * GiB),
        total_bytes: String(100n * GiB),
        last_report_at: '2026-10-16T11:30:00.000000Z',
      },
      {
        user_id: 'usr-3',
        email_masked: 'c***@example.test',
        subscription_count: 1,
        upload_bytes: '100',
        download_bytes: '100',
        total_bytes: '200', // 微量流量，测 barPct 最小为 4
        last_report_at: '2026-10-16T11:20:00.000000Z',
      },
      {
        user_id: 'usr-4',
        email_masked: 'z***@example.test',
        subscription_count: 0,
        upload_bytes: '0',
        download_bytes: '0',
        total_bytes: '0', // 0 流量，barPct 应为 0
        last_report_at: '2026-10-16T10:00:00.000000Z',
      },
    ],
    totals: {
      reported_bytes: String(400n * GiB),
      attributed_bytes: String(300n * GiB),
      unattributed_bytes: '0',
    },
    ranking: {
      returned_bytes: String(300n * GiB),
      other_user_bytes: '0',
    },
    quality: {
      invalid_report_count: 2,
      invalid_entry_count: 0,
    },
  }

  it('仍能被 userTrafficSchema 解析', () => {
    const parsed = userTrafficSchema.safeParse(userRaw)
    expect(parsed.success).toBe(true)
  })

  const userData = userTrafficSchema.parse(userRaw)

  it('用户字段、path、barPct 最小值与 0 流量', () => {
    const view = rankView(userData)
    expect(view.kind).toBe('users')
    expect(view.rows).toHaveLength(4)

    // 用户 1
    expect(view.rows[0]!.name).toBe('a***@example.test')
    expect(view.rows[0]!.path).toBe('/users/list/usr-1')
    expect(view.rows[0]!.barPct).toBe(100)

    // 用户 2: 100 GiB / 200 GiB = 50%
    expect(view.rows[1]!.name).toBe('b***@example.test')
    expect(view.rows[1]!.path).toBe('/users/list/usr-2')
    expect(view.rows[1]!.barPct).toBe(50)

    // 用户 3: 200 字节 vs 200 GiB，远小于 4%，但有流量因此最小为 4
    expect(view.rows[2]!.barPct).toBe(4)

    // 用户 4: 0 字节，barPct 为 0
    expect(view.rows[3]!.barPct).toBe(0)
    expect(view.rows[3]!.share).toBe('0.0%')
  })

  it('用户页脚（合计 + 前 N 名占比）与 more', () => {
    const view = rankView(userData)
    // 列表合计 200 GiB + 100 GiB + 200 B + 0 = 300 GiB + 200 B
    // 分母 300 GiB
    // 占比 100.0%
    expect(view.footer).toBe('合计 300 GB · 前 4 名占 100.0%')
    expect(view.more).toEqual({
      label: '全部用户',
      target: { module: 'users', tab: 'list' },
    })
  })
})

describe('rankView - 空状态与 allUnattributed', () => {
  it('节点 items 为空时 empty 为 true', () => {
    const emptyRaw = {
      range: '24h',
      snapshot_at: '2026-10-16T12:00:00.000000Z',
      from: '2026-10-15T12:00:00.000000Z',
      to: '2026-10-16T12:00:00.000000Z',
      basis: 'strict_raw_report_entries',
      items: [],
      totals: { reported_bytes: '0', attributed_bytes: '0', unattributed_bytes: '0' },
      ranking: { returned_bytes: '0', other_node_bytes: '0' },
      quality: { invalid_report_count: 0, invalid_entry_count: 0 },
    }
    const view = rankView(nodeTrafficSchema.parse(emptyRaw))
    expect(view.kind).toBe('nodes')
    expect(view.empty).toBe(true)
    expect(view.allUnattributed).toBe(false)
    expect(view.footer).toBe('合计 0 B')
    expect(view.more).toEqual({
      label: '全部节点',
      target: { module: 'nodes', tab: 'nodes' },
    })
  })

  it('用户 items 为空且全部未归属时 allUnattributed 为 true', () => {
    const allUnattRaw = {
      range: '24h',
      snapshot_at: '2026-10-16T12:00:00.000000Z',
      from: '2026-10-15T12:00:00.000000Z',
      to: '2026-10-16T12:00:00.000000Z',
      basis: 'strict_raw_report_entries',
      items: [],
      totals: { reported_bytes: '1000', attributed_bytes: '0', unattributed_bytes: '1000' },
      ranking: { returned_bytes: '0', other_user_bytes: '0' },
      quality: { invalid_report_count: 0, invalid_entry_count: 0 },
    }
    const view = rankView(userTrafficSchema.parse(allUnattRaw))
    expect(view.kind).toBe('users')
    expect(view.empty).toBe(true)
    expect(view.allUnattributed).toBe(true)
    expect(view.footer).toBe('合计 0 B')
    expect(view.more).toEqual({
      label: '全部用户',
      target: { module: 'users', tab: 'list' },
    })
  })
})
