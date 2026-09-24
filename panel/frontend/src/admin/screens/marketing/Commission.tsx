/**
 * [INPUT]: 依赖 react 的 useState / FormEvent，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../core/format 的 formatMoney，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./logic、./parts、./queries、./schemas，依赖 ./marketing.module.css 与 ./Commission.module.css
 * [OUTPUT]: 对外提供 Commission（营销 · 佣金与提现标签）
 * [POS]: admin/screens/marketing 的佣金与提现标签（设计稿 t_commission）：四个统计（GET v1/commission/overview）、提现申请（GET v1/withdrawals；通过 / 拒绝 POST review，打款 POST paid 带幂等 commission_withdrawal_mark_paid，都要 marketing.withdrawal.approve + reauth）、邀请与佣金设置（POST v1/commission/config，marketing.commission.write + reauth）。拒绝补必填理由、打款补必填转账流水号（契约待补·前端）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { formatMoney } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, Empty, Input, Modal, Segmented, Tag, TextArea, useToast } from '../../../ui'
import local from './Commission.module.css'
import { buildCommissionConfig, commissionStats, commissionToForm, formatDateTime, withdrawalState, type CommissionForm, type FieldErrors } from './logic'
import css from './marketing.module.css'
import { QueryView, StatStrip } from './parts'
import { useCan, useCommissionOverview, useFailure, useIntentKey, useInvalidateMarketing, useWithdrawals } from './queries'
import { okResponse, paidResponse, reviewResponse, type CommissionOverview, type Withdrawal } from './schemas'

export function Commission() {
  const overview = useCommissionOverview()
  return (
    <div className={css.stack}>
      <StatStrip label="佣金统计" items={overview.data ? commissionStats(overview.data) : undefined} />
      <div className={local.columns}>
        <Withdrawals />
        <section className={`${css.panel} ${local.settings}`}>
          <div className={css.panelHead}>
            <h3 className={css.panelTitle}>邀请与佣金设置</h3>
          </div>
          <QueryView query={overview} rows={4} isEmpty={() => false} empty={null}>
            {(data) => <Settings key={`${data.rate_percent}-${data.freeze_days}-${data.min_withdraw}-${data.scope}`} overview={data} />}
          </QueryView>
        </section>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 提现申请
// ---------------------------------------------------------------------------
type Dialog = { kind: 'reject' | 'pay'; withdrawal: Withdrawal } | null

function Withdrawals() {
  const can = useCan()
  const list = useWithdrawals()
  const approver = can('marketing.withdrawal.approve')
  const [dialog, setDialog] = useState<Dialog>(null)

  return (
    <section className={`${css.panel} ${local.withdrawals}`}>
      <div className={css.panelHead}>
        <h3 className={css.panelTitle}>提现申请</h3>
        <span className={css.faint}>审核通过后打款，审核与打款都需二次认证</span>
      </div>
      <QueryView query={list} empty={<Empty bare title="还没有提现申请" description="用户在门户申请提现后会出现在这里。" />}>
        {(rows) => rows.map((w) => <WithdrawalRow key={w.id} withdrawal={w} approver={approver} onDialog={setDialog} />)}
      </QueryView>
      <RejectDialog withdrawal={dialog?.kind === 'reject' ? dialog.withdrawal : null} onClose={() => setDialog(null)} />
      <PayDialog withdrawal={dialog?.kind === 'pay' ? dialog.withdrawal : null} onClose={() => setDialog(null)} />
    </section>
  )
}

function WithdrawalRow({ withdrawal: w, approver, onDialog }: { withdrawal: Withdrawal; approver: boolean; onDialog: (d: Dialog) => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateMarketing()
  const state = withdrawalState(w.status)
  // 设计里「通过」没有确认框；后端要 reauth，由常驻对话框接管
  const approve = useMutation({
    mutationFn: () => api.post(`v1/withdrawals/${w.id}/review`, reviewResponse, { body: { action: 'approve' } }),
    onSuccess: () => {
      toast('已通过，等待打款')
      void invalidate()
    },
    onError: (error) => {
      fail(error)
      void invalidate()
    },
  })

  return (
    <div className={local.row}>
      <span className={local.who}>
        <span className={local.ellipsis}>{w.email}</span>
        <span className={`${css.faint} ${local.ellipsis}`}>
          {formatDateTime(w.requested_at)} · {w.payout_detail || '未填收款信息'}
        </span>
        {w.status === 'rejected' && w.reject_reason && <span className={`${css.faint} ${local.ellipsis}`}>拒绝理由：{w.reject_reason}</span>}
      </span>
      <span className={local.amount}>
        <span className={local.money}>{formatMoney(w.amount, w.currency)}</span>
        <span className={css.faint} title="该用户累计获得的佣金，用来核对提现是否合理">
          累计 {formatMoney(w.earned_total, w.currency)}
        </span>
      </span>
      <span className={local.state}>
        <Tag tone={state.tone}>{state.label}</Tag>
      </span>
      <span className={local.actions}>
        {approver && state.canReview && (
          <>
            <Button size="xs" className={local.reject} onClick={() => onDialog({ kind: 'reject', withdrawal: w })} disabled={approve.isPending}>
              拒绝
            </Button>
            <Button size="xs" variant="primary" busy={approve.isPending} onClick={() => approve.mutate()}>
              通过
            </Button>
          </>
        )}
        {approver && state.canPay && (
          <Button size="xs" variant="primary" className={local.pay} onClick={() => onDialog({ kind: 'pay', withdrawal: w })}>
            打款
          </Button>
        )}
      </span>
    </div>
  )
}

function RejectDialog({ withdrawal, onClose }: { withdrawal: Withdrawal | null; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateMarketing()
  const [reason, setReason] = useState('')
  const [errors, setErrors] = useState<FieldErrors>({})
  const close = () => {
    setReason('')
    setErrors({})
    onClose()
  }
  const reject = useMutation({
    mutationFn: () => api.post(`v1/withdrawals/${withdrawal!.id}/review`, reviewResponse, { body: { action: 'reject', reason: reason.trim() } }),
    onSuccess: () => {
      toast('已拒绝')
      void invalidate()
      close()
    },
    onError: (error) => {
      if (!fail(error, setErrors)) close()
      void invalidate()
    },
  })
  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (!reason.trim()) return setErrors({ reason: '拒绝时必须填写理由，用户会看到它' })
    reject.mutate()
  }
  return (
    <Modal
      open={withdrawal !== null}
      onClose={close}
      dismissible={!reject.isPending}
      title="拒绝这笔提现？"
      actions={
        <>
          <Button size="dialog" onClick={close} disabled={reject.isPending}>
            取消
          </Button>
          <Button size="dialog" variant="danger" type="submit" form="withdrawal-reject" busy={reject.isPending}>
            拒绝
          </Button>
        </>
      }
    >
      {withdrawal && (
        <form id="withdrawal-reject" className={css.stack} onSubmit={submit} noValidate>
          <div className={css.muted}>
            {withdrawal.email} 申请提现 {formatMoney(withdrawal.amount, withdrawal.currency)}。拒绝后金额退回用户佣金余额。
          </div>
          <TextArea label="拒绝理由" rows={3} value={reason} onChange={(e) => setReason(e.target.value)} error={errors.reason} data-autofocus="" placeholder="如：邀请关系异常" />
        </form>
      )}
    </Modal>
  )
}

function PayDialog({ withdrawal, onClose }: { withdrawal: Withdrawal | null; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateMarketing()
  const intent = useIntentKey()
  const [reference, setReference] = useState('')
  const [errors, setErrors] = useState<FieldErrors>({})
  const close = () => {
    setReference('')
    setErrors({})
    onClose()
  }
  const pay = useMutation({
    mutationFn: (body: { payout_reference: string }) => api.post(`v1/withdrawals/${withdrawal!.id}/paid`, paidResponse, { body, idempotencyKey: intent.keyFor([withdrawal!.id, body]) }),
    onSuccess: () => {
      intent.reset()
      toast('已记录打款')
      void invalidate()
      close()
    },
    onError: (error) => {
      if (!fail(error, setErrors)) close()
      void invalidate()
    },
  })
  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (!reference.trim()) return setErrors({ payout_reference: '请填写转账流水号，日后对账要用' })
    pay.mutate({ payout_reference: reference.trim() })
  }
  return (
    <Modal
      open={withdrawal !== null}
      onClose={close}
      dismissible={!pay.isPending}
      title={withdrawal ? `确认已向 ${withdrawal.email} 打款 ${formatMoney(withdrawal.amount, withdrawal.currency)}？` : ''}
      actions={
        <>
          <Button size="dialog" onClick={close} disabled={pay.isPending}>
            取消
          </Button>
          <Button size="dialog" variant="primary" type="submit" form="withdrawal-pay" busy={pay.isPending}>
            确认已打款
          </Button>
        </>
      }
    >
      {withdrawal && (
        <form id="withdrawal-pay" className={css.stack} onSubmit={submit} noValidate>
          <div className={css.muted}>收款：{withdrawal.payout_detail || '未填收款信息'}。记录后这笔佣金从账本扣出，不能撤销。</div>
          <Input label="转账流水号" mono value={reference} onChange={(e) => setReference(e.target.value)} error={errors.payout_reference} data-autofocus="" />
        </form>
      )}
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// 邀请与佣金设置
// ---------------------------------------------------------------------------
function Settings({ overview }: { overview: CommissionOverview }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateMarketing()
  const writable = can('marketing.commission.write')
  // 计佣范围是待补·后端：overview 没回 scope 说明后端还不认这个字段，控件与请求里都不出现
  const supportsScope = overview.scope !== undefined
  const [form, setForm] = useState<CommissionForm>(() => commissionToForm(overview))
  const [errors, setErrors] = useState<FieldErrors>({})

  const save = useMutation({
    mutationFn: (body: object) => api.post('v1/commission/config', okResponse, { body }),
    onSuccess: () => {
      toast('佣金设置已保存')
      void invalidate()
    },
    onError: (error) => fail(error, setErrors),
  })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    const built = buildCommissionConfig(form, supportsScope)
    if (!built.ok) return setErrors(built.errors)
    setErrors({})
    save.mutate(built.body)
  }

  return (
    <form className={local.form} onSubmit={submit} noValidate>
      <label className={local.rate}>
        <span className={css.groupLabel}>返佣比例</span>
        <span className={local.rateLine}>
          <input type="range" min={0} max={50} step={1} value={form.rate} disabled={!writable} onChange={(e) => setForm({ ...form, rate: Number(e.target.value) })} className={local.range} />
          <span className={css.mono}>{form.rate}%</span>
        </span>
        {errors.rate_percent && <span className={css.error}>{errors.rate_percent}</span>}
      </label>
      {supportsScope && (
        <div>
          <div className={css.groupLabel}>计佣范围</div>
          <Segmented<CommissionForm['scope']>
            label="计佣范围"
            size="sm"
            value={form.scope}
            onChange={(scope) => writable && setForm({ ...form, scope })}
            options={[
              { value: 'first_order', label: '仅首单' },
              { value: 'every_order', label: '每笔订单' },
            ]}
            className={local.scope}
          />
        </div>
      )}
      <div className={local.pair}>
        <Input label="结算冻结期（天）" mono inputMode="numeric" value={form.freezeDays} disabled={!writable} onChange={(e) => setForm({ ...form, freezeDays: e.target.value })} error={errors.freeze_days} />
        <Input label="最低提现（¥）" mono inputMode="decimal" value={form.minWithdraw} disabled={!writable} onChange={(e) => setForm({ ...form, minWithdraw: e.target.value })} error={errors.min_withdraw} />
      </div>
      {writable ? (
        <Button type="submit" variant="primary" block busy={save.isPending}>
          保存设置
        </Button>
      ) : (
        <div className={css.faint}>当前账号只能查看，修改需要佣金配置权限。</div>
      )}
    </form>
  )
}
