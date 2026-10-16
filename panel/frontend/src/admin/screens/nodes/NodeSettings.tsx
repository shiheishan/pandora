import { useState } from 'react'
import { Button, Modal, QueryView, Tag } from '../../../ui'
import { NodeAccess } from './NodeAccess'
import css from './detail.module.css'
import { ruleSummary } from './logic'
import { NodeRouting } from './NodeRouting'
import { NodeTraffic } from './NodeTraffic'
import { useNodeRouting } from './queries'
import type { NodeRow } from './schemas'
import { trafficPercent, trafficUsageLabel } from './xb'

type EditKind = 'routing' | 'traffic' | 'access' | null

export function NodeSettings({ node, baseRate }: { node: NodeRow; baseRate: number | null }) {
  const routing = useNodeRouting(node.id)
  const [edit, setEdit] = useState<EditKind>(null)
  const retired = node.serving_status === 'retired'

  const close = () => setEdit(null)

  return (
    <div className={css.stack}>
      <section className={css.section}>
        <div className={css.sectionHead}>
          <h4 className={css.sectionTitle}>路由</h4>
          <Button size="sm" disabled={retired} onClick={() => setEdit('routing')}>
            编辑
          </Button>
        </div>
        <QueryView query={routing} rows={3} isEmpty={() => false} empty={null}>
          {(d) => {
            const groupNames = d.groups.map((g) => g.name)
            const outboundTags = d.outbounds.map((o) => o.tag)
            const rules = d.routes.filter((r) => r.enabled)
            const rulePreview = rules.slice(0, 3).map((r) => ruleSummary(r)).join('、')
            return (
              <div className={css.rowTable}>
                <div className={css.infoRow}>
                  <span className={css.infoKey}>路由组</span>
                  <span>{groupNames.length ? groupNames.join('、') : '未加入路由组'}</span>
                  <span />
                </div>
                <div className={css.infoRow}>
                  <span className={css.infoKey}>出站</span>
                  <span className={css.mono}>{outboundTags.length ? outboundTags.join('、') : '沿用路由组与全局'}</span>
                  <span />
                </div>
                <div className={css.infoRow}>
                  <span className={css.infoKey}>分流规则</span>
                  <span>
                    {rules.length ? `${rules.length} 条 · ${rulePreview}${rules.length > 3 ? '…' : ''}` : '沿用路由组与全局规则'}
                  </span>
                  <span />
                </div>
              </div>
            )
          }}
        </QueryView>
        <p className={css.sectionNote}>节点私有规则排在所在路由组与全局规则之前；全局规则在「路由」页签维护。</p>
      </section>

      <section className={css.section}>
        <div className={css.sectionHead}>
          <h4 className={css.sectionTitle}>流量与标签</h4>
          <Button size="sm" disabled={retired} onClick={() => setEdit('traffic')}>
            编辑
          </Button>
        </div>
        <div className={css.rowTable}>
          <div className={css.infoRow}>
            <span className={css.infoKey}>本周期用量</span>
            <div className={css.infoVal}>
              <span className={css.valLine}>
                {node.traffic.limit_bytes === null ? '不限量' : trafficUsageLabel(node.traffic)}
                {node.traffic.exceeded_at && <Tag tone="danger">超限</Tag>}
              </span>
              {node.traffic.limit_bytes != null && (() => {
                const pct = trafficPercent(node.traffic)
                if (pct == null) return null
                return (
                  <span className={css.progress}>
                    <span className={`${css.progressFill} ${pct >= 90 ? css.progressWarn : ''}`} style={{ width: `${pct}%` }} />
                  </span>
                )
              })()}
            </div>
            <span />
          </div>
          <div className={css.infoRow}>
            <span className={css.infoKey}>计费倍率</span>
            <span>
              {baseRate == null ? '—' : `${baseRate}×`}
              {node.traffic_rate_schedule.length > 0 ? ` · ${node.traffic_rate_schedule.length} 个时段倍率` : ''}
            </span>
            <span />
          </div>
          <div className={css.infoRow}>
            <span className={css.infoKey}>标签</span>
            {node.tags.length ? (
              <span className={css.chips}>
                {node.tags.map((t) => (
                  <span key={t} className={css.chip}>{t}</span>
                ))}
              </span>
            ) : (
              <span>—</span>
            )}
            <span />
          </div>
        </div>
        <p className={css.sectionNote}>超出上限后节点停止下发，下个周期自动恢复。</p>
      </section>

      <section className={css.section}>
        <div className={css.sectionHead}>
          <h4 className={css.sectionTitle}>中转与 ECH</h4>
          <Button size="sm" disabled={retired} onClick={() => setEdit('access')}>
            编辑
          </Button>
        </div>
        <div className={css.rowTable}>
          <div className={css.infoRow}>
            <span className={css.infoKey}>中转</span>
            <span>{node.relay_count === 0 ? '无' : `${node.relay_count} 条`}</span>
            <span />
          </div>
          <div className={css.infoRow}>
            <span className={css.infoKey}>ECH</span>
            <span>{node.ech_enabled ? '已开启' : '未开启'}</span>
            <span />
          </div>
        </div>
        <p className={css.sectionNote}>ECH 需要客户端支持；不支持的客户端会回退到普通 TLS。</p>
      </section>

      <Modal open={edit === 'routing'} onClose={close} size={760} tall divided title={`编辑路由 · ${node.name}`} bodyClassName={css.modalBody}>
        <div className={css.scroll}>
          <NodeRouting nodeId={node.id} />
        </div>
      </Modal>
      <Modal open={edit === 'traffic'} onClose={close} size={640} tall divided title={`编辑流量与标签 · ${node.name}`} bodyClassName={css.modalBody}>
        <div className={css.scroll}>
          <NodeTraffic node={node} baseRate={baseRate} />
        </div>
      </Modal>
      <Modal open={edit === 'access'} onClose={close} size={760} tall divided title={`编辑中转与 ECH · ${node.name}`} bodyClassName={css.modalBody}>
        <div className={css.scroll}>
          <NodeAccess node={node} />
        </div>
      </Modal>
    </div>
  )
}
