import { navigate } from '../../../core/router'
import { Empty, Modal, QueryView, Tabs, Tag, type QueryLike } from '../../../ui'
import { addressLabel, nodeState, protocolLabel } from './logic'
import css from './detail.module.css'
import { NodeForm } from './NodeForm'
import { NodeIdentity } from './NodeIdentity'
import { NodeMonitor } from './NodeMonitor'
import { NodeHeaderAction, NodeOps } from './NodeOps'
import { NodeSettings } from './NodeSettings'
import type { NodeDetail, NodeRow } from './schemas'

export const DRAWER_TABS = [
  ['metrics', '概览'],
  ['proto', '协议参数'],
  ['settings', '路由与流量'],
  ['ops', '身份与操作'],
] as const
export type DrawerTab = (typeof DRAWER_TABS)[number][0]

function statusTone(tone: string): 'ok' | 'danger' | 'neutral' {
  if (tone === 'ok') return 'ok'
  if (tone === 'danger') return 'danger'
  return 'neutral'
}

/**
 * node 是列表行（或列表没加载到时的单取结果）；detail 是同一个节点的单取查询（GET v1/nodes?id=），
 * 带着列表不回的编辑字段，协议表单要等它到了再画
 */
export function NodeDrawer({ node, detail, tab, onClose }: { node: NodeRow | null; detail: QueryLike<NodeDetail | null>; tab: DrawerTab; onClose: () => void }) {
  const state = node ? nodeState(node) : null

  return (
    <Modal
      open={node !== null}
      onClose={onClose}
      size={760}
      tall
      divided
      className={css.detailModal}
      bodyClassName={css.modalBody}
      headExtra={node && <NodeHeaderAction node={node} />}
      title={
        node && (
          <span className={css.titleWrap}>
            {node.country_code && <span className={css.cc}>{node.country_code}</span>}
            <span className={css.titleName}>{node.name}</span>
            {state && (
              <span className={css.statusTags}>
                <Tag tone={statusTone(state.tone)}>{state.label}</Tag>
                {state.note === '草稿' && <Tag tone="neutral">草稿</Tag>}
              </span>
            )}
          </span>
        )
      }
      subtitle={
        node && (
          <>
            <span className={css.subMono}>
              {addressLabel(node)} · {protocolLabel(node.node_type)} · {node.server_name ?? '未绑定服务器'}
            </span>
            {!node.delivered_to_users && node.delivery_note && <span className={css.deliveryNote}>不下发给用户：{node.delivery_note}</span>}
          </>
        )
      }
    >
      {node && (
        <>
          <div className={css.tabsBar}>
            <Tabs label="节点详情" items={DRAWER_TABS.map(([value, label]) => ({ value, label }))} value={tab} onChange={(t) => navigate(`/nodes/nodes/${node.id}/${t}`, { replace: true })} />
          </div>
          <div className={css.scroll} key={`${node.id}-${tab}`}>
            {tab === 'metrics' && <NodeMonitor node={node} />}
            {tab === 'proto' && (
              <QueryView query={detail} rows={4} isEmpty={(d) => d === null} empty={<Empty bare title="节点不存在" description="它可能刚被删除，关掉弹窗刷新列表看看。" />}>
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
            {tab === 'settings' && <NodeSettings node={node} baseRate={detail.data?.traffic_rate ?? null} />}
            {tab === 'ops' && (
              <div className={css.stack}>
                <NodeIdentity node={node} />
                <NodeOps node={node} onGone={onClose} />
              </div>
            )}
          </div>
        </>
      )}
    </Modal>
  )
}
