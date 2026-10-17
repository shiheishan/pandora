import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { Add01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { formatMoney } from '../../../core/format'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Card, ConfirmModal, Empty, FilterTabs, Pager, QueryView, Select, StatStrip, Table, Tag, type TableColumn, useToast } from '../../../ui'
import { PageActions, Row, RowList } from '../../kit'
import { GenerateModal, OneTimeModal, useBatchExport, useCodesReport } from './GenerateCodes'
import { TemplateModal } from './TemplateDrawer'
import local from './Gifts.module.css'
import {
  CODE_STATUS,
  TEMPLATE_TYPE_LABEL,
  batchLabel,
  codeFilterQuery,
  grantedLabel,
  redeemRate,
  templateFace,
  templateStatusBody,
  type CodeStatusFilter,
} from './giftModel'
import { formatDateTime } from './logic'
import css from './marketing.module.css'
import { useFailure, useGiftBatches, useGiftCodes, useGiftStats, useGiftTemplates, useGiftUsages, useInvalidateMarketing, usePlanCatalog } from './queries'
import { templateSaved, toggleResponse, type Batch, type CodesGenerated, type GiftCode, type GiftStats, type GiftTemplate, type Plan, type Usage } from './schemas'

type View = 'templates' | 'batches' | 'usages'
const VIEWS: readonly View[] = ['templates', 'batches', 'usages']
const giftPath = (view: View, batchId?: string) => `/marketing/gifts/${view}${batchId ? `/${encodeURIComponent(batchId)}` : ''}`

/** 第四格是已发行通用卡的面额合计 balance_issued（契约） */
function giftStats(s: GiftStats) {
  return [
    { label: '已发行', value: s.codes_total.toLocaleString('zh-CN') },
    { label: '已兑换', value: s.codes_used.toLocaleString('zh-CN') },
    { label: '兑换率', value: redeemRate(s.codes_used, s.codes_total) },
    { label: '面额合计', value: formatMoney(s.balance_issued) },
  ]
}

export function Gifts({ rest }: { rest: string[] }) {
  const view: View = VIEWS.includes(rest[0] as View) ? (rest[0] as View) : 'templates'
  const stats = useGiftStats()
  const [generated, setGenerated] = useState<CodesGenerated | null>(null)
  const [templateModalOpen, setTemplateModalOpen] = useState(false)
  const [usageTemplateId, setUsageTemplateId] = useState('')
  const catalog = usePlanCatalog()
  const templates = useGiftTemplates()
  const plans = catalog.isSuccess ? catalog.data : undefined

  return (
    <div className={css.stack}>
      <PageActions>
        <Button variant="primary" icon={Add01Icon} onClick={() => setTemplateModalOpen(true)}>
          新建
        </Button>
      </PageActions>

      <StatStrip label="礼品卡统计" items={stats.data ? giftStats(stats.data) : undefined} />
      <div className={local.toolbarRow}>
        <FilterTabs<View>
          label="礼品卡视图"
          value={view}
          onChange={(v) => navigate(giftPath(v))}
          items={[
            { value: 'templates', label: '模板' },
            { value: 'batches', label: '批次与卡码' },
            { value: 'usages', label: '使用记录' },
          ]}
        />
        <div className={css.spacer} />
        {view === 'usages' && (
          <div className={local.rightMeta}>
            <Select
              aria-label="按模板筛选"
              size="sm"
              value={usageTemplateId}
              onChange={(e) => setUsageTemplateId(e.target.value)}
              options={[{ value: '', label: '全部模板' }, ...(templates.data ?? []).map((t) => ({ value: t.id, label: t.name }))]}
            />
            <span className={css.faint}>最近 200 条</span>
          </div>
        )}
      </div>

      {view === 'templates' && <Templates onGenerated={setGenerated} />}
      {view === 'batches' && <Batches selected={rest[1] ?? null} />}
      {view === 'usages' && <Usages templateId={usageTemplateId} />}

      <TemplateModal open={templateModalOpen} plans={plans} onClose={() => setTemplateModalOpen(false)} />
      <OneTimeModal
        result={generated}
        onClose={() => {
          if (generated) navigate(giftPath('batches', generated.batch_id))
          setGenerated(null)
        }}
      />
    </div>
  )
}

// ---------------------------------------------------------------------------
// 模板视图（逐行列表）
// ---------------------------------------------------------------------------
function Templates({ onGenerated }: { onGenerated: (result: CodesGenerated) => void }) {
  const list = useGiftTemplates()
  const catalog = usePlanCatalog()
  const plans = catalog.isSuccess ? catalog.data : undefined
  const [generating, setGenerating] = useState<GiftTemplate | null>(null)

  return (
    <>
      <QueryView
        query={list}
        rows={3}
        isEmpty={(data) => data.length === 0}
        empty={<Empty title="还没有礼品卡模板" description="点右上角「新建」建第一张。" />}
      >
        {(templates) => (
          <Card flush>
            <RowList label="礼品卡模板">
              {templates.map((t) => (
                <TemplateRowItem key={t.id} template={t} plans={plans} onGen={() => setGenerating(t)} />
              ))}
            </RowList>
          </Card>
        )}
      </QueryView>
      <GenerateModal
        template={generating}
        onClose={() => setGenerating(null)}
        onGenerated={(result) => {
          setGenerating(null)
          onGenerated(result)
        }}
      />
    </>
  )
}

function TemplateRowItem({
  template: t,
  plans,
  onGen,
}: {
  template: GiftTemplate
  plans: readonly Plan[] | undefined
  onGen: () => void
}) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateMarketing()
  const face = templateFace(t, plans)
  const isPaused = t.status === 'paused'

  const toggleStatus = useMutation({
    mutationFn: () => {
      const nextStatus = isPaused ? 'active' : 'paused'
      return api.post('v1/gift-cards', templateSaved, { body: templateStatusBody(t, nextStatus) })
    },
    onSuccess: () => {
      toast(isPaused ? '已恢复兑换' : '已暂停兑换，已发出的卡码暂时不能用')
      void invalidate()
    },
    onError: (error) => fail(error),
  })

  return (
    <Row
      lead={
        <div className={local.face}>
          <b className={local.faceNum}>{face.face}</b>
          <small className={local.faceKind}>{face.kind}</small>
        </div>
      }
      title={
        <>
          <span>{t.name}</span>
          {' '}
          <Tag tone="neutral">{TEMPLATE_TYPE_LABEL[t.type]}</Tag>
          {isPaused && (
            <>
              {' '}
              <Tag tone="warn">暂停兑换</Tag>
            </>
          )}
          {t.price_archived && (
            <>
              {' '}
              <Tag tone="danger">价格已下架</Tag>
            </>
          )}
        </>
      }
      meta={`兑换后 ${face.after} · 已发 ${t.code_total.toLocaleString('zh-CN')} 张 · 已兑 ${t.code_used.toLocaleString('zh-CN')}`}
      actions={
        <>
          <Button size="sm" onClick={onGen}>
            生成卡码
          </Button>
          <Button
            size="sm"
            disabled={toggleStatus.isPending}
            onClick={() => toggleStatus.mutate()}
          >
            {isPaused ? '恢复兑换' : '暂停兑换'}
          </Button>
        </>
      }
    />
  )
}

// ---------------------------------------------------------------------------
// 批次与卡码
// ---------------------------------------------------------------------------
const BATCH_PAGE = 50
const CODE_PAGE = 50

function Batches({ selected }: { selected: string | null }) {
  const [offset, setOffset] = useState(0)
  const list = useGiftBatches({ limit: BATCH_PAGE, offset })
  const items = list.isError ? [] : (list.data?.items ?? [])
  const current = items.find((b) => b.id === selected) ?? (selected ? undefined : items[0])
  const [exportBatchTarget, setExportBatchTarget] = useState<Batch | null>(null)
  const exporter = useBatchExport(() => setExportBatchTarget(null))

  const total = list.data?.total ?? 0

  const batchColumns: readonly TableColumn<Batch>[] = [
    {
      key: 'batch',
      header: '批次',
      render: (b) => (
        <div>
          <span className={css.mono}>{batchLabel(b)}</span>
          <span className={local.subLine}>
            {formatDateTime(b.created_at)}
            {b.created_by_email ? ` · ${b.created_by_email}` : ''}
          </span>
        </div>
      ),
    },
    {
      key: 'template',
      header: '模板',
      hideOnMobile: true,
      render: (b) => b.template_name,
    },
    {
      key: 'count',
      header: '数量',
      align: 'right',
      mono: true,
      render: (b) => b.count.toLocaleString('zh-CN'),
    },
    {
      key: 'export',
      header: '导出',
      hideOnMobile: true,
      render: (b) =>
        b.exported_at ? (
          <span className={css.muted}>已导出 · {b.exported_by_email ?? ''}</span>
        ) : (
          <span>
            <span className={local.warnText}>未导出</span>
            <span className={css.faint}> · 完整卡码仍可一次性导出</span>
          </span>
        ),
    },
    {
      key: 'actions',
      header: '',
      align: 'right',
      render: (b) => {
        const isExported = b.exported_at !== null
        return (
          <Button
            size="sm"
            disabled={isExported}
            onClick={(e) => {
              e.stopPropagation()
              if (!isExported) setExportBatchTarget(b)
            }}
          >
            {isExported ? '已导出过' : '一次性导出'}
          </Button>
        )
      },
    },
  ]

  const batchFooter =
    total > BATCH_PAGE ? <Pager inline total={total} limit={BATCH_PAGE} offset={offset} onChange={setOffset} /> : <span>共 {total} 批</span>

  return (
    <div className={css.stack}>
      <QueryView
        query={list}
        isEmpty={(d) => d.items.length === 0}
        empty={<Empty bare title="还没有批次" description="在「模板」里生成一批卡码后，这里就会出现。" />}
      >
        {(data) => (
          <Table<Batch>
            label="批次"
            boxed
            columns={batchColumns}
            rows={data.items}
            rowKey={(b) => b.id}
            onRowClick={(b) => navigate(giftPath('batches', b.id))}
            rowClassName={(b) => (b.id === current?.id ? local.cur : undefined)}
            footer={batchFooter}
          />
        )}
      </QueryView>

      {current ? (
        <BatchCodes key={current.id} batch={current} />
      ) : (
        list.isSuccess && selected && <Empty title="批次不在这一页" description="它可能在别的分页里，或者已经不存在。" />
      )}

      <ConfirmModal
        open={exportBatchTarget !== null}
        title={exportBatchTarget ? `一次性导出 ${batchLabel(exportBatchTarget)}？` : ''}
        body="导出的 CSV 含完整卡码，只能导出这一次；之后列表里只显示掩码。请把文件存到安全的位置。"
        confirmLabel="导出 CSV"
        onConfirm={() => (exportBatchTarget ? exporter.mutateAsync(exportBatchTarget.id).catch(() => setExportBatchTarget(null)) : undefined)}
        onCancel={() => setExportBatchTarget(null)}
      />
    </div>
  )
}

const STATUS_OPTIONS: ReadonlyArray<{ value: CodeStatusFilter; label: string }> = [
  { value: '', label: '全部状态' },
  ...(['unused', 'used', 'disabled', 'expired'] as const).map((v) => ({ value: v, label: CODE_STATUS[v]!.label })),
]

function BatchCodes({ batch }: { batch: Batch }) {
  const [offset, setOffset] = useState(0)
  const [status, setStatus] = useState<CodeStatusFilter>('')
  const [reporting, setReporting] = useState(false)
  const filter = codeFilterQuery(batch.id, status)
  const codes = useGiftCodes({ ...filter, limit: CODE_PAGE, offset })
  const report = useCodesReport(() => setReporting(false))
  const total = codes.data?.total ?? 0
  const scope = `批次 ${batchLabel(batch)}${status ? ` · ${CODE_STATUS[status]!.label}` : ''}${codes.data?.total !== undefined ? ` · 共 ${codes.data.total.toLocaleString('zh-CN')} 张` : ''}`

  const codeFooter =
    total > CODE_PAGE ? <Pager inline total={total} limit={CODE_PAGE} offset={offset} onChange={setOffset} /> : <span>共 {total} 张</span>

  const codeColumns: readonly TableColumn<GiftCode>[] = [
    {
      key: 'code',
      header: '卡码',
      mono: true,
      render: (c) => c.code_masked,
    },
    {
      key: 'status',
      header: '状态',
      render: (c) => {
        const st = CODE_STATUS[c.status] ?? { label: c.status, tone: 'neutral' as const }
        return <Tag tone={st.tone}>{st.label}</Tag>
      },
    },
    {
      key: 'user',
      header: '兑换人',
      hideOnMobile: true,
      render: (c) => <span className={css.muted}>{c.used_email ? `兑换人 ${c.used_email}` : '—'}</span>,
    },
    {
      key: 'action',
      header: '',
      align: 'right',
      render: (c) => <CodeActionCell code={c} />,
    },
  ]

  return (
    <Card
      flush
      title={<><span className={css.mono}>{batchLabel(batch)}</span> 的卡码</>}
      extra={
        <>
          <Select
            aria-label="按状态筛选卡码"
            size="sm"
            value={status}
            onChange={(e) => {
              setStatus(e.target.value as CodeStatusFilter)
              setOffset(0)
            }}
            options={STATUS_OPTIONS}
          />
          <Button
            size="sm"
            disabled={total === 0}
            onClick={() => setReporting(true)}
          >
            导出当前筛选
          </Button>
        </>
      }
    >
      <QueryView
        query={codes}
        isEmpty={(d) => d.codes.length === 0}
        empty={<Empty bare title="没有这个状态的码" />}
      >
        {(data) => (
          <Table<GiftCode>
            label={`${batchLabel(batch)} 的卡码`}
            columns={codeColumns}
            rows={data.codes}
            rowKey={(c) => c.id}
            footer={codeFooter}
          />
        )}
      </QueryView>

      <ConfirmModal
        open={reporting}
        title="导出当前筛选的卡码报表？"
        body={`按当前筛选（${scope}）导出对账用 CSV：卡码只有掩码，另含状态、模板、批次、有效期、生成时间、兑换人与兑换时间。完整卡码只在生成批次后的那一次导出里提供，这里拿不到明文。`}
        confirmLabel="导出 CSV"
        onConfirm={() => report.mutateAsync(filter).catch(() => setReporting(false))}
        onCancel={() => setReporting(false)}
      />
    </Card>
  )
}

function CodeActionCell({ code: c }: { code: GiftCode }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateMarketing()
  const toggle = useMutation({
    mutationFn: (disabled: boolean) => api.post(`v1/gift-cards/codes/${c.id}/toggle`, toggleResponse, { body: { disabled } }),
    onSuccess: ({ disabled }) => {
      toast(disabled ? '卡码已停用' : '卡码已恢复')
      void invalidate()
    },
    onError: (error) => {
      fail(error)
      void invalidate()
    },
  })
  const action = c.status === 'unused' ? '停用' : c.status === 'disabled' ? '恢复' : null
  if (!action) return null

  return (
    <button
      type="button"
      className={action === '停用' ? local.linkD : local.linkB}
      disabled={toggle.isPending}
      onClick={() => toggle.mutate(c.status === 'unused')}
    >
      {action}
    </button>
  )
}

// ---------------------------------------------------------------------------
// 使用记录视图
// ---------------------------------------------------------------------------
function Usages({ templateId }: { templateId: string }) {
  const list = useGiftUsages(templateId)

  const usageColumns: readonly TableColumn<Usage>[] = [
    {
      key: 'time',
      header: '时间',
      mono: true,
      hideOnMobile: true,
      render: (u) => <span className={css.muted}>{formatDateTime(u.redeemed_at)}</span>,
    },
    {
      key: 'code',
      header: '卡码',
      mono: true,
      render: (u) => u.code_masked,
    },
    {
      key: 'template',
      header: '模板',
      hideOnMobile: true,
      render: (u) => <span className={css.muted}>{u.template_name}</span>,
    },
    {
      key: 'user',
      header: '用户',
      render: (u) => u.user_email,
    },
    {
      key: 'granted',
      header: '获得',
      align: 'right',
      render: (u) => grantedLabel(u),
    },
  ]

  return (
    <QueryView query={list} empty={<Empty bare title="还没有兑换记录" description="用户兑换礼品卡后会出现在这里。" />}>
      {(rows) => (
        <Table<Usage>
          label="使用记录"
          boxed
          columns={usageColumns}
          rows={rows}
          rowKey={(u) => `${u.code_masked}-${u.redeemed_at}`}
        />
      )}
    </QueryView>
  )
}
