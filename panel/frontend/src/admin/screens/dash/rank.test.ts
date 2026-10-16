import { describe, expect, it } from 'vitest'
import { nodeTrafficSchema, userTrafficSchema } from './api'
import { NEXT_SORT, RANK_SORT_LABEL, rankView } from './rank'

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

  const nodeData = nodeTrafficSchema.parse(nodeRaw)
  const now = new Date('2026-10-16T12:00:00.000000Z')

  it('名称、initials 回退逻辑与 userId', () => {
    const view = rankView(nodeData, 'total', now)
    expect(view.rows).toHaveLength(4)

    // display_name 有值时用 display_name，initials 用 name 前两字符大写
    expect(view.rows[0].name).toBe('香港 01')
    expect(view.rows[0].initials).toBe('HK')
    expect(view.rows[0].userId).toBeUndefined()

    // display_name 为 null 时回退到 name
    const sg = view.rows.find((r) => r.key === 'node-3')!
    expect(sg.name).toBe('sg-sin-01')
    expect(sg.initials).toBe('SG')
    expect(sg.userId).toBeUndefined()
  })

  it('占比 share、与均值的差 delta、above 与 tooltip', () => {
    const view = rankView(nodeData, 'total', now)
    // 分母 = 2500 GiB
    // 合计 = 2000 GiB，4 行，均值 = 500 GiB
    // node-1: 1000 GiB. pct = 1000 * 1000 / 2500 = 400 -> 40.0%
    expect(view.rows[0].share).toBe('占 40.0%')
    // 差 = 1000 - 500 = +500 GiB
    expect(view.rows[0].above).toBe(true)
    expect(view.rows[0].delta).toBe('+500 GB')
    expect(view.rows[0].tip.title).toBe('香港 01')
    expect(view.rows[0].tip.main).toBe('1000 GB · 占 40.0%')
    expect(view.rows[0].tip.sub).toContain('高于均值 500 GB')
    expect(view.rows[0].tip.sub).toContain('下行 90% · 上行 10%')
    expect(view.rows[0].tip.sub).toContain('最近上报 2 分钟前')
    // 节点 tip.sub 不应该含「订阅」
    expect(view.rows[0].tip.sub).not.toContain('订阅')

    // node-3: 200 GiB. pct = 200 * 1000 / 2500 = 80 -> 8.0%
    // 差 = 200 - 500 = -300 GiB, 数学减号 U+2212 '−'
    expect(sgRow(view).share).toBe('占 8.0%')
    expect(sgRow(view).above).toBe(false)
    expect(sgRow(view).delta).toBe('−300 GB')
    expect(sgRow(view).tip.sub).toContain('低于均值 300 GB')
  })

  function sgRow(v: ReturnType<typeof rankView>) {
    return v.rows.find((r) => r.key === 'node-3')!
  }

  it('widthPct 与 avgPct（第一名约 89.29%）', () => {
    const view = rankView(nodeData, 'total', now)
    // maxTotal = 1000 GiB
    // axis = 1000 * 1.12 = 1120 GiB
    // 第一名 widthPct = 1000 / 1120 * 100 = 89.2857... -> 89.29
    expect(view.rows[0].widthPct).toBe(89.29)
    // avg = 500 GiB. avgPct = 500 / 1120 * 100 = 44.6428... -> 44.64
    expect(view.avgPct).toBe(44.64)
  })

  it('三种排序的顺序与稳定性', () => {
    // 1. 按流量 total 降序
    const viewTotal = rankView(nodeData, 'total', now)
    expect(viewTotal.rows.map((r) => r.key)).toEqual(['node-1', 'node-2', 'node-3', 'node-4'])

    // 2. 按上行占比 up 降序（up 相同按 total 降序）
    // node-1: 100/1000 = 10%
    // node-2: 300/600 = 50%
    // node-3: 100/200 = 50%
    // node-4: 120/200 = 60%
    // 期望顺序: node-4(60%), node-2(50%, 600G), node-3(50%, 200G), node-1(10%)
    const viewUp = rankView(nodeData, 'up', now)
    expect(viewUp.rows.map((r) => r.key)).toEqual(['node-4', 'node-2', 'node-3', 'node-1'])

    // 3. 按名称 name 中文拼音/locale 升序
    // '东京 01', '洛杉矶 01', 'sg-sin-01', '香港 01'
    const viewName = rankView(nodeData, 'name', now)
    const expectedNameOrder = [...viewName.rows]
      .map((r) => r.name)
      .sort((a, b) => a.localeCompare(b, 'zh-Hans-CN'))
    expect(viewName.rows.map((r) => r.name)).toEqual(expectedNameOrder)

    // axis 与 avgPct 在不同排序下保持不变
    expect(viewUp.avgPct).toBe(viewTotal.avgPct)
    expect(viewName.avgPct).toBe(viewTotal.avgPct)
  })

  it('total 行四个字段与 more', () => {
    const view = rankView(nodeData, 'total', now)
    // 列表合计 2000 GiB，分母 2500 GiB
    // pct = 2000 * 1000 / 2500 = 800 -> 80.0%
    expect(view.total.label).toBe('前 4 个节点')
    expect(view.total.sub).toBe('均值 500 GB · 占全部 80.0%')
    expect(view.total.value).toBe('2000 GB')
    expect(view.total.of).toBe('共 2.44 TB') // 2500 * GiB
    expect(view.more).toEqual({ label: '查看全部节点', module: 'nodes', tab: 'nodes' })
  })

  it('notes 包含数据说明、未归属与无效条目，不含为 0 的无效上报', () => {
    const view = rankView(nodeData, 'total', now)
    expect(view.notes).toEqual([
      '朱红竖线为当前列表的均值',
      '未归属 200 GB：节点已上报、但找不到对应用户的流量',
      '无效条目 3：上报数据不完整，未计入排行',
      '数据来自节点原始上报，不是计费账本，与用户扣量可能有出入',
    ])
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
        email_masked: '***',
        subscription_count: 1,
        upload_bytes: String(20n * GiB),
        download_bytes: String(80n * GiB),
        total_bytes: String(100n * GiB),
        last_report_at: '2026-10-16T11:30:00.000000Z',
      },
      {
        user_id: 'usr-3',
        email_masked: 'z***@example.test',
        subscription_count: 0,
        upload_bytes: '0',
        download_bytes: '0',
        total_bytes: '0',
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

  const userData = userTrafficSchema.parse(userRaw)
  const now = new Date('2026-10-16T12:00:00.000000Z')

  it('用户字段、initials 与 userId 以及 tip.sub', () => {
    const view = rankView(userData, 'total', now)
    expect(view.rows).toHaveLength(3)

    // 用户 1: initials 为首字符大写 'A', userId 保留
    expect(view.rows[0].name).toBe('a***@example.test')
    expect(view.rows[0].initials).toBe('A')
    expect(view.rows[0].userId).toBe('usr-1')
    expect(view.rows[0].tip.sub).toContain('2 个订阅')
    expect(view.rows[0].tip.sub).toContain('最近上报 刚刚')

    // 用户 2: email_masked 为 '***'，initials 为 '*'
    expect(view.rows[1].name).toBe('***')
    expect(view.rows[1].initials).toBe('*')
    expect(view.rows[1].userId).toBe('usr-2')
    expect(view.rows[1].tip.sub).toContain('1 个订阅')

    // 用户 3: 0 流量
    expect(view.rows[2].value).toBe('0 B')
    expect(view.rows[2].share).toBe('占 0.0%')
    expect(view.rows[2].widthPct).toBe(0)
    expect(view.rows[2].upPct).toBe(0)
    expect(view.rows[2].tip.sub).toContain('0 个订阅')

    // total & more
    expect(view.total.label).toBe('前 3 位用户')
    expect(view.total.sub).toBe('均值 100 GB · 占全部 100.0%') // 分母是 attributed_bytes: 300 GiB
    expect(view.total.of).toBe('共 300 GB')
    expect(view.more).toEqual({ label: '查看全部用户', module: 'users', tab: 'list' })
  })

  it('notes 在 unattributed_bytes === 0 时不包含未归属，但包含无效上报', () => {
    const view = rankView(userData, 'total', now)
    expect(view.notes).toEqual([
      '朱红竖线为当前列表的均值',
      '无效上报 2：整份上报格式不对，未计入',
      '数据来自节点原始上报，不是计费账本，与用户扣量可能有出入',
    ])
  })
})

describe('rankView - 差值为 0 的展示（±0 B）', () => {
  it('total_bytes 等于均值时 delta 为 +0 B', () => {
    const singleRaw = {
      range: '24h',
      snapshot_at: '2026-10-16T12:00:00.000000Z',
      from: '2026-10-15T12:00:00.000000Z',
      to: '2026-10-16T12:00:00.000000Z',
      basis: 'strict_raw_report_entries',
      items: [
        {
          node_id: 'n1',
          name: 'hk-1',
          display_name: null,
          upload_bytes: '500',
          download_bytes: '500',
          total_bytes: '1000',
          last_report_at: '2026-10-16T11:58:00.000000Z',
        },
      ],
      totals: {
        reported_bytes: '1000',
        attributed_bytes: '1000',
        unattributed_bytes: '0',
      },
      ranking: { returned_bytes: '1000', other_node_bytes: '0' },
      quality: { invalid_report_count: 0, invalid_entry_count: 0 },
    }
    const view = rankView(nodeTrafficSchema.parse(singleRaw), 'total')
    expect(view.rows[0].delta).toBe('+0 B')
    expect(view.rows[0].above).toBe(true)
  })
})

describe('rankView - 空状态与 allUnattributed', () => {
  it('节点 items 为空时 empty 为 true，判断 node 仍然正确', () => {
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
    const view = rankView(nodeTrafficSchema.parse(emptyRaw), 'total')
    expect(view.empty).toBe(true)
    expect(view.allUnattributed).toBe(false)
    expect(view.avgPct).toBe(0)
    expect(view.total.label).toBe('前 0 个节点')
    expect(view.total.sub).toBe('均值 0 B · 占全部 0.0%')
    expect(view.more.module).toBe('nodes')
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
    const view = rankView(userTrafficSchema.parse(allUnattRaw), 'total')
    expect(view.empty).toBe(true)
    expect(view.allUnattributed).toBe(true)
    expect(view.more.module).toBe('users')
    expect(view.total.label).toBe('前 0 位用户')
  })
})

describe('排序常量与轮转', () => {
  it('RANK_SORT_LABEL 与 NEXT_SORT 轮转正常', () => {
    expect(RANK_SORT_LABEL.total).toBe('按流量')
    expect(RANK_SORT_LABEL.up).toBe('按上行占比')
    expect(RANK_SORT_LABEL.name).toBe('按名称')

    expect(NEXT_SORT.total).toBe('up')
    expect(NEXT_SORT.up).toBe('name')
    expect(NEXT_SORT.name).toBe('total')
  })
})
