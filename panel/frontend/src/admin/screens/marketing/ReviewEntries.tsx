import { useMutation } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { formatMoney } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, Empty, Modal, QueryView, TextArea, useToast } from '../../../ui'
import local from './Commission.module.css'
import { approveReasonError, formatDateTime, freezeLabel, voidReasonError, type FieldErrors } from './logic'
import css from './marketing.module.css'
import { useFailure, useInvalidateMarketing, useReviewEntries } from './queries'
import { entryReviewResponse, type ReviewEntry } from './schemas'

type Dialog = { kind: 'approve' | 'void'; entry: ReviewEntry } | null

export function ReviewEntries() {
  const q = useReviewEntries()
  const [dialog, setDialog] = useState<Dialog>(null)
  const flat = q.data?.pages.flatMap((p) => p.entries)
  const query = { ...q, data: flat }

  return (
    <section className={css.panel}>
      <div className={css.panelHead}>
        <h3 className={css.panelTitle}>待复核佣金</h3>
        <span className={css.faint}>同源嫌疑或解冻异常的佣金不会自动解冻；通过后照常解冻，作废会冲回计提。两个操作都需要重新验证身份</span>
      </div>
      <QueryView
        query={query}
        empty={<Empty bare title="没有待复核的佣金" description="同源嫌疑、同源检查失败或解冻时余额不足的佣金会出现在这里。" />}
      >
        {(rows) => (
          <>
            {rows.map((e) => (
              <ReviewRow key={e.id} entry={e} onDialog={setDialog} />
            ))}
            {q.hasNextPage && (
              <div className={local.more}>
                <Button size="xs" busy={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
                  加载更多
                </Button>
              </div>
            )}
          </>
        )}
      </QueryView>
      <ApproveDialog entry={dialog?.kind === 'approve' ? dialog.entry : null} onClose={() => setDialog(null)} />
      <VoidDialog entry={dialog?.kind === 'void' ? dialog.entry : null} onClose={() => setDialog(null)} />
    </section>
  )
}

function ReviewRow({ entry: e, onDialog }: { entry: ReviewEntry; onDialog: (d: Dialog) => void }) {
  return (
    <div className={local.row}>
      <span className={local.who}>
        <span className={local.ellipsis}>{e.referrer_email}</span>
        <span className={`${css.faint} ${local.ellipsis}`}>
          来自 {e.referee_email} · 订单 {e.order_no} · {formatDateTime(e.created_at)}
        </span>
        <span className={`${css.faint} ${local.ellipsis}`}>复核原因：{e.review_reason}</span>
        <span className={`${css.faint} ${local.ellipsis}`}>{freezeLabel(e, new Date())}</span>
      </span>
      <span className={local.amount}>
        <span className={local.money}>{formatMoney(e.amount, e.currency)}</span>
      </span>
      <span className={local.actions}>
        <Button size="xs" className={local.reject} onClick={() => onDialog({ kind: 'void', entry: e })}>
          作废
        </Button>
        <Button size="xs" variant="primary" onClick={() => onDialog({ kind: 'approve', entry: e })}>
          通过
        </Button>
      </span>
    </div>
  )
}

// 通过后冻结期已过的条目一小时内就进可提现，之后不能再作废：先确认一次，可选填通过原因（只进审计）
function ApproveDialog({ entry, onClose }: { entry: ReviewEntry | null; onClose: () => void }) {
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
  const approve = useMutation({
    mutationFn: (body: { action: 'approve'; reason?: string }) => api.post(`v1/commission/entries/${entry!.id}/review`, entryReviewResponse, { body }),
    onSuccess: () => {
      toast(entry?.review_source === 'underfunded' ? '已通过；待结科目余额不足时解冻仍会失败，请核对账本' : '已通过，冻结期满后自动解冻')
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
    const msg = approveReasonError(reason)
    if (msg) return setErrors({ reason: msg })
    const trimmed = reason.trim()
    approve.mutate(trimmed ? { action: 'approve', reason: trimmed } : { action: 'approve' })
  }
  return (
    <Modal
      open={entry !== null}
      onClose={close}
      dismissible={!approve.isPending}
      title="通过这笔佣金？"
      actions={
        <>
          <Button size="dialog" onClick={close} disabled={approve.isPending}>
            取消
          </Button>
          <Button size="dialog" variant="primary" type="submit" form="commission-approve" busy={approve.isPending}>
            通过
          </Button>
        </>
      }
    >
      {entry && (
        <form id="commission-approve" className={css.stack} onSubmit={submit} noValidate>
          <div className={css.muted}>
            {entry.referrer_email} 的佣金 {formatMoney(entry.amount, entry.currency)}（订单 {entry.order_no}）。复核原因：{entry.review_reason}
          </div>
          <div className={css.muted}>
            {freezeLabel(entry, new Date())}
            {entry.review_source === 'underfunded' ? '。' : '。解冻后进入可提现，之后不能再作废。'}
          </div>
          <TextArea
            label="通过原因（选填）"
            rows={2}
            value={reason}
            onChange={(ev) => setReason(ev.target.value)}
            error={errors.reason}
            data-autofocus=""
            placeholder="如：已核实为同一家庭的不同用户"
          />
        </form>
      )}
    </Modal>
  )
}

function VoidDialog({ entry, onClose }: { entry: ReviewEntry | null; onClose: () => void }) {
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
  const voidMut = useMutation({
    mutationFn: () =>
      api.post(`v1/commission/entries/${entry!.id}/review`, entryReviewResponse, { body: { action: 'void', reason: reason.trim() } }),
    onSuccess: () => {
      toast('已作废，佣金已冲回')
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
    const msg = voidReasonError(reason)
    if (msg) return setErrors({ reason: msg })
    voidMut.mutate()
  }
  return (
    <Modal
      open={entry !== null}
      onClose={close}
      dismissible={!voidMut.isPending}
      title="作废这笔佣金？"
      actions={
        <>
          <Button size="dialog" onClick={close} disabled={voidMut.isPending}>
            取消
          </Button>
          <Button size="dialog" variant="danger" type="submit" form="commission-void" busy={voidMut.isPending}>
            作废
          </Button>
        </>
      }
    >
      {entry && (
        <form id="commission-void" className={css.stack} onSubmit={submit} noValidate>
          <div className={css.muted}>
            {entry.referrer_email} 的佣金 {formatMoney(entry.amount, entry.currency)}（订单 {entry.order_no}）。作废后这笔佣金从推荐人的待结佣金冲回平台收入，不能撤销。
          </div>
          <TextArea
            label="作废原因"
            rows={3}
            value={reason}
            onChange={(ev) => setReason(ev.target.value)}
            error={errors.reason}
            data-autofocus=""
            placeholder="如：与被邀请人同一来源，判定为自邀刷单"
          />
        </form>
      )}
    </Modal>
  )
}
