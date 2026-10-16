import { useState } from 'react'
import { Button, Empty, FilterTabs, QueryView, SearchField, Select, Table, Tag, Toolbar, type TableColumn } from '../../../ui'
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
      <Toolbar
        end={
          <Button variant="primary" onClick={() => setCreating(true)}>
            新建
          </Button>
        }
      >
        <SearchField
          aria-label="搜索套餐"
          placeholder="名称或代码"
          width={240}
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
        <Select
          aria-label="按状态筛选"
          options={STATUS_FILTER_OPTIONS}
          value={state.status}
          fieldClassName={css.statusSelect}
          onChange={(e) => onChange({ status: isStatusFilter(e.target.value) ? e.target.value : 'all' })}
        />
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
      render: (r) => {
        const archived = r.status === 'archived'
        return (
          <span className={archived ? css.archived : undefined}>
            <span className={css.cellName}>
              <span className={css.nameRow}>
                <span className={css.nameText}>{r.name}</span>
                {r.recommended && (
                  <Tag tone="brand" className={css.nameTag}>
                    推荐
                  </Tag>
                )}
              </span>
              <span className={css.subText}>{r.code}</span>
            </span>
          </span>
        )
      },
    },
    {
      key: 'kind',
      header: '类型',
      render: (r) => <span className={cellCls(r)}>{KIND_LABELS[r.kind]}</span>,
    },
    {
      key: 'price',
      header: '价格',
      render: (r) => {
        const view = planListView(r)
        return (
          <span className={cellCls(r)}>
            <span className={css.cellName}>
              <span className={css.monoText}>{view.price}</span>
              {view.priceSub && <span className={css.subText}>{view.priceSub}</span>}
            </span>
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
          <span className={cellCls(r)}>
            <span className={css.cellName}>
              <span>{view.quota}</span>
              <span className={css.subText}>{view.quotaSub}</span>
            </span>
          </span>
        )
      },
    },
    {
      key: 'lines',
      header: '线路',
      render: (r) => {
        const view = planListView(r)
        return <span className={[cellCls(r), view.linesWarn ? css.warnText : ''].filter(Boolean).join(' ')}>{view.lines}</span>
      },
    },
    {
      key: 'subs',
      header: '有效订阅',
      align: 'right',
      render: (r) => <span className={`${cellCls(r)} ${css.monoText}`}>{planListView(r).subs}</span>,
    },
    {
      key: 'status',
      header: '状态',
      align: 'right',
      render: (r) => {
        const view = PLAN_STATUS_VIEW[r.status]
        return (
          <span className={cellCls(r)}>
            <Tag tone={view.tone}>{view.label}</Tag>
          </span>
        )
      },
    },
  ]

  if (rows.length === 0) {
    return hasAny ? <Empty bare title="没有匹配的套餐" description="换个筛选条件，或点「新建」" /> : null
  }
  return <Table label="套餐列表" columns={columns} rows={rows} rowKey={(r) => r.id} onRowClick={(r) => onOpen(r.id)} />
}

/** 每格外包一层：归档整行降透明度（Table 没有行 class） */
function cellCls(row: PlanRow): string {
  return row.status === 'archived' ? (css.archived ?? '') : ''
}
