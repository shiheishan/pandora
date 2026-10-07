import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Card, QueryView, useToast } from '../../../ui'
import { useCan, useIntentKey } from '../../actions'
import { planUpdatedSchema, poolsBoundSchema, useInvalidatePlans, usePlanPools, type PlanDetail, type PlanPools } from './api'
import { useCatalogFailure } from './failure'
import { updateBody, wizardFromPlan } from './model'
import css from './Plans.module.css'

export function PoolCard({ plan }: { plan: PlanDetail }) {
  const pools = usePlanPools(plan.id)
  return (
    <Card
      flush
      title={
        <>
          线路 · 节点池 <span className={css.cardSub}>订阅可用的节点来自这些池</span>
        </>
      }
    >
      <QueryView query={pools} rows={1} isEmpty={(d) => d.pools.length === 0} empty={<p className={`${css.small} ${css.cardPad}`}>还没有可用的节点池，先到「节点与服务器 · 节点池」建一个。</p>}>
        {(data) => <Binding key={`${data.version_id}:${data.row_version}`} plan={plan} data={data} />}
      </QueryView>
    </Card>
  )
}

function Binding({ plan, data }: { plan: PlanDetail; data: PlanPools }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useCatalogFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidatePlans()
  const initial = data.pools.filter((p) => p.bound).map((p) => p.id)
  const [picked, setPicked] = useState<string[]>(initial)
  const [busy, setBusy] = useState(false)
  const writable = can('catalog.publish') && plan.status !== 'archived' && data.version_id !== ''
  const draft = plan.versions.find((v) => v.id === data.version_id)
  const dirty = picked.length !== initial.length || picked.some((id) => !initial.includes(id))

  const save = async () => {
    setBusy(true)
    try {
      if (data.editable) {
        const body = { version_id: data.version_id, expected_version_row_version: data.row_version, pool_ids: picked }
        const r = await api.post(`v1/plans/${encodeURIComponent(plan.id)}/pools`, poolsBoundSchema, { body, idempotencyKey: intent.keyFor([plan.id, body]) })
        toast(`草稿 v${draft?.version ?? ''} 已绑定 ${r.bound} 个节点池，发布后生效`)
      } else {
        const original = wizardFromPlan(plan)
        const body = updateBody({ ...original, poolIds: picked }, original, plan.row_version)
        const r = await api.put(`v1/plans/${encodeURIComponent(plan.id)}/complete`, planUpdatedSchema, { body, idempotencyKey: intent.keyFor([plan.id, body]) })
        toast(r.changed.join('；') || '节点池绑定已保存')
      }
      intent.reset()
    } catch (e) {
      fail(e, { intent })
    } finally {
      setBusy(false)
      void invalidate()
    }
  }

  const warning = poolBindingWarning(data.pools, picked)

  return (
    <>
      <PoolChips pools={data.pools} picked={picked} onChange={setPicked} disabled={!writable} />
      {warning && (
        <div className={css.cardPad}>
          <p className={css.note} role="status">
            {warning}
          </p>
        </div>
      )}
      {writable && (
        <div className={css.cardFoot}>
          <span className={css.small}>
            {data.editable
              ? `改的是草稿 v${draft?.version ?? ''}，发布后生效`
              : '保存即开新版本并发布：新购立即用新线路，已买的用户续费时切换'}
          </span>
          <span className={css.spacer} />
          <Button size="sm" variant="outline" busy={busy} disabled={!dirty} onClick={() => void save()}>
            保存绑定
          </Button>
        </div>
      )}
    </>
  )
}

/**
 * 按所选节点池的可下发节点数给出「0 节点」提示，没问题时返回 null。
 * 可下发数与订阅下载同一口径（后端 subscription.DeliverableNodeSQL）；池限定了用户组时，
 * 组外用户实际拿到的更少，这里不细分。
 */
export function poolBindingWarning(pools: ReadonlyArray<{ id: string; deliverable_nodes: number }>, picked: readonly string[]): string | null {
  if (picked.length === 0) return '还没绑定节点池：此套餐的订阅里将是 0 个节点。'
  const total = pools.filter((p) => picked.includes(p.id)).reduce((sum, p) => sum + p.deliverable_nodes, 0)
  if (total > 0) return null
  return '所选节点池里没有可下发的节点：此套餐的订阅里将是 0 个节点。检查池里的节点是否在役、上报过心跳、服务器已就绪、协议与地址已配好。'
}

/** 节点池 chip 组：aria-pressed 表示选中；数字优先是可下发节点数，没给就用在线节点数，都没给不画 */
export function PoolChips({
  pools,
  picked,
  onChange,
  disabled = false,
  inline = false,
}: {
  pools: ReadonlyArray<{ id: string; name: string; active_nodes?: number; deliverable_nodes?: number }>
  picked: readonly string[]
  onChange: (next: string[]) => void
  disabled?: boolean
  inline?: boolean
}) {
  const toggle = (id: string) => onChange(picked.includes(id) ? picked.filter((x) => x !== id) : [...picked, id])
  return (
    <div className={`${css.chips} ${inline ? css.inline : ''}`} role="group" aria-label="节点池">
      {pools.map((p) => (
        <button key={p.id} type="button" className={css.chip} aria-pressed={picked.includes(p.id)} disabled={disabled} onClick={() => toggle(p.id)}>
          <span className={css.chipBox} aria-hidden="true" />
          {p.name}
          {p.deliverable_nodes !== undefined ? (
            <span className={css.chipCount} title="可下发节点数：能写进订阅的节点">
              {p.deliverable_nodes}
            </span>
          ) : (
            p.active_nodes !== undefined && (
              <span className={css.chipCount} title="在线节点数">
                {p.active_nodes}
              </span>
            )
          )}
        </button>
      ))}
    </div>
  )
}
