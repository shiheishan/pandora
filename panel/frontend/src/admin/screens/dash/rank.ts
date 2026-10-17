import { formatBytes } from '../../../core/format'
import type { Target } from './model'
import type { NodeTraffic, UserTraffic } from './api'

export interface RankRow {
  key: string        // node_id / user_id
  name: string       // 节点：display_name ?? name；用户：email_masked
  path: string       // 点进去的地址：节点 `/nodes/nodes/${encodeURIComponent(node_id)}`，用户 `/users/list/${encodeURIComponent(user_id)}`
  value: string      // formatBytes(total_bytes)
  share: string      // 占分母的百分比，一位小数加 %：'22.8%'（分母为 0 时 '0.0%'），用 BigInt 算（同现在的 formatSharePct）
  barPct: number     // 条宽 0–100：total_bytes / 第一名 total_bytes × 100，保留 1 位小数；有流量时最小 4；第一名为 0 时 0
}

export interface RankView {
  kind: 'nodes' | 'users'
  rows: RankRow[]           // 按 total_bytes 从大到小，相等时保持接口顺序
  empty: boolean            // items 为空
  allUnattributed: boolean  // 同现在
  footer: string            // 见下
  more: { label: string; target: Target }  // 节点 { '全部节点', { module: 'nodes', tab: 'nodes' } }；用户 { '全部用户', { module: 'users', tab: 'list' } }
}

function formatSharePct(part: bigint, whole: bigint): string {
  if (whole <= 0n) return '0.0%'
  const scaled = (part * 1000n) / whole
  return `${(Number(scaled) / 10).toFixed(1)}%`
}

export function rankView(
  data: NodeTraffic | UserTraffic,
  nodesOnline?: { online: number; total: number }
): RankView {
  const isNode = data.items.length > 0 ? 'node_id' in data.items[0]! : 'other_node_bytes' in data.ranking
  const kind = isNode ? 'nodes' : 'users'
  const denominator = BigInt(isNode ? data.totals.reported_bytes : data.totals.attributed_bytes)
  const items = data.items

  const empty = items.length === 0
  const allUnattributed = empty && data.totals.reported_bytes !== '0' && data.totals.attributed_bytes === '0'

  interface ItemWithBytes {
    item: (typeof items)[number]
    totalBytes: bigint
    orderIndex: number
  }

  const itemsWithBytes: ItemWithBytes[] = items.map((item, index) => ({
    item,
    totalBytes: BigInt(item.total_bytes),
    orderIndex: index,
  }))

  itemsWithBytes.sort((a, b) => {
    if (a.totalBytes > b.totalBytes) return -1
    if (a.totalBytes < b.totalBytes) return 1
    return a.orderIndex - b.orderIndex
  })

  const maxTotalBytes = itemsWithBytes.length > 0 ? itemsWithBytes[0]!.totalBytes : 0n

  let listSum = 0n
  const rows: RankRow[] = itemsWithBytes.map(({ item, totalBytes }) => {
    listSum += totalBytes

    let key: string
    let name: string
    let path: string

    if ('node_id' in item) {
      key = item.node_id
      name = item.display_name ?? item.name
      path = `/nodes/nodes/${encodeURIComponent(item.node_id)}`
    } else {
      key = item.user_id
      name = item.email_masked
      path = `/users/list/${encodeURIComponent(item.user_id)}`
    }

    const value = formatBytes(totalBytes)
    const share = formatSharePct(totalBytes, denominator)

    let barPct = 0
    if (maxTotalBytes > 0n && totalBytes > 0n) {
      const rawPct = (Number(totalBytes) / Number(maxTotalBytes)) * 100
      barPct = Number(Math.max(4, rawPct).toFixed(1))
    }

    return {
      key,
      name,
      path,
      value,
      share,
      barPct,
    }
  })

  let footer = `合计 ${formatBytes(isNode ? data.totals.reported_bytes : data.totals.attributed_bytes)}`
  if (isNode) {
    if (!empty && nodesOnline != null) {
      footer += ` · ${nodesOnline.online}/${nodesOnline.total} 节点在线`
    }
  } else {
    if (!empty) {
      const sumSharePct = formatSharePct(listSum, denominator)
      footer += ` · 前 ${rows.length} 名占 ${sumSharePct}`
    }
  }

  const more = isNode
    ? { label: '全部节点', target: { module: 'nodes' as const, tab: 'nodes' } }
    : { label: '全部用户', target: { module: 'users' as const, tab: 'list' } }

  return {
    kind,
    rows,
    empty,
    allUnattributed,
    footer,
    more,
  }
}

