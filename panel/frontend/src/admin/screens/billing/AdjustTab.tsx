/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/api 的 isApiError，依赖 ../../../core/format 的 formatMoney，依赖 ../../../core/router 的 navigate / useHashLocation，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / ConfirmModal / Empty / Input / Modal / QueryView / Select / TextArea / useToast，依赖 ../../actions 的 useCan / useFailure / useIntentKey，依赖 ./api，依赖 ./model，依赖 ./Billing.module.css
 * [OUTPUT]: 对外提供 AdjustTab
 * [POS]: 订单与收款「收入调整」标签（后台-05，报表口径、只追加）：币种筛选（?c=）、「登记调整」行内表单（币种、金额元转分可为负、原因、生效日可选且不晚于今天——设计缺、契约补）与确认、列表（原因与登记人 · 时间 R66 叠成一格、生效日、币种、金额、冲销 / 冲销单 / 已冲销；只有列表横向滚动）、冲销确认框（冲销原因必填，默认「冲销：原因」）。写接口 billing.adjustment.write + reauth + 幂等；最多 200 条、无分页
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { formatMoney } from '../../../core/format'
import { navigate, useHashLocation } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Empty, Input, Modal, QueryView, Select, TextArea, useToast } from '../../../ui'
import { useCan, useFailure, useIntentKey } from '../../actions'
import { parseYuan } from '../users/model'
import { adjustmentSchema, useAdjustments, useInvalidateBilling, type Adjustment, type Currency } from './api'
import css from './Billing.module.css'
import { adjustBody, adjustmentView, adjustProblems, emptyAdjust, reasonProblem, reverseReason, todayLocal, type AdjustForm } from './model'

const CURRENCY_OPTIONS = [
  { value: 'CNY', label: 'CNY' },
  { value: 'USD', label: 'USD' },
]

export function AdjustTab() {
  const location = useHashLocation()
  const can = useCan()
  const c = location.query.get('c')
  const currency = c === 'CNY' || c === 'USD' ? c : ''
  const list = useAdjustments(currency)
  const writable = can('billing.adjustment.write')
  const [formOpen, setFormOpen] = useState(false)
  const [reversing, setReversing] = useState<Adjustment | null>(null)

  return (
    <section className={css.adjCard} aria-label="收入调整">
      <div className={css.adjHead}>
        <span className={css.bannerTitle}>收入调整</span>
        <span className={css.small}>只追加，不改原账；冲销会生成一条反向记录</span>
        <span className={css.spacer} />
        <Select
          size="sm"
          aria-label="币种"
          emptyOption="全部币种"
          options={CURRENCY_OPTIONS}
          value={currency}
          onChange={(e) => navigate('/billing/adjust', { replace: true, query: { c: e.target.value || undefined } })}
        />
        {writable && (
          <Button size="sm" variant="primary" aria-expanded={formOpen} onClick={() => setFormOpen((v) => !v)}>
            登记调整
          </Button>
        )}
      </div>
      {formOpen && <AdjustFormRow onClose={() => setFormOpen(false)} />}
      <QueryView
        query={list}
        rows={3}
        isEmpty={(d) => d.length === 0}
        empty={
          <Empty
            bare
            title={currency ? `没有 ${currency} 的收入调整` : '还没有收入调整'}
            description="渠道手续费返还、线下补录这类不经订单的收入，在这里登记后计入报表。"
          />
        }
      >
        {(rows) => (
          <div role="table" aria-label="收入调整列表" className={css.adjList}>
            <div role="row" className={`${css.adjRow} ${css.adjHeader}`}>
              <span role="columnheader">原因 · 登记人</span>
              <span role="columnheader">生效日</span>
              <span role="columnheader">币种</span>
              <span role="columnheader" className={css.right}>
                金额
              </span>
              <span role="columnheader" />
            </div>
            {rows.map((a) => (
              <Row key={a.id} a={a} writable={writable} onReverse={() => setReversing(a)} />
            ))}
          </div>
        )}
      </QueryView>
      {reversing && <ReverseDialog key={reversing.id} a={reversing} onClose={() => setReversing(null)} />}
    </section>
  )
}

function Row({ a, writable, onReverse }: { a: Adjustment; writable: boolean; onReverse: () => void }) {
  const v = adjustmentView(a)
  return (
    <div role="row" className={`${css.adjRow} ${v.state === 'reversed' ? css.faded : ''}`}>
      <span role="cell" className={css.stack}>
        <span className={css.ellipsis} title={a.reason}>
          {a.reason}
        </span>
        <span className={`${css.small} ${css.ellipsis}`}>{v.meta}</span>
      </span>
      <span role="cell" className={`${css.mono} ${css.muted}`}>
        {a.effective_on}
      </span>
      <span role="cell" className={css.muted}>
        {a.currency}
      </span>
      <span role="cell" className={`${css.right} ${css.num} ${css[`tone_${v.tone}`]}`}>
        {v.amount}
      </span>
      <span role="cell" className={css.right}>
        {v.state !== 'open' ? (
          <span className={css.small}>{v.state === 'reversal' ? '冲销单' : '已冲销'}</span>
        ) : writable ? (
          <button type="button" className={css.textAction} onClick={onReverse}>
            冲销
          </button>
        ) : null}
      </span>
    </div>
  )
}

/** 设计稿的行内表单：币种、金额、原因 + 契约补的生效日；提交前再确认一次（登记后不能改，只能冲销） */
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
      <Select size="sm" fieldClassName={css.adjCurrency} aria-label="调整币种" options={CURRENCY_OPTIONS} value={form.currency} onChange={(e) => set('currency', e.target.value as Currency)} error={errors.currency} />
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
        body={`${cents > 0 ? '+' : '−'}${formatMoney(Math.abs(cents), form.currency)} · ${form.reason.trim()}，生效日 ${form.effectiveOn || '今天'}。登记后不能修改，只能冲销。`}
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
          {formatMoney(Math.abs(a.amount), a.currency)}，生效日同为 {a.effective_on}），原记录保留。
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
