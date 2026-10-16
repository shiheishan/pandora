import { useMutation } from '@tanstack/react-query'
import { useState, type KeyboardEvent } from 'react'
import { formatMoney } from '../../../core/format'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Empty, FilterTabs, Pager, QueryView, Select, StatStrip, Tag, useToast } from '../../../ui'
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
import { templateSaved, toggleResponse, type Batch, type CodesGenerated, type GiftCode, type GiftStats, type GiftTemplate, type Plan } from './schemas'

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
        {view === 'templates' && (
          <Button variant="primary" onClick={() => setTemplateModalOpen(true)}>
            新建
          </Button>
        )}
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
          <div className={local.templateList}>
            {templates.map((t) => (
              <TemplateRowItem key={t.id} template={t} plans={plans} onGen={() => setGenerating(t)} />
            ))}
          </div>
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
    <div className={isPaused ? `${local.templateRow} ${local.templateRowPaused}` : local.templateRow}>
      <div className={local.tplFaceCol}>
        <div className={local.tplFace}>{face.face}</div>
        <div className={local.tplKind}>{face.kind}</div>
      </div>
      <div className={local.tplMainCol}>
        <div className={local.tplTitleLine}>
          <span className={local.tplName}>{t.name}</span>
          <span className={local.typeTag}>{TEMPLATE_TYPE_LABEL[t.type]}</span>
          {isPaused && <Tag tone="warn">暂停兑换</Tag>}
          {t.price_archived && <Tag tone="danger">价格已下架</Tag>}
        </div>
        <div className={local.tplMetaLine}>
          兑换后 {face.after} · 已发 {t.code_total.toLocaleString('zh-CN')} 张 · 已兑 {t.code_used.toLocaleString('zh-CN')}
        </div>
      </div>
      <div className={local.tplActionsCol}>
        <button type="button" className={local.tplGenBtn} onClick={onGen}>
          生成卡码
        </button>
        <button
          type="button"
          className={local.tplPauseBtn}
          disabled={toggleStatus.isPending}
          onClick={() => toggleStatus.mutate()}
        >
          {isPaused ? '恢复兑换' : '暂停兑换'}
        </button>
      </div>
    </div>
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

  const handleKeyDown = (e: KeyboardEvent<HTMLTableRowElement>, id: string) => {
    // 行里的「一次性导出」按钮自己处理按键
    if (e.target !== e.currentTarget) return
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      navigate(giftPath('batches', id))
    }
  }

  return (
    <div className={local.batchesStack}>
      <div className={local.tableWrap}>
        <QueryView query={list} isEmpty={(d) => d.items.length === 0} empty={<Empty bare title="还没有批次" description="在「模板」里生成一批卡码后，这里就会出现。" />}>
          {(data) => (
            <>
              <table className={local.dataTable}>
                <thead>
                  <tr>
                    <th className={local.th}>批次</th>
                    <th className={local.th}>模板</th>
                    <th className={`${local.th} ${local.thRight} ${local.thQty}`}>数量</th>
                    <th className={local.th}>导出</th>
                    <th className={`${local.th} ${local.thAction}`}></th>
                  </tr>
                </thead>
                <tbody>
                  {data.items.map((b) => {
                    const isSelected = b.id === current?.id
                    return (
                      <tr
                        key={b.id}
                        tabIndex={0}
                        aria-current={isSelected}
                        className={isSelected ? `${local.batchTr} ${local.batchTrSelected}` : local.batchTr}
                        onClick={() => navigate(giftPath('batches', b.id))}
                        onKeyDown={(e) => handleKeyDown(e, b.id)}
                      >
                        <td className={local.batchTd}>
                          <span className={css.mono}>{batchLabel(b)}</span>
                          <span className={local.subLine}>
                            {formatDateTime(b.created_at)}
                          </span>
                        </td>
                        <td className={local.batchTdStandard}>{b.template_name}</td>
                        <td className={local.batchTdQty}>{b.count.toLocaleString('zh-CN')}</td>
                        <td className={`${local.batchTdExport} ${b.exported_at ? local.exportDone : local.exportPending}`}>
                          {b.exported_at ? `已导出 · ${b.exported_by_email ?? ''}` : '未导出 · 完整卡码仍可一次性导出'}
                        </td>
                        <td className={local.batchTdBtn}>
                          <button
                            type="button"
                            className={local.textActionBtn}
                            disabled={b.exported_at !== null}
                            onClick={(e) => {
                              e.stopPropagation()
                              if (!b.exported_at) setExportBatchTarget(b)
                            }}
                          >
                            {b.exported_at !== null ? '已导出过' : '一次性导出'}
                          </button>
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
              <Pager total={data.total} limit={BATCH_PAGE} offset={offset} onChange={setOffset} />
            </>
          )}
        </QueryView>
      </div>

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
        onConfirm={() => exportBatchTarget && exporter.mutateAsync(exportBatchTarget.id).catch(() => setExportBatchTarget(null))}
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
  const total = codes.data?.total
  const scope = `批次 ${batchLabel(batch)}${status ? ` · ${CODE_STATUS[status]!.label}` : ''}${total !== undefined ? ` · 共 ${total.toLocaleString('zh-CN')} 张` : ''}`

  return (
    <section className={local.codesSection}>
      <div className={local.codesHeader}>
        <h3 className={local.codesTitle}>{batchLabel(batch)} 的卡码</h3>
        <div className={local.codeStatusSelect}>
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
        </div>
        <button
          type="button"
          className={local.textActionBtn}
          disabled={total === 0}
          onClick={() => setReporting(true)}
        >
          导出当前筛选
        </button>
      </div>

      <QueryView
        query={codes}
        isEmpty={(d) => d.codes.length === 0}
        empty={<div className={local.emptyBlock}>没有这个状态的码</div>}
      >
        {(data) => (
          <>
            {data.codes.map((c) => (
              <CodeRow key={c.id} code={c} />
            ))}
            <Pager total={data.total} limit={CODE_PAGE} offset={offset} onChange={setOffset} />
          </>
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
    </section>
  )
}

function CodeRow({ code: c }: { code: GiftCode }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateMarketing()
  const status = CODE_STATUS[c.status] ?? { label: c.status, tone: 'neutral' as const }
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

  return (
    <div className={local.codeGridRow}>
      <span className={local.codeText}>{c.code_masked}</span>
      <span>
        <Tag tone={status.tone}>{status.label}</Tag>
      </span>
      <span className={local.whoText}>{c.used_email ? `兑换人 ${c.used_email}` : ''}</span>
      <span className={local.rightAlign}>
        {action && (
          <button
            type="button"
            className={action === '停用' ? `${local.textActionBtn} ${local.dangerBtn}` : local.textActionBtn}
            disabled={toggle.isPending}
            onClick={() => toggle.mutate(c.status === 'unused')}
          >
            {action}
          </button>
        )}
      </span>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 使用记录视图
// ---------------------------------------------------------------------------
function Usages({ templateId }: { templateId: string }) {
  const list = useGiftUsages(templateId)

  return (
    <div className={local.tableWrap}>
      <QueryView query={list} empty={<Empty bare title="还没有兑换记录" description="用户兑换礼品卡后会出现在这里。" />}>
        {(rows) => (
          <table className={local.usagesTable}>
            <thead>
              <tr>
                <th className={`${local.th} ${local.thTime}`}>
                  时间
                </th>
                <th className={local.th}>卡码</th>
                <th className={local.th}>模板</th>
                <th className={local.th}>用户</th>
                <th className={`${local.th} ${local.thRight}`}>获得</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((u, i) => (
                <tr key={`${u.code_masked}-${u.redeemed_at}-${i}`} className={local.usageTr}>
                  <td className={local.usageTdMono}>{formatDateTime(u.redeemed_at)}</td>
                  <td className={local.usageTdMono}>{u.code_masked}</td>
                  <td className={local.usageTd}>{u.template_name}</td>
                  <td className={`${local.usageTd} ${local.usageTdMuted}`}>
                    {u.user_email}
                  </td>
                  <td className={local.usageTdRight}>{grantedLabel(u)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </QueryView>
    </div>
  )
}
