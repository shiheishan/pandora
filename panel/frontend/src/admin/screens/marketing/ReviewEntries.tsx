import { useMutation } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { formatMoney } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, Card, Empty, Modal, QueryView, TextArea, useToast } from '../../../ui'
import { Row, RowList } from '../../kit'
import local from './Commission.module.css'
import { approveReasonError, freezeLabel, voidReasonError } from './commissionModel'
import { formatDateTime, type FieldErrors } from './logic'
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
    <>
      <QueryView
        query={query}
        empty={<Empty bare title="没有待复核的佣金" />}
      >
        {(rows) => (
          <Card flush>
            <RowList label="待复核佣金">
              {rows.map((e) => (
                <ReviewEntryRow key={e.id} entry={e} onDialog={setDialog} />
              ))}
            </RowList>
            {q.hasNextPage && (
              <div className={local.more}>
                <Button size="xs" busy={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
                  加载更多
                </Button>
              </div>
            )}
          </Card>
        )}
      </QueryView>
      <ApproveDialog entry={dialog?.kind === 'approve' ? dialog.entry : null} onClose={() => setDialog(null)} />
      <VoidDialog entry={dialog?.kind === 'void' ? dialog.entry : null} onClose={() => setDialog(null)} />
    </>
  )
}

function ReviewEntryRow({ entry: e, onDialog }: { entry: ReviewEntry; onDialog: (d: Dialog) => void }) {
  const isUnderfunded = e.review_source === 'underfunded'
  const freezeText = freezeLabel(e, new Date())

  const metaContent = (
    <span className={local.warnText}>{e.review_reason}</span>
  )

  const extraContent = (
    <small className={local.subLine}>
      订单 {e.order_no} · {formatDateTime(e.created_at)} ·{' '}
      {isUnderfunded ? (
        <span className={local.dangerText}>{freezeText}</span>
      ) : (
        <span>{freezeText}</span>
      )}
    </small>
  )

  return (
    <Row
      title={`${e.referrer_email} ← ${e.referee_email}`}
      meta={metaContent}
      extra={extraContent}
      value={formatMoney(e.amount)}
      actions={
        <>
          {e.review_source !== 'account_deleted' && (
            <Button size="sm" onClick={() => onDialog({ kind: 'approve', entry: e })}>
              通过
            </Button>
          )}
          <Button size="sm" variant="danger" onClick={() => onDialog({ kind: 'void', entry: e })}>
            作废
          </Button>
        </>
      }
    />
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
            {entry.referrer_email} 的佣金 {formatMoney(entry.amount)}（订单 {entry.order_no}）。复核原因：{entry.review_reason}
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
            {entry.referrer_email} 的佣金 {formatMoney(entry.amount)}（订单 {entry.order_no}）。作废后这笔佣金从推荐人的待结佣金冲回平台收入，不能撤销。
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
