/**
 * [INPUT]: 依赖 react 的 useEffect / useState，依赖 ../../../core/format 的 formatCount，依赖 ../../../core/router 的 href / navigate，依赖 ../../../ui 的 Button / Empty / QueryView / Tag，依赖 ../../actions 的 useCan，依赖 ./api 的 usePlans / PlanDetail / PlanRow，依赖 ./model 的 PLAN_STATUS_VIEW / planFacts，依赖 ./PlanDetail、./Wizard，依赖 ./Plans.module.css
 * [OUTPUT]: 对外提供 CatalogTab
 * [POS]: 套餐页「套餐」标签（#/plans/catalog/<套餐 id>）：左栏「＋ 新建套餐」与套餐卡片（名称 / 代码 / 状态 / 起价 / 流量·设备 / 订阅数，已归档半透明），右侧 PlanDetail；没选中时落到第一张卡。向导（新建 / 编辑）在这一层开合，新建成功后地址跳到新套餐。向导与所有改价、发布入口只对 catalog.publish 显示（5.A D-C-2）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useEffect, useState } from 'react'
import { formatCount } from '../../../core/format'
import { href, navigate } from '../../../core/router'
import { Button, Empty, QueryView, Tag } from '../../../ui'
import { useCan } from '../../actions'
import { usePlans, type PlanDetail, type PlanRow } from './api'
import { PLAN_STATUS_VIEW, planFacts } from './model'
import { PlanDetailView } from './PlanDetail'
import css from './Plans.module.css'
import { Wizard } from './Wizard'

const pathOf = (id: string) => `/plans/catalog/${encodeURIComponent(id)}`

export function CatalogTab({ selected }: { selected: string | null }) {
  const plans = usePlans()
  const can = useCan()
  const [wizard, setWizard] = useState<{ mode: 'new' } | { mode: 'edit'; plan: PlanDetail } | null>(null)
  const first = plans.data?.[0]?.id

  // 没选中时落到第一张卡（replace，不留历史）
  useEffect(() => {
    if (!selected && first) navigate(pathOf(first), { replace: true })
  }, [selected, first])

  const row = plans.data?.find((p) => p.id === selected)

  return (
    <div className={css.catalog}>
      <div className={css.rail}>
        {can('catalog.publish') && (
          <Button variant="primary" block onClick={() => setWizard({ mode: 'new' })}>
            ＋ 新建套餐
          </Button>
        )}
        <QueryView
          query={plans}
          rows={5}
          isEmpty={(d) => d.length === 0}
          empty={<Empty title="还没有套餐" description={can('catalog.publish') ? '用「新建套餐」建第一个，向导里一次填完价格和线路。' : '有发布权限的管理员建好后会出现在这里。'} />}
        >
          {(list) => list.map((p) => <PlanCard key={p.id} plan={p} current={p.id === selected} />)}
        </QueryView>
      </div>

      <div className={css.detail}>
        {selected ? (
          <PlanDetailView id={selected} row={row} onEdit={(plan) => setWizard({ mode: 'edit', plan })} />
        ) : plans.data?.length === 0 ? (
          <Empty title="选择或新建一个套餐" description="套餐的价格、线路与版本都在这里管理。" />
        ) : null}
      </div>

      {wizard && (
        <Wizard
          key={wizard.mode === 'edit' ? wizard.plan.id : 'new'}
          plan={wizard.mode === 'edit' ? wizard.plan : null}
          onClose={() => setWizard(null)}
          onSaved={(id) => {
            setWizard(null)
            if (id !== selected) navigate(pathOf(id))
          }}
        />
      )}
    </div>
  )
}

function PlanCard({ plan, current }: { plan: PlanRow; current: boolean }) {
  const view = PLAN_STATUS_VIEW[plan.status]
  const facts = planFacts(plan)
  return (
    <a href={href(pathOf(plan.id))} className={`${css.planCard} ${plan.status === 'archived' ? css.planCardArchived : ''}`} aria-current={current}>
      <span className={css.cardHead}>
        <span className={css.cardName}>{plan.name}</span>
        <span className={css.cardCode}>{plan.code}</span>
        <Tag tone={view.tone}>{view.label}</Tag>
      </span>
      <span className={css.cardMeta}>
        <span>{facts.price}</span>
        <span>{facts.quota}</span>
        <span>{formatCount(plan.active_subscriptions)} 订阅</span>
      </span>
    </a>
  )
}
