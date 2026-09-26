/**
 * [INPUT]: 依赖 ../../../core/router 的 navigate，依赖 ../../../ui 的 Drawer / Tabs / Tag，依赖 ./logic 的 addressLabel / heartbeatLabel / nodeState / protocolLabel，依赖 ./NodeForm、./NodeMonitor、./NodeRouting、./NodeIdentity、./NodeOps，依赖 ./schemas 的 NodeRow，依赖 ./nodes.module.css
 * [OUTPUT]: 对外提供 NodeDrawer、DRAWER_TABS、DrawerTab
 * [POS]: admin/screens/nodes 的节点详情抽屉（设计稿 aside）：头部国家、名称、状态、协议 · 服务器 · 心跳，delivered_to_users=false 时给出 delivery_note（契约待补·前端）；五个标签监控 / 协议参数 / 路由 / 身份与令牌 / 操作，标签记在地址 #/nodes/nodes/<节点 id>/<标签>，刷新与分享落在同一处
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { navigate } from '../../../core/router'
import { Drawer, Tabs, Tag } from '../../../ui'
import { addressLabel, heartbeatLabel, nodeState, protocolLabel } from './logic'
import { NodeForm } from './NodeForm'
import { NodeIdentity } from './NodeIdentity'
import { NodeMonitor } from './NodeMonitor'
import { NodeOps } from './NodeOps'
import { NodeRouting } from './NodeRouting'
import css from './nodes.module.css'
import type { NodeRow } from './schemas'

export const DRAWER_TABS = [
  ['metrics', '监控'],
  ['proto', '协议参数'],
  ['routing', '路由'],
  ['identity', '身份与令牌'],
  ['ops', '操作'],
] as const
export type DrawerTab = (typeof DRAWER_TABS)[number][0]

export function NodeDrawer({ node, tab, onClose }: { node: NodeRow | null; tab: DrawerTab; onClose: () => void }) {
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
          {tab === 'proto' && <NodeForm key={node.row_version} node={node} onSaved={() => undefined} />}
          {tab === 'routing' && <NodeRouting nodeId={node.id} />}
          {tab === 'identity' && <NodeIdentity node={node} />}
          {tab === 'ops' && <NodeOps node={node} onGone={onClose} />}
        </div>
      )}
    </Drawer>
  )
}
