import { href, navigate, useHashLocation } from '../../../core/router'
import { formatBytes } from '../../../core/format'
import { Empty, QueryView, Tag } from '../../../ui'
import { DetailPage, SideFact, SideFacts, StatusDot, type StatusTone } from '../../kit'
import css from './detail.module.css'
import { addressLabel, nodeState, protocolLabel, REGIONS } from './logic'
import { NodeIdentity } from './NodeIdentity'
import { NodeMonitor } from './NodeMonitor'
import { NodeHeaderAction, NodeOps } from './NodeOps'
import { NodeProtocol } from './NodeProtocol'
import { NodeSettings } from './NodeSettings'
import { runState } from './runtime'
import { useNodeDetail, useNodes } from './queries'
import type { NodeRow } from './schemas'
import { trafficUsageLabel } from './xb'

export const DETAIL_TABS = [
  ['metrics', '概览'],
  ['proto', '协议参数'],
  ['settings', '路由与流量'],
  ['ops', '身份与操作'],
] as const
export type DetailTab = (typeof DETAIL_TABS)[number][0]

/** nodeState 的 tone（ok / danger / 其余）落到 StatusDot 的 tone */
function statusDotTone(tone: string): StatusTone {
  if (tone === 'ok') return 'ok'
  if (tone === 'danger') return 'bad'
  return 'off'
}

/** REGIONS 里按两位代码找中文地区名（REGIONS 的名称形如「HK · 香港」，取「·」后半），认不出显示原代码 */
function regionLabel(code: string | null): string {
  if (!code) return '—'
  const label = REGIONS.find(([c]) => c === code)?.[1]
  return label ? (label.split(' · ')[1] ?? label) : code
}

/**
 * node 是列表行（列表里有就秒出），列表没加载到时用单取结果 detail 兜底；
 * detail 同时给协议页签带编辑字段。「路由与流量」的编辑器要 traffic_rate，从 detail 取
 */
export function NodeDetailPage({ id, tab }: { id: string; tab: DetailTab }) {
  const list = useNodes()
  const detail = useNodeDetail(id)
  const location = useHashLocation()
  // 列表页带来的筛选查询串原样带着走，返回时还原
  const listQuery = Object.fromEntries(location.query)
  const back = { href: href('/nodes/nodes', listQuery), label: '节点' }
  // 以单条详情为准；它还没回来时先用列表缓存里的这一行秒出
  const row: NodeRow | null = detail.data ?? list.data?.nodes.find((n) => n.id === id) ?? null

  if (!row) {
    return (
      <DetailPage back={back} title="节点详情">
        <QueryView query={detail} rows={4} isEmpty={(d) => d === null} empty={<Empty title="不存在或已删除" description="这个节点可能刚被删除，回到列表刷新看看。" />}>
          {() => null}
        </QueryView>
      </DetailPage>
    )
  }

  const state = nodeState(row)
  const run = runState(row)

  return (
    <DetailPage
      back={back}
      lead={row.country_code ? <span className={css.pageCc}>{row.country_code}</span> : undefined}
      title={row.name}
      meta={
        <>
          <StatusDot tone={statusDotTone(state.tone)}>{state.note === '草稿' ? '草稿' : state.label}</StatusDot>
          <span className={css.subMono}>{addressLabel(row)}</span>
          <span>{protocolLabel(row.node_type)}</span>
          <span>{row.server_name ?? '未绑定服务器'}</span>
        </>
      }
      actions={<NodeHeaderAction node={row} />}
      tabs={{
        label: '节点详情',
        items: DETAIL_TABS.map(([value, label]) => ({ value, label })),
        value: tab,
        onChange: (t) => navigate(`/nodes/nodes/${id}/${t}`, { replace: true, query: listQuery }),
      }}
      side={
        <SideFacts label="节点信息">
          <SideFact label="服务器">
            {row.server_id ? <a href={href(`/nodes/servers/${row.server_id}`)}>{row.server_name ?? '服务器'}</a> : '未绑定'}
          </SideFact>
          <SideFact label="地区">{regionLabel(row.country_code)}</SideFact>
          <SideFact label="节点池">{row.pool_name ?? '未划入节点池'}</SideFact>
          <SideFact label="是否下发">
            {row.delivered_to_users ? (
              <span className={css.okText}>正在下发给用户</span>
            ) : (
              <span className={css.warnText}>不下发：{row.delivery_note}</span>
            )}
          </SideFact>
          <SideFact label="本周期用量">
            {row.traffic.limit_bytes === null ? (
              <>
                {formatBytes(row.traffic.used_bytes)}
                <span className={css.faint}> / 不限</span>
              </>
            ) : (
              trafficUsageLabel(row.traffic)
            )}
            {row.traffic.exceeded_at && <Tag tone="danger">超限</Tag>}
          </SideFact>
          <SideFact label="运行状态">
            {run === null ? '—' : run.label}
            {run !== null && run.detail !== '' && (
              <span className={css.faint}>{run.detail}</span>
            )}
          </SideFact>
        </SideFacts>
      }
    >
      {/* 换页签时子组件状态重置 */}
      <div key={`${id}-${tab}`} className={css.stack}>
        {tab === 'metrics' && <NodeMonitor node={row} />}
        {tab === 'proto' && <NodeProtocol node={row} detail={detail} />}
        {tab === 'settings' && <NodeSettings node={row} baseRate={detail.data?.traffic_rate ?? null} />}
        {tab === 'ops' && (
          <>
            <NodeIdentity node={row} />
            <NodeOps node={row} />
          </>
        )}
      </div>
    </DetailPage>
  )
}
