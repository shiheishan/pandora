import { formatBytes, relativeTime } from '../../../core/format'
import type { NodeTraffic, UserTraffic } from './api'

export type RankSort = 'total' | 'up' | 'name'
export const RANK_SORT_LABEL: Readonly<Record<RankSort, string>> = { total: '按流量', up: '按上行占比', name: '按名称' }
export const NEXT_SORT: Readonly<Record<RankSort, RankSort>> = { total: 'up', up: 'name', name: 'total' }

export interface RankRow {
  key: string            // node_id / user_id
  name: string           // 节点：display_name ?? name；用户：email_masked
  initials: string       // 节点：name 前两个字符转大写；用户：第一个字符转大写
  userId?: string        // 只有用户行有（点进用户详情）
  value: string          // formatBytes(total_bytes)
  share: string          // `占 ${pct}%`，pct = total_bytes / 分母，保留 1 位小数（分母为 0 时 '占 0.0%'）
  widthPct: number       // 条宽 0–100：total_bytes / axis * 100，axis = 第一名(按流量最大) * 1.12；保留 2 位小数；最小 1（有流量时）
  delta: string          // 与均值的差：`+${formatBytes(差)}` 或 `−${formatBytes(|差|)}`（数学减号 U+2212）；差为 0 写 `±0 B` 的话用 `+${formatBytes(0n)}`
  above: boolean         // total_bytes >= 均值
  upPct: number          // 上行占比整数百分比：upload_bytes*100/total_bytes 取整（total 为 0 时 0）
  tip: { title: string; main: string; sub: string }
  // title = name；main = `${value} · ${share}`；
  // sub = [`下行 ${100-upPct}% · 上行 ${upPct}%`, `${above ? '高于' : '低于'}均值 ${formatBytes(|差|)}`, 用户行加 `${subscription_count} 个订阅`, `最近上报 ${relativeTime(last_report_at, now)}`].join(' · ')
}

export interface RankView {
  rows: RankRow[]        // 已按 sort 排好
  empty: boolean         // items 为空
  allUnattributed: boolean // items 为空 && totals.reported_bytes !== '0' && totals.attributed_bytes === '0'
  avgPct: number         // 均值竖线位置 0–100：均值 / axis * 100，保留 2 位小数；没有行时 0
  total: { label: string; sub: string; value: string; of: string }
  // label = 节点 `前 ${n} 个节点`，用户 `前 ${n} 位用户`
  // sub = `均值 ${formatBytes(均值)} · 占全部 ${pct}%`（pct = 列表合计 / 分母，1 位小数）
  // value = formatBytes(列表合计)；of = `共 ${formatBytes(分母)}`
  notes: string[]        // 「数据说明」气泡里的几行，见下
  more: { label: string; module: 'nodes' | 'users'; tab: string } // 节点 {'查看全部节点','nodes','nodes'}；用户 {'查看全部用户','users','list'}
}

function formatSharePct(part: bigint, whole: bigint): string {
  if (whole <= 0n) return '0.0'
  const scaled = (part * 1000n) / whole
  return (Number(scaled) / 10).toFixed(1)
}

/** 「刚刚」「2 分钟前」「10-14」：relativeTime 的分钟、小时补上「前」 */
function ago(at: string, now: Date): string {
  const t = relativeTime(at, now)
  return /(分钟|小时)$/.test(t) ? `${t}前` : t
}

function absBigInt(n: bigint): bigint {
  return n < 0n ? -n : n
}

export function rankView(data: NodeTraffic | UserTraffic, sort: RankSort, now: Date = new Date()): RankView {
  const isNode = data.items.length > 0 ? 'node_id' in data.items[0]! : 'other_node_bytes' in data.ranking

  const denominator = BigInt(isNode ? data.totals.reported_bytes : data.totals.attributed_bytes)
  const items = data.items

  const empty = items.length === 0
  const allUnattributed = empty && data.totals.reported_bytes !== '0' && data.totals.attributed_bytes === '0'

  let listSum = 0n
  let maxTotal = 0n
  for (const item of items) {
    const total = BigInt(item.total_bytes)
    listSum += total
    if (total > maxTotal) {
      maxTotal = total
    }
  }

  const count = BigInt(items.length)
  const avg = count > 0n ? listSum / count : 0n

  const axis = (Number(maxTotal) * 1.12) || 0

  const avgPct = axis > 0 ? Number(((Number(avg) / axis) * 100).toFixed(2)) : 0

  interface RowInternal {
    row: RankRow
    totalBytes: bigint
    orderIndex: number
  }

  const internalRows: RowInternal[] = items.map((item, index) => {
    const totalBytes = BigInt(item.total_bytes)
    const uploadBytes = BigInt(item.upload_bytes)

    let key: string
    let name: string
    let initials: string
    let userId: string | undefined
    let subCountPart: string | undefined

    if ('node_id' in item) {
      key = item.node_id
      name = item.display_name ?? item.name
      initials = item.name.slice(0, 2).toUpperCase()
    } else {
      key = item.user_id
      name = item.email_masked
      initials = item.email_masked.slice(0, 1).toUpperCase()
      userId = item.user_id
      subCountPart = `${item.subscription_count} 个订阅`
    }

    const value = formatBytes(totalBytes)
    const sharePct = formatSharePct(totalBytes, denominator)
    const share = `占 ${sharePct}%`

    let widthPct = 0
    if (axis > 0) {
      widthPct = Number(((Number(totalBytes) / axis) * 100).toFixed(2))
      if (totalBytes > 0n && widthPct < 1) {
        widthPct = 1
      }
    }

    const deltaVal = totalBytes - avg
    const delta = deltaVal < 0n ? `−${formatBytes(absBigInt(deltaVal))}` : `+${formatBytes(deltaVal)}`
    const above = totalBytes >= avg

    const upPct = totalBytes > 0n ? Number((uploadBytes * 100n) / totalBytes) : 0

    const subParts = [
      `下行 ${100 - upPct}% · 上行 ${upPct}%`,
      `${above ? '高于' : '低于'}均值 ${formatBytes(absBigInt(deltaVal))}`,
      ...(subCountPart ? [subCountPart] : []),
      `最近上报 ${ago(item.last_report_at, now)}`,
    ]

    const tip = {
      title: name,
      main: `${value} · ${share}`,
      sub: subParts.join(' · '),
    }

    const row: RankRow = {
      key,
      name,
      initials,
      userId,
      value,
      share,
      widthPct,
      delta,
      above,
      upPct,
      tip,
    }

    return {
      row,
      totalBytes,
      orderIndex: index,
    }
  })

  if (sort === 'total') {
    internalRows.sort((a, b) => {
      if (a.totalBytes > b.totalBytes) return -1
      if (a.totalBytes < b.totalBytes) return 1
      return a.orderIndex - b.orderIndex
    })
  } else if (sort === 'up') {
    internalRows.sort((a, b) => {
      if (a.row.upPct !== b.row.upPct) {
        return b.row.upPct - a.row.upPct
      }
      if (a.totalBytes > b.totalBytes) return -1
      if (a.totalBytes < b.totalBytes) return 1
      return a.orderIndex - b.orderIndex
    })
  } else if (sort === 'name') {
    internalRows.sort((a, b) => a.row.name.localeCompare(b.row.name, 'zh-Hans-CN'))
  }

  const sortedRows = internalRows.map((r) => r.row)

  const n = items.length
  const totalLabel = isNode ? `前 ${n} 个节点` : `前 ${n} 位用户`
  const sumSharePct = formatSharePct(listSum, denominator)
  const totalSub = `均值 ${formatBytes(avg)} · 占全部 ${sumSharePct}%`
  const totalValue = formatBytes(listSum)
  const totalOf = `共 ${formatBytes(denominator)}`

  const notes: string[] = ['朱红竖线为当前列表的均值']
  if (data.totals.unattributed_bytes !== '0') {
    notes.push(`未归属 ${formatBytes(data.totals.unattributed_bytes)}：节点已上报、但找不到对应用户的流量`)
  }
  if (data.quality.invalid_report_count > 0) {
    notes.push(`无效上报 ${data.quality.invalid_report_count}：整份上报格式不对，未计入`)
  }
  if (data.quality.invalid_entry_count > 0) {
    notes.push(`无效条目 ${data.quality.invalid_entry_count}：上报数据不完整，未计入排行`)
  }
  notes.push('数据来自节点原始上报，不是计费账本，与用户扣量可能有出入')

  const more = isNode
    ? { label: '查看全部节点', module: 'nodes' as const, tab: 'nodes' }
    : { label: '查看全部用户', module: 'users' as const, tab: 'list' }

  return {
    rows: sortedRows,
    empty,
    allUnattributed,
    avgPct,
    total: {
      label: totalLabel,
      sub: totalSub,
      value: totalValue,
      of: totalOf,
    },
    notes,
    more,
  }
}
