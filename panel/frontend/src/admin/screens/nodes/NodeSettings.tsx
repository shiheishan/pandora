import { PencilEdit01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { useState } from 'react'
import { KvList, KvRow, SectionTitle } from '../../kit'
import { formatBytes } from '../../../core/format'
import { Button, Modal, QueryView } from '../../../ui'
import css from './detail.module.css'
import { ruleSummary } from './logic'
import { NodeAccess } from './NodeAccess'
import { NodeRouting } from './NodeRouting'
import { NodeTraffic } from './NodeTraffic'
import { useNodeRouting } from './queries'
import type { NodeRow } from './schemas'

type EditKind = 'routing' | 'traffic' | 'access' | null

/** 「设置」页签：路由、流量与标签、访问三块只读摘要，「编辑」各开一个弹窗装现成的编辑器 */
export function NodeSettings({ node, baseRate }: { node: NodeRow; baseRate: number | null }) {
  const routing = useNodeRouting(node.id)
  const [edit, setEdit] = useState<EditKind>(null)
  const retired = node.serving_status === 'retired'

  const close = () => setEdit(null)

  return (
    <div className={css.stack}>
      <section>
        <SectionTitle
          action={
            <Button size="sm" icon={PencilEdit01Icon} disabled={retired} onClick={() => setEdit('routing')}>
              编辑
            </Button>
          }
        >
          路由
        </SectionTitle>
        <QueryView query={routing} rows={3} isEmpty={() => false} empty={null}>
          {(d) => {
            const groupNames = d.groups.map((g) => g.name)
            const outboundTags = d.outbounds.map((o) => o.tag)
            const rules = d.routes.filter((r) => r.enabled)
            const rulePreview = rules.slice(0, 3).map((r) => ruleSummary(r)).join('、')
            return (
              <KvList boxed>
                <KvRow label="所属路由组">{groupNames.length ? groupNames.join('、') : '未加入路由组'}</KvRow>
                <KvRow label="节点私有出站">
                  {outboundTags.length ? (
                    <span className={css.mono}>{outboundTags.join('、')}</span>
                  ) : (
                    <span className={css.faint}>沿用路由组与全局</span>
                  )}
                </KvRow>
                <KvRow label="节点私有规则">
                  {rules.length ? (
                    `${rules.length} 条 · ${rulePreview}${rules.length > 3 ? '…' : ''}`
                  ) : (
                    <>
                      0 条<span className={css.faint}> · 按路由组与全局规则走</span>
                    </>
                  )}
                </KvRow>
              </KvList>
            )
          }}
        </QueryView>
      </section>

      <section>
        <SectionTitle
          action={
            <Button size="sm" icon={PencilEdit01Icon} disabled={retired} onClick={() => setEdit('traffic')}>
              编辑
            </Button>
          }
        >
          流量与标签
        </SectionTitle>
        <KvList boxed>
          <KvRow label="流量上限">{node.traffic.limit_bytes === null ? '不限' : formatBytes(node.traffic.limit_bytes)}</KvRow>
          <KvRow label="重置日">{node.traffic.reset_day != null ? `每月 ${node.traffic.reset_day} 日` : '不自动重置'}</KvRow>
          <KvRow label="计费倍率">
            {baseRate == null ? '—' : `基础 ${baseRate}×`}
            {node.traffic_rate_schedule.length > 0 && (
              <span className={css.faint}>
                {' '}
                · {node.traffic_rate_schedule.length} 段时段倍率，如 {node.traffic_rate_schedule[0]!.start}–
                {node.traffic_rate_schedule[0]!.end} ×{node.traffic_rate_schedule[0]!.rate}
              </span>
            )}
          </KvRow>
          <KvRow label="标签">{node.tags.length ? node.tags.join('、') : '—'}</KvRow>
        </KvList>
      </section>

      <section>
        <SectionTitle
          action={
            <Button size="sm" icon={PencilEdit01Icon} disabled={retired} onClick={() => setEdit('access')}>
              编辑
            </Button>
          }
        >
          访问
        </SectionTitle>
        <KvList boxed>
          <KvRow label="节点池">
            {node.pool_name ?? (
              <>
                <span className={css.faint}>未划入节点池</span>
                <span className={css.faint}> · 不服务任何用户</span>
              </>
            )}
          </KvRow>
          <KvRow label="中转">{node.relay_count === 0 ? '无' : `${node.relay_count} 条`}</KvRow>
          <KvRow label="ECH">{node.ech_enabled ? '已开启' : '未开启'}</KvRow>
        </KvList>
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
      <Modal open={edit === 'access'} onClose={close} size={760} tall divided title={`编辑访问 · ${node.name}`} bodyClassName={css.modalBody}>
        <div className={css.scroll}>
          <NodeAccess node={node} />
        </div>
      </Modal>
    </div>
  )
}
