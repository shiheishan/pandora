import { useState } from 'react'
import { HugeiconsIcon } from '@hugeicons/react'
import { PlusSignIcon } from '@hugeicons-pro/core-stroke-rounded'
import { Button, Empty, FilterTabs, QueryView, SearchField, Select, Table, Tag, Toolbar, type TableColumn } from '../../../ui'
import { PageActions } from '../../kit'
import { usePlans, type PlanKind, type PlanRow } from './api'
import { CreateModal } from './CreateModal'
import {
  filterPlans,
  kindCounts,
  KIND_LABELS,
  PLAN_STATUS_VIEW,
  planListView,
  STATUS_FILTER_OPTIONS,
  isStatusFilter,
  type KindFilter,
  type StatusFilter,
} from './model'
import css from './Plans.module.css'

export interface ListState {
  kind: KindFilter
  status: StatusFilter
  q: string
}

const KIND_TABS: ReadonlyArray<readonly [Exclude<KindFilter, 'all'>, string]> = [
  ['subscription', '订阅'],
  ['one_time', '一次性'],
]

export function PlanList(props: {
  state: ListState
  onChange: (next: Partial<ListState>) => void
  onOpen: (id: string) => void
}) {
  const { state, onChange, onOpen } = props
  const plans = usePlans()
  const [creating, setCreating] = useState(false)
  const counts = plans.data ? kindCounts(plans.data, state.status) : null
  const initialKind: PlanKind = state.kind === 'one_time' ? 'one_time' : 'subscription'

  return (
    <div className={css.page}>
      {/* 「新建」进外框页头右侧的操作插槽 */}
      <PageActions>
        <Button variant="primary" onClick={() => setCreating(true)}>
          <HugeiconsIcon icon={PlusSignIcon} size={16} strokeWidth={1.6} aria-hidden />新建
        </Button>
      </PageActions>

      <Toolbar
        end={
          <Select
            aria-label="按状态筛选"
            options={STATUS_FILTER_OPTIONS}
            value={state.status}
            onChange={(e) => onChange({ status: isStatusFilter(e.target.value) ? e.target.value : 'all' })}
          />
        }
      >
        <SearchField
          aria-label="搜索套餐"
          placeholder="名称或代码"
          value={state.q}
          onChange={(e) => onChange({ q: e.target.value })}
        />
        {counts && (
          <FilterTabs<KindFilter>
            label="按类型筛选"
            value={state.kind}
            onChange={(kind) => onChange({ kind })}
            items={[{ value: 'all', label: '全部', count: counts.all }, ...KIND_TABS.map(([value, label]) => ({ value, label, count: counts[value] }))]}
          />
        )}
      </Toolbar>

      <QueryView
        query={plans}
        rows={8}
        isEmpty={(rows) => rows.length === 0}
        empty={<Empty bare title="还没有套餐" description="点「新建」建第一个套餐" />}
      >
        {(rows) => <PlanTable rows={filterPlans(rows, state)} onOpen={onOpen} hasAny={rows.length > 0} />}
      </QueryView>

      <CreateModal
        open={creating}
        initialKind={initialKind}
        onClose={() => setCreating(false)}
        onCreated={(id) => {
          setCreating(false)
          onOpen(id)
        }}
      />
    </div>
  )
}

function PlanTable({ rows, onOpen, hasAny }: { rows: readonly PlanRow[]; onOpen: (id: string) => void; hasAny: boolean }) {
  const columns: TableColumn<PlanRow>[] = [
    {
      key: 'name',
      header: '名称',
      render: (r) => (
        <span className={css.cellName}>
          <span className={css.nameRow}>
            <span className={css.nameText}>{r.name}</span>
            {r.recommended && <Tag tone="info">推荐</Tag>}
          </span>
          <span className={`${css.subText} ${css.mono}`}>{r.code}</span>
        </span>
      ),
    },
    {
      key: 'kind',
      header: '类型',
      render: (r) => <span className={css.nowrap}>{KIND_LABELS[r.kind]}</span>,
    },
    {
      key: 'price',
      header: '价格',
      render: (r) => {
        const view = planListView(r)
        return (
          <span className={css.cellName}>
            <span className={`${css.monoText} ${css.nowrap}`}>{view.price}</span>
            {view.priceSub && <span className={css.subText}>{view.priceSub}</span>}
          </span>
        )
      },
    },
    {
      key: 'quota',
      header: '额度',
      render: (r) => {
        const view = planListView(r)
        return (
          <span className={css.cellName}>
            <span className={css.nowrap}>{view.quota}</span>
            {view.quotaSub && <span className={css.subText}>{view.quotaSub}</span>}
          </span>
        )
      },
    },
    {
      key: 'lines',
      header: '线路',
      render: (r) => {
        const view = planListView(r)
        return <span className={view.linesWarn ? css.warnText : undefined}>{view.lines}</span>
      },
    },
    {
      key: 'subs',
      header: '有效订阅',
      align: 'right',
      render: (r) => <span className={css.monoText}>{planListView(r).subs}</span>,
    },
    {
      key: 'status',
      header: '状态',
      align: 'right',
      render: (r) => {
        const view = PLAN_STATUS_VIEW[r.status]
        return <Tag tone={view.tone}>{view.label}</Tag>
      },
    },
  ]

  if (rows.length === 0) {
    return hasAny ? <Empty bare title="没有匹配的套餐" description="换个筛选条件，或点「新建」" /> : null
  }
  return (
    <Table
      label="套餐列表"
      columns={columns}
      rows={rows}
      rowKey={(r) => r.id}
      onRowClick={(r) => onOpen(r.id)}
      boxed
      rowClassName={(r) => (r.status === 'archived' ? css.archivedRow : undefined)}
    />
  )
}
