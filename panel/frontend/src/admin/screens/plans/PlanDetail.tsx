/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/format 的 formatBytes / formatCount / formatDateTime，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / ConfirmModal / QueryView / StatStrip / Tag / useToast，依赖 ../../actions 的 useCan / useIntentKey，依赖 ./api 的 usePlan / useInvalidatePlans / rowVersionSchema / PlanDetail / PlanRow，依赖 ./model，依赖 ./failure 的 useCatalogFailure，依赖 ./PriceCard、./PoolCard、./Versions、./SalesDrawer，依赖 ./Plans.module.css
 * [OUTPUT]: 对外提供 PlanDetailView
 * [POS]: 套餐详情（后台-04 右侧）：头部（名称、状态、说明、「用向导编辑」「销售设置」「归档套餐」）、四格事实（有效订阅取列表行的 active_subscriptions，详情接口没有）、提示条（node_count = 0 的「买了也是空订阅」、停止新购、上架时间窗），下面是价格 / 线路两卡与版本列表。D-C-1 已决（5.A.2）不做「恢复上架」：归档写明不可恢复，只想暂停售卖的引导去销售设置
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { formatBytes, formatCount, formatDateTime } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, QueryView, StatStrip, Tag, useToast } from '../../../ui'
import { useCan, useIntentKey } from '../../actions'
import { rowVersionSchema, useInvalidatePlans, usePlan, type PlanDetail, type PlanRow } from './api'
import { useCatalogFailure } from './failure'
import { PLAN_STATUS_VIEW, trafficOf, VISIBILITY_LABELS } from './model'
import css from './Plans.module.css'
import { PoolCard } from './PoolCard'
import { PriceCard } from './PriceCard'
import { SalesDrawer } from './SalesDrawer'
import { Versions } from './Versions'

export function PlanDetailView({ id, row, onEdit }: { id: string; row: PlanRow | undefined; onEdit: (plan: PlanDetail) => void }) {
  const detail = usePlan(id)
  return (
    <QueryView query={detail} rows={4} empty={null}>
      {(plan) => <Loaded plan={plan} row={row} onEdit={() => onEdit(plan)} />}
    </QueryView>
  )
}

function Loaded({ plan, row, onEdit }: { plan: PlanDetail; row: PlanRow | undefined; onEdit: () => void }) {
  const can = useCan()
  const publish = can('catalog.publish')
  const archived = plan.status === 'archived'
  const [archiving, setArchiving] = useState(false)
  const [sales, setSales] = useState(false)
  const view = PLAN_STATUS_VIEW[plan.status]
  const current = plan.versions.find((v) => v.id === plan.current_version_id)
  const shown = current ?? plan.versions[0]
  const traffic = shown ? trafficOf(shown.quotas) : null

  const notes: Array<{ text: string; info?: boolean }> = []
  // node_count 只数当前发布版本的池；从没发布过的草稿不适用，发布时后端会拦「池里没有可服务节点」
  if (!archived && row && row.version !== null && row.node_count === 0)
    notes.push({ text: `当前版本没有在线节点：买了这个套餐的用户会拿到一份空订阅。${publish ? '在「线路 · 节点池」里绑定有在线节点的池。' : ''}` })
  if (!archived && !plan.allow_new_purchase) notes.push({ text: '已关闭新购：门户看得到也买不了，已有订阅照常续费。', info: true })
  if (!archived && plan.visibility !== 'public') notes.push({ text: `可见范围：${VISIBILITY_LABELS[plan.visibility]}。`, info: true })
  if (plan.visible_from || plan.visible_until)
    notes.push({ text: `上架时间窗：${plan.visible_from ? formatDateTime(plan.visible_from) : '不限'} 至 ${plan.visible_until ? formatDateTime(plan.visible_until) : '不限'}。`, info: true })

  return (
    <>
      <section className={css.head} aria-label="套餐概况">
        <div className={css.headText}>
          <div className={css.headTitle}>
            <h2>{plan.name}</h2>
            <Tag tone={view.tone}>{view.label}</Tag>
            <span className={css.cardCode}>{plan.code}</span>
          </div>
          <p className={css.desc}>{plan.description || '没有说明'}</p>
        </div>
        {publish && !archived && (
          <div className={css.headActions}>
            <Button size="sm" variant="outline" onClick={onEdit}>
              用向导编辑
            </Button>
            <Button size="sm" variant="outline" onClick={() => setSales(true)}>
              销售设置
            </Button>
            <Button size="sm" variant="outline" className={css.dangerText} onClick={() => setArchiving(true)}>
              归档套餐
            </Button>
          </div>
        )}
        {archived && <p className={`${css.small} ${css.headWide}`}>已归档：门户不再展示、不能新购，已有订阅照常续费到期。归档不可恢复。</p>}
        <div className={css.headWide}>
          <StatStrip
            label="套餐事实"
            items={[
              { label: '有效订阅', value: row ? formatCount(row.active_subscriptions) : '—' },
              { label: '当前版本', value: current ? `v${current.version}` : '未发布' },
              { label: '每周期流量', value: shown ? (traffic === null ? '不限' : formatBytes(traffic)) : '—' },
              { label: '设备上限', value: shown ? (shown.max_devices === null ? '不限' : String(shown.max_devices)) : '—' },
            ]}
          />
        </div>
      </section>

      {notes.map((n) => (
        <p key={n.text} className={`${css.note} ${n.info ? css.noteInfo : ''}`}>
          {n.text}
        </p>
      ))}

      <div className={css.cards}>
        <PriceCard plan={plan} />
        <PoolCard plan={plan} />
      </div>
      <Versions plan={plan} />

      <ArchiveDialog plan={plan} open={archiving} onClose={() => setArchiving(false)} />
      <SalesDrawer plan={plan} open={sales} onClose={() => setSales(false)} />
    </>
  )
}

/** POST v1/plans/{id}/archive：catalog.publish + reauth + 幂等；不可逆（库里触发器只许 active / draft → archived） */
function ArchiveDialog({ plan, open, onClose }: { plan: PlanDetail; open: boolean; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useCatalogFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidatePlans()
  const confirm = async () => {
    const body = { expected_row_version: plan.row_version }
    try {
      await api.post(`v1/plans/${encodeURIComponent(plan.id)}/archive`, rowVersionSchema, { body, idempotencyKey: intent.keyFor([plan.id, body]) })
      intent.reset()
      toast(`「${plan.name}」已归档`)
      onClose()
    } catch (e) {
      fail(e, { intent })
    } finally {
      void invalidate()
    }
  }
  return (
    <ConfirmModal
      open={open}
      title={`归档「${plan.name}」？`}
      body="门户不再展示、不能再新购，已有订阅照常续费到期。归档后不能恢复上架；只想暂停售卖，请改用「销售设置」关闭新购或隐藏。"
      confirmLabel="归档"
      tone="danger"
      onCancel={onClose}
      onConfirm={confirm}
    />
  )
}
