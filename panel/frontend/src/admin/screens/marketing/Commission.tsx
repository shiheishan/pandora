import { useMutation } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { formatMoney } from '../../../core/format'
import { navigate, useHashLocation } from '../../../core/router'
import {
  isUSDTMethod,
  looksLikeTxHash,
  payoutReferenceHint,
  withdrawMethodLabel,
} from '../../../core/withdraw-methods'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Empty, FilterTabs, Input, Modal, QueryView, StatStrip, Tabs, Tag, TextArea, useToast } from '../../../ui'
import local from './Commission.module.css'
import { CommissionSettings } from './CommissionSettings'
import { commissionStats, withdrawalState } from './commissionModel'
import { formatDateTime, type FieldErrors } from './logic'
import css from './marketing.module.css'
import { ReviewEntries } from './ReviewEntries'
import { useCommissionOverview, useFailure, useIntentKey, useInvalidateMarketing, useWithdrawals, type WithdrawalFilter } from './queries'
import { paidResponse, reviewResponse, type CommissionOverview, type Withdrawal } from './schemas'

type CommissionTab = 'review' | 'withdraw' | 'settings'

export function Commission({ rest }: { rest: string[] }) {
  const overview = useCommissionOverview()
  const rawTab = rest[0]
  const currentTab: CommissionTab =
    rawTab === 'withdraw' || rawTab === 'settings' ? rawTab : 'review'

  const onTabChange = (nextTab: CommissionTab) => {
    navigate(`/marketing/commission/${nextTab}`)
  }

  const needReview = overview.data?.need_review ?? 0
  const waitingWithdrawals = overview.data?.waiting_withdrawals ?? 0

  const tabItems = [
    {
      value: 'review' as const,
      label: '待复核佣金',
      badge: needReview > 0 ? needReview : undefined,
    },
    {
      value: 'withdraw' as const,
      label: '提现申请',
      badge: waitingWithdrawals > 0 ? waitingWithdrawals : undefined,
    },
    {
      value: 'settings' as const,
      label: '规则设置',
    },
  ]

  return (
    <div className={css.stack}>
      <StatStrip label="佣金统计" items={overview.data ? commissionStats(overview.data) : undefined} />
      <Tabs<CommissionTab>
        label="佣金与提现"
        value={currentTab}
        items={tabItems}
        onChange={onTabChange}
      />
      {currentTab === 'review' && <ReviewEntries />}
      {currentTab === 'withdraw' && <Withdrawals />}
      {currentTab === 'settings' && (
        <QueryView query={overview} rows={4} isEmpty={() => false} empty={null}>
          {(data: CommissionOverview) => {
            const settingsKey = `${data.rate_percent}-${data.freeze_days}-${data.min_withdraw}-${data.scope}-${data.distribution_enabled}-${data.distribution_levels.join('/')}-${data.withdraw_enabled}-${data.withdraw_methods.join(',')}`
            return <CommissionSettings key={settingsKey} overview={data} />
          }}
        </QueryView>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 提现申请
// ---------------------------------------------------------------------------
type Dialog =
  | { kind: 'approve'; withdrawal: Withdrawal }
  | { kind: 'reject'; withdrawal: Withdrawal }
  | { kind: 'pay'; withdrawal: Withdrawal }
  | null

function Withdrawals() {
  const loc = useHashLocation()
  const rawStatus = loc.query.get('status')
  const filter: WithdrawalFilter =
    rawStatus === 'done' || rawStatus === 'all' ? rawStatus : 'open'

  const list = useWithdrawals(filter)
  const [dialog, setDialog] = useState<Dialog>(null)

  const onFilterChange = (nextFilter: WithdrawalFilter) => {
    navigate('/marketing/commission/withdraw', {
      query: nextFilter === 'open' ? undefined : { status: nextFilter },
      replace: true,
    })
  }

  const emptyTitle =
    filter === 'open'
      ? '没有待处理的提现'
      : filter === 'done'
        ? '还没有处理完的提现'
        : '还没有提现申请'

  return (
    <section className={local.section}>
      <div className={local.filterRow}>
        <FilterTabs<WithdrawalFilter>
          label="提现状态筛选"
          value={filter}
          onChange={onFilterChange}
          items={[
            { value: 'open', label: '处理中' },
            { value: 'done', label: '已完成' },
            { value: 'all', label: '全部' },
          ]}
        />
      </div>
      <QueryView query={list} empty={<Empty bare title={emptyTitle} />}>
        {(rows) => rows.map((w) => <WithdrawalRow key={w.id} withdrawal={w} onDialog={setDialog} />)}
      </QueryView>
      <ApproveDialog withdrawal={dialog?.kind === 'approve' ? dialog.withdrawal : null} onClose={() => setDialog(null)} />
      <RejectDialog withdrawal={dialog?.kind === 'reject' ? dialog.withdrawal : null} onClose={() => setDialog(null)} />
      <PayDialog withdrawal={dialog?.kind === 'pay' ? dialog.withdrawal : null} onClose={() => setDialog(null)} />
    </section>
  )
}

function WithdrawalRow({ withdrawal: w, onDialog }: { withdrawal: Withdrawal; onDialog: (d: Dialog) => void }) {
  const state = withdrawalState(w.status)

  return (
    <div className={local.withdrawalRow}>
      <span className={local.who}>
        <span className={local.ellipsis}>{w.email}</span>
        <span className={`${local.reviewMeta} ${local.ellipsis}`}>
          {formatDateTime(w.requested_at)} · {withdrawMethodLabel(w.method)} · {w.payout_detail || '未填收款信息'}
          {w.status === 'rejected' && w.reject_reason && ` · 拒绝理由：${w.reject_reason === 'account_deleted' ? '账号已注销' : w.reject_reason}`}
          {w.status === 'paid' && w.payout_reference && ` · 流水号 ${w.payout_reference}`}
          {w.status === 'paid' && w.paid_usdt_amount && ` · 实付 ${w.paid_usdt_amount} USDT`}
        </span>
      </span>
      <span className={local.amount} title={`累计获得 ${formatMoney(w.earned_total)}`}>
        {formatMoney(w.amount)}
      </span>
      <span className={local.state}>
        <Tag tone={state.tone}>{state.label}</Tag>
      </span>
      <span className={local.actions}>
        {state.canReview && (
          <>
            <button type="button" className={local.btnPass} onClick={() => onDialog({ kind: 'approve', withdrawal: w })}>
              通过
            </button>
            <button type="button" className={local.btnVoid} onClick={() => onDialog({ kind: 'reject', withdrawal: w })}>
              拒绝
            </button>
          </>
        )}
        {state.canPay && (
          <button type="button" className={local.btnPass} onClick={() => onDialog({ kind: 'pay', withdrawal: w })}>
            记录打款
          </button>
        )}
      </span>
    </div>
  )
}

function ApproveDialog({ withdrawal, onClose }: { withdrawal: Withdrawal | null; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateMarketing()

  const onConfirm = async () => {
    if (!withdrawal) return
    try {
      await api.post(`v1/withdrawals/${withdrawal.id}/review`, reviewResponse, { body: { action: 'approve' } })
      toast('已通过，等待打款')
      void invalidate()
      onClose()
    } catch (error) {
      fail(error)
      void invalidate()
      onClose()
    }
  }

  const bodyText = withdrawal
    ? `${withdrawal.email} · ${formatMoney(withdrawal.amount)} · ${withdrawMethodLabel(withdrawal.method)}。通过后进入「待打款」，打款后再回来记录流水号。`
    : ''

  return (
    <ConfirmModal
      open={withdrawal !== null}
      title="通过这笔提现？"
      body={bodyText}
      confirmLabel="通过"
      cancelLabel="取消"
      onConfirm={onConfirm}
      onCancel={onClose}
    />
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
          <div className={css.muted}>佣金退回用户的可提现余额。</div>
          <TextArea
            label="拒绝理由"
            rows={3}
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            error={errors.reason}
            data-autofocus=""
            placeholder="如：邀请关系异常"
            hint="用户会看到这条理由"
          />
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
  const [usdt, setUsdt] = useState('')
  const [errors, setErrors] = useState<FieldErrors>({})
  const close = () => {
    setReference('')
    setUsdt('')
    setErrors({})
    onClose()
  }
  const pay = useMutation({
    mutationFn: (body: { payout_reference: string; paid_usdt_amount?: string }) =>
      api.post(`v1/withdrawals/${withdrawal!.id}/paid`, paidResponse, {
        body,
        idempotencyKey: intent.keyFor([withdrawal!.id, body]),
      }),
    onSuccess: () => {
      intent.reset()
      toast('已记录打款')
      void invalidate()
      close()
    },
    onError: (error) => {
      if (!fail(error, { fields: setErrors, intent })) close()
      void invalidate()
    },
  })
  const method = withdrawal?.method ?? ''
  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (!reference.trim()) return setErrors({ payout_reference: '请填写转账流水号，日后对账要用' })
    if (isUSDTMethod(method) && !looksLikeTxHash(method, reference)) {
      return setErrors({ payout_reference: '交易哈希格式不对：' + payoutReferenceHint(method) })
    }
    const body: { payout_reference: string; paid_usdt_amount?: string } = {
      payout_reference: reference.trim(),
    }
    const usdtTrim = usdt.trim()
    if (usdtTrim) body.paid_usdt_amount = usdtTrim
    pay.mutate(body)
  }
  return (
    <Modal
      open={withdrawal !== null}
      onClose={close}
      dismissible={!pay.isPending}
      title="记录打款"
      actions={
        <>
          <Button size="dialog" onClick={close} disabled={pay.isPending}>
            取消
          </Button>
          <Button size="dialog" variant="primary" type="submit" form="withdrawal-pay" busy={pay.isPending}>
            记录打款
          </Button>
        </>
      }
    >
      {withdrawal && (
        <form id="withdrawal-pay" className={css.stack} onSubmit={submit} noValidate>
          <div className={css.muted}>
            收款（{withdrawMethodLabel(method)}）：{withdrawal.payout_detail || '未填收款信息'}。记录后这笔佣金从账本扣出，不能撤销。
            {isUSDTMethod(method) ? ' 面板不发起链上转账，请先在钱包里转账再记录。' : ''}
          </div>
          <Input
            label={payoutReferenceHint(withdrawal.method)}
            mono
            value={reference}
            onChange={(e) => setReference(e.target.value)}
            error={errors.payout_reference}
            data-autofocus=""
          />
          {isUSDTMethod(method) && (
            <Input
              label="实付 USDT 数量（可选，仅记录）"
              mono
              inputMode="decimal"
              value={usdt}
              onChange={(e) => setUsdt(e.target.value)}
              error={errors.paid_usdt_amount}
            />
          )}
        </form>
      )}
    </Modal>
  )
}

