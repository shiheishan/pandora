import { navigate } from '../../../core/router'
import { Drawer, Empty, QueryView, Tabs, Tag, type QueryLike } from '../../../ui'
import { addressLabel, heartbeatLabel, nodeState, protocolLabel } from './logic'
import { NodeForm } from './NodeForm'
import { NodeIdentity } from './NodeIdentity'
import { NodeMonitor } from './NodeMonitor'
import { NodeOps } from './NodeOps'
import { NodeRouting } from './NodeRouting'
import css from './nodes.module.css'
import type { NodeDetail, NodeRow } from './schemas'

export const DRAWER_TABS = [
  ['metrics', '监控'],
  ['proto', '协议参数'],
  ['routing', '路由'],
  ['identity', '身份与令牌'],
  ['ops', '操作'],
] as const
export type DrawerTab = (typeof DRAWER_TABS)[number][0]

/**
 * node 是列表行（或列表没加载到时的单取结果）；detail 是同一个节点的单取查询（GET v1/nodes?id=），
 * 带着列表不回的编辑字段，协议表单要等它到了再画
 */
export function NodeDrawer({ node, detail, tab, onClose }: { node: NodeRow | null; detail: QueryLike<NodeDetail | null>; tab: DrawerTab; onClose: () => void }) {
  const state = node ? nodeState(node) : null
  return (
    <Drawer
      open={node !== null}
      onClose={onClose}
      width={620}
      title={
        node && (
          <span className={css.drawerTitle}>
            {node.country_code && <span className={css.cc}>{node.country_code}</span>}
            {node.name}
            <span className={`${css.mono} ${css.faint}`}>#{node.node_no}</span>
          </span>
        )
      }
      subtitle={
        node &&
        state && (
          <span className={css.drawerSub}>
            <span className={css.state}>
              <span className={`${css.dot} ${css[`dot_${state.tone}`]}`} aria-hidden="true" />
              {state.label}
              {state.note && <Tag tone={state.note === '排空中' ? 'warn' : 'neutral'}>{state.note}</Tag>}
            </span>
            <span className={css.faint}>
              {protocolLabel(node.node_type)} · {node.server_name ?? '未绑定服务器'} · {addressLabel(node)} · 心跳 {heartbeatLabel(node.last_heartbeat_at)}
            </span>
            {!node.delivered_to_users && node.delivery_note && <span className={css.deliveryNote}>不下发给用户：{node.delivery_note}</span>}
          </span>
        )
      }
      toolbar={node && <Tabs label="节点详情" items={DRAWER_TABS.map(([value, label]) => ({ value, label }))} value={tab} onChange={(t) => navigate(`/nodes/nodes/${node.id}/${t}`, { replace: true })} />}
    >
      {node && (
        <div key={`${node.id}-${tab}`}>
          {tab === 'metrics' && <NodeMonitor node={node} />}
          {tab === 'proto' && (
            <QueryView query={detail} rows={4} isEmpty={(d) => d === null} empty={<Empty bare title="节点不存在" description="它可能刚被删除，关掉抽屉刷新列表看看。" />}>
              {(d) =>
                d && (
                  <>
                    {d.warnings?.map((w) => (
                      <div key={w} className={css.configWarning}>
                        <Tag tone="warn">配置提示</Tag> {w}
                      </div>
                    ))}
                    <NodeForm key={d.row_version} node={d} onSaved={() => undefined} />
                  </>
                )
              }
            </QueryView>
          )}
          {tab === 'routing' && <NodeRouting nodeId={node.id} />}
          {tab === 'identity' && <NodeIdentity node={node} />}
          {tab === 'ops' && <NodeOps node={node} onGone={onClose} />}
        </div>
      )}
    </Drawer>
  )
}
