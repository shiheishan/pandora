import { PlusSignIcon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { formatMoney } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, Card, ConfirmModal, Empty, Input, Modal, QueryView, Table, TextArea, useToast, type TableColumn } from '../../../ui'
import { useFailure, useIntentKey } from '../../actions'
import { parseYuan } from '../users/model'
import { adjustmentSchema, useAdjustments, useInvalidateBilling, type Adjustment } from './api'
import css from './Billing.module.css'
import { adjustBody, adjustmentView, adjustProblems, emptyAdjust, reasonProblem, reverseReason, todayLocal, type AdjustForm } from './model'

export function AdjustTab() {
  const list = useAdjustments()
  const [formOpen, setFormOpen] = useState(false)
  const [reversing, setReversing] = useState<Adjustment | null>(null)

  const columns: TableColumn<Adjustment>[] = [
    {
      key: 'reason',
      header: '原因 · 登记人',
      render: (a) => {
        const v = adjustmentView(a)
        return (
          <>
            <span className={css.ellipsis} title={a.reason}>{a.reason}</span>
            <span className={css.sub2}>{v.meta}</span>
          </>
        )
      },
    },
    {
      key: 'effective_on',
      header: '生效日',
      width: '110px',
      render: (a) => <span className={`${css.mono} ${css.muted}`}>{a.effective_on}</span>,
    },
    {
      key: 'amount',
      header: '金额',
      width: '120px',
      align: 'right',
      render: (a) => {
        const v = adjustmentView(a)
        return <span className={`${css.num} ${css[`tone_${v.tone}`]}`}>{v.amount}</span>
      },
    },
    {
      key: 'act',
      header: '',
      width: '72px',
      align: 'right',
      render: (a) => {
        const v = adjustmentView(a)
        if (v.state === 'open') {
          return (
            <Button variant="link" size="xs" onClick={() => setReversing(a)}>
              冲销
            </Button>
          )
        }
        return <span className={css.small}>{v.state === 'reversal' ? '冲销单' : '已冲销'}</span>
      },
    },
  ]

  return (
    <Card
      title="收入调整"
      subtitle="只追加，不改原账；冲销会生成一条反向记录"
      extra={
        <Button size="sm" aria-expanded={formOpen} onClick={() => setFormOpen((v) => !v)}>
          <HugeiconsIcon icon={PlusSignIcon} size={16} strokeWidth={1.6} aria-hidden />
          登记调整
        </Button>
      }
      flush
    >
      {formOpen && <AdjustFormRow onClose={() => setFormOpen(false)} />}
      <QueryView
        query={list}
        rows={3}
        isEmpty={(d) => d.length === 0}
        empty={
          <Empty
            bare
            title="还没有收入调整"
            description="渠道手续费返还、线下补录这类不经订单的收入，在这里登记后计入报表。"
          />
        }
      >
        {(rows) => (
          <Table
            label="收入调整列表"
            columns={columns}
            rows={rows}
            rowKey={(a) => a.id}
            rowClassName={(a) => (adjustmentView(a).state === 'reversed' ? css.faded : undefined)}
          />
        )}
      </QueryView>
      {reversing && <ReverseDialog key={reversing.id} a={reversing} onClose={() => setReversing(null)} />}
    </Card>
  )
}

/** 设计稿的行内表单：金额、原因 + 契约补的生效日；提交前再确认一次（登记后不能改，只能冲销） */
function AdjustFormRow({ onClose }: { onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateBilling()
  const [form, setForm] = useState<AdjustForm>(emptyAdjust)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [confirming, setConfirming] = useState(false)
  const today = todayLocal()
  const set = <K extends keyof AdjustForm>(key: K, value: AdjustForm[K]) => {
    setForm((f) => ({ ...f, [key]: value }))
    setErrors((e) => ({ ...e, [key === 'effectiveOn' ? 'effective_on' : key]: '' }))
  }
  const check = () => {
    const problems = adjustProblems(form, today)
    if (Object.keys(problems).length) return setErrors(problems)
    setConfirming(true)
  }
  const submit = async () => {
    const body = adjustBody(form)
    try {
      await api.post('v1/revenue/adjustments', adjustmentSchema, { body, idempotencyKey: intent.keyFor(body) })
      intent.reset()
      toast('已登记收入调整')
      setConfirming(false)
      onClose()
    } catch (e) {
      setConfirming(false)
      fail(e, { fields: setErrors, intent })
    } finally {
      void invalidate()
    }
  }
  const cents = parseYuan(form.amount) ?? 0
  return (
    <div className={css.adjForm}>
      <Input size="sm" fieldClassName={css.adjAmount} mono aria-label="调整金额（元）" placeholder="金额，可为负" inputMode="decimal" value={form.amount} onChange={(e) => set('amount', e.target.value)} error={errors.amount} autoFocus />
      <Input size="sm" fieldClassName={css.adjReason} aria-label="调整原因" placeholder="原因（必填，写入审计）" value={form.reason} onChange={(e) => set('reason', e.target.value)} error={errors.reason} />
      <Input size="sm" fieldClassName={css.adjDate} type="date" aria-label="生效日（默认今天）" title="生效日，留空为今天" max={today} value={form.effectiveOn} onChange={(e) => set('effectiveOn', e.target.value)} error={errors.effective_on} />
      <Button size="sm" onClick={onClose}>
        取消
      </Button>
      <Button size="sm" variant="primary" onClick={check}>
        登记
      </Button>
      <ConfirmModal
        open={confirming}
        title="登记收入调整"
        body={`${cents > 0 ? '+' : '−'}${formatMoney(Math.abs(cents))} · ${form.reason.trim()}，生效日 ${form.effectiveOn || '今天'}。登记后不能修改，只能冲销。`}
        confirmLabel="登记"
        onCancel={() => setConfirming(false)}
        onConfirm={submit}
      />
    </div>
  )
}

/** POST v1/revenue/adjustments/{id}/reverse：设计缺原因输入，契约补必填（默认「冲销：原因」） */
function ReverseDialog({ a, onClose }: { a: Adjustment; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateBilling()
  const [reason, setReason] = useState(() => reverseReason(a))
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = async () => {
    const problem = reasonProblem(reason, '冲销原因')
    if (problem) return setError(problem)
    const body = { reason: reason.trim() }
    setBusy(true)
    try {
      await api.post(`v1/revenue/adjustments/${encodeURIComponent(a.id)}/reverse`, adjustmentSchema, { body, idempotencyKey: intent.keyFor([a.id, body]) })
      intent.reset()
      toast('已追加冲销记录')
      onClose()
    } catch (e) {
      fail(e, { fields: (f) => setError(f.reason ?? Object.values(f).join('；')), intent })
      // 409 已冲销 / 反向记录不能再冲销：关框，列表按最新状态重画
      if (isApiError(e, 'conflict')) onClose()
    } finally {
      setBusy(false)
      void invalidate()
    }
  }
  const v = adjustmentView(a)
  return (
    <Modal
      open
      onClose={onClose}
      dismissible={!busy}
      title="冲销这条调整？"
      actions={
        <>
          <Button size="dialog" onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button size="dialog" variant="danger" busy={busy} onClick={() => void submit()}>
            冲销
          </Button>
        </>
      }
    >
      <div className={css.dialogBody}>
        <p className={css.dialogText}>
          系统会追加一条金额相反的记录（{v.amount.startsWith('+') ? '−' : '+'}
          {formatMoney(Math.abs(a.amount))}，生效日同为 {a.effective_on}），原记录保留。
        </p>
        <TextArea
          label="冲销原因（写入审计）"
          rows={2}
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
