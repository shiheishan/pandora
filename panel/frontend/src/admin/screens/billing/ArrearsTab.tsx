/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/api 的 isApiError，依赖 ../../../core/format 的 formatMoney / formatDateTime，依赖 ../../../core/router 的 href / navigate / useHashLocation，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / Empty / Modal / Pager / QueryView / Segmented / Table / Tag / TextArea / useToast，依赖 ../../actions 的 useCan / useIntentKey，依赖 ../users/api 的 useInvalidateUsers，依赖 ./api，依赖 ./model，依赖 ./failure 的 useBillingFailure，依赖 ./Billing.module.css
 * [OUTPUT]: 对外提供 ArrearsTab
 * [POS]: 订单与收款「挂账」标签（设计稿「欠费单」，保留规则 6 按后端挂账语义做，方向是平台欠用户）：说明条与按币种分开的待处理合计（R3）、状态分段与分页（?s=&o=）、表格（用户与成因 · 单号叠成一格、金额、账龄、状态）、「转入余额」确认框（处理原因必填，billing.adjustment.write + reauth + 幂等 late_payment_apply）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { formatDateTime, formatMoney } from '../../../core/format'
import { href, navigate, useHashLocation } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Empty, Modal, Pager, QueryView, Segmented, Table, Tag, TextArea, useToast, type TableColumn } from '../../../ui'
import { useCan, useIntentKey } from '../../actions'
import { useInvalidateUsers } from '../users/api'
import { LATE_PAGE, lateAppliedSchema, useInvalidateBilling, useLatePayments, type LateCase } from './api'
import css from './Billing.module.css'
import { useBillingFailure } from './failure'
import { ageDays, isLateFilter, LATE_FILTERS, LATE_STATUS_VIEW, lateReason, pendingTotals, reasonProblem, type LateFilter } from './model'

export function ArrearsTab({ now }: { now: Date }) {
  const location = useHashLocation()
  const can = useCan()
  const s = location.query.get('s')
  const filter: LateFilter = isLateFilter(s) ? s : 'all'
  const offset = Math.max(0, Number.parseInt(location.query.get('o') ?? '', 10) || 0)
  const status = LATE_FILTERS.find(([k]) => k === filter)![2]
  const late = useLatePayments(status, offset)
  const [applying, setApplying] = useState<LateCase | null>(null)
  const go = (next: { filter?: LateFilter; offset?: number }) => {
    const f = next.filter ?? filter
    const o = next.offset ?? offset
    navigate('/billing/arrears', { replace: true, query: { s: f === 'all' ? undefined : f, o: o || undefined } })
  }
  const totals = late.data ? pendingTotals(late.data.pending_amounts) : []
  const writable = can('billing.adjustment.write')

  const columns: TableColumn<LateCase>[] = [
    {
      // 设计稿用户与原因分两列；960 宽放不下两列不换行的邮箱与单号，上下叠成一格
      key: 'who',
      header: '用户 · 原因',
      render: (c) => (
        <span className={css.stack}>
          {can('iam.user.read') ? (
            <a className={`${css.link} ${css.ellipsis}`} href={href(`/users/list/${encodeURIComponent(c.user_id)}`)}>
              {c.user_email}
            </a>
          ) : (
            <span className={css.ellipsis}>{c.user_email}</span>
          )}
          <span className={`${css.small} ${css.ellipsis}`}>{lateReason(c)}</span>
        </span>
      ),
    },
    { key: 'amount', header: '金额', width: '110px', align: 'right', mono: true, render: (c) => formatMoney(c.amount, c.currency) },
    {
      key: 'age',
      header: '账龄',
      width: '72px',
      align: 'right',
      render: (c) => {
        const days = ageDays(c.received_at, now)
        return (
          <span className={c.status === 'suspense' && days > 30 ? css.tone_danger : css.muted} title={`到账于 ${formatDateTime(c.received_at)}`}>
            {days} 天
          </span>
        )
      },
    },
    {
      key: 'status',
      header: '状态',
      width: '100px',
      render: (c) => {
        const v = LATE_STATUS_VIEW[c.status]
        return (
          <span title={c.resolution_reason ? `${c.resolved_at ? formatDateTime(c.resolved_at) : ''} ${c.resolution_reason}` : undefined}>
            <Tag tone={v.tone}>{v.label}</Tag>
          </span>
        )
      },
    },
  ]
  if (writable)
    columns.push({
      key: 'act',
      header: '',
      width: '100px',
      align: 'right',
      render: (c) =>
        c.status === 'suspense' ? (
          <Button size="xs" onClick={() => setApplying(c)}>
            转入余额
          </Button>
        ) : null,
    })

  return (
    <div className={css.page}>
      <div className={css.banner}>
        <div className={css.bannerText}>
          <span className={css.bannerTitle}>挂账</span>
          <span className={css.small}>订单取消后才到账、或超额扣款的款项暂记在挂账科目。「转入余额」会把这笔钱记入用户余额（贷记），挂账随之关闭。</span>
        </div>
        <div className={css.total}>
          <span className={css.small}>待处理合计</span>
          <span className={css.totalValue}>{late.data ? totals.join(' · ') || '无' : '…'}</span>
        </div>
      </div>
      <div className={css.toolbar}>
        <Segmented size="sm" label="挂账状态" options={LATE_FILTERS.map(([value, label]) => ({ value, label }))} value={filter} onChange={(f) => go({ filter: f, offset: 0 })} />
      </div>
      <div className={css.tableBox}>
        <QueryView
          query={late}
          rows={4}
          isEmpty={(d) => d.cases.length === 0}
          empty={
            <Empty
              bare
              title={filter === 'suspense' ? '没有待处理的挂账' : filter === 'applied' ? '还没有转入余额的挂账' : '没有挂账'}
              description="订单取消后才到账或多扣了款时，系统会把这笔钱记在这里，等你处理。"
            />
          }
        >
          {(d) => <Table label="挂账列表" columns={columns} rows={d.cases} rowKey={(c) => c.id} />}
        </QueryView>
      </div>
      <Pager total={late.data?.total ?? 0} limit={LATE_PAGE} offset={offset} onChange={(o) => go({ offset: o })} />
      {applying && <ApplyDialog key={applying.id} c={applying} onClose={() => setApplying(null)} />}
    </div>
  )
}

/** POST v1/late-payments/{id}/apply-to-balance：设计确认框本来就标了 reauth，契约补必填的处理原因 */
function ApplyDialog({ c, onClose }: { c: LateCase; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useBillingFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateBilling()
  const invalidateUsers = useInvalidateUsers()
  const [reason, setReason] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = async () => {
    const problem = reasonProblem(reason, '处理原因')
    if (problem) return setError(problem)
    const body = { reason: reason.trim() }
    setBusy(true)
    try {
      await api.post(`v1/late-payments/${encodeURIComponent(c.id)}/apply-to-balance`, lateAppliedSchema, { body, idempotencyKey: intent.keyFor([c.id, body]) })
      intent.reset()
      toast(`已转入 ${c.user_email} 的余额`)
      onClose()
    } catch (e) {
      fail(e, { fields: (f) => setError(f.reason ?? Object.values(f).join('；')), intent })
      // 409「已经处理过了」：关框，列表按最新状态重画；断网与 5xx 留着框，用同一把键重试
      if (isApiError(e, 'conflict')) onClose()
    } finally {
      setBusy(false)
      void invalidate()
      void invalidateUsers()
    }
  }
  return (
    <Modal
      open
      onClose={onClose}
      dismissible={!busy}
      title="转入余额？"
      actions={
        <>
          <Button size="dialog" onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={busy} onClick={() => void submit()}>
            转入余额
          </Button>
        </>
      }
    >
      <div className={css.dialogBody}>
        <p className={css.dialogText}>
          {c.user_email} 的余额将<strong>增加</strong> {formatMoney(c.amount, c.currency)}，挂账关闭。（{lateReason(c)}）
        </p>
        <TextArea
          label="处理原因（写入审计）"
          rows={3}
          value={reason}
          onChange={(e) => {
            setReason(e.target.value)
            setError('')
          }}
          error={error}
          data-autofocus=""
        />
      </div>
    </Modal>
  )
}
