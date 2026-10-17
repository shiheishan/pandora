import { useMutation } from '@tanstack/react-query'
import { useState, type ReactNode } from 'react'
import { isApiError } from '../../../core/api'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Modal, Select, TextArea, useToast } from '../../../ui'
import { useFailure, useIntentKey } from '../../actions'
import { okSchema, useAssignees, useInvalidateTickets, type TicketDetail, type TicketStatus } from './api'
import css from './Detail.module.css'
import { assigneeOptions, STATUS_OPTIONS } from './model'

export interface TicketControls {
  /** 状态下拉：<Select size="sm" aria-label="状态" options={STATUS_OPTIONS(t.status)} value={t.status} …/> */
  status: ReactNode
  /** 指派下拉：<Select size="sm" aria-label="指派" options={assigneeOptions(…)} …/> */
  assign: ReactNode
  /** 升级：已升级（t.escalated_at !== undefined）时按钮禁用；open() 打开确认框 */
  escalate: { escalated: boolean; open: () => void }
  /** 升级确认框 + 关闭工单对话框，调用方原样渲染一次 */
  dialogs: ReactNode
}

export function useTicketControls(t: TicketDetail): TicketControls {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateTickets()
  const assignees = useAssignees()
  const statusKey = useIntentKey()
  const assignKey = useIntentKey()
  const [closing, setClosing] = useState(false)
  const [escalating, setEscalating] = useState(false)

  const setStatus = useMutation({
    mutationFn: (body: { status: TicketStatus; reason?: string }) =>
      api.post(`v1/tickets/${encodeURIComponent(t.id)}/status`, okSchema, { body, idempotencyKey: statusKey.keyFor([t.id, body]) }),
    onSuccess: (_, body) => {
      statusKey.reset()
      void invalidate()
      toast(body.status === 'escalated' ? '已升级到 L2，优先级至少为高' : body.status === 'closed' ? '工单已关闭' : '状态已更新')
    },
  })

  const assign = useMutation({
    mutationFn: (assigned_to: string) =>
      api.post(`v1/tickets/${encodeURIComponent(t.id)}/assign`, okSchema, { body: { assigned_to }, idempotencyKey: assignKey.keyFor([t.id, assigned_to]) }),
    onSuccess: (_, assigned_to) => {
      assignKey.reset()
      void invalidate()
      const who = assignees.data?.find((a) => a.id === assigned_to)
      toast(assigned_to ? `已指派给 ${who?.display_name || who?.email || '所选客服'}` : '已取消指派')
    },
    onError: (e) => fail(e, { intent: assignKey }),
  })

  const onStatus = (value: string) => {
    const status = value as TicketStatus
    if (status === t.status) return
    // 关闭会让用户看到「客服关闭」，先问原因（选填，写进系统消息）
    if (status === 'closed') return setClosing(true)
    setStatus.mutate({ status }, { onError: (e) => fail(e, { intent: statusKey }) })
  }

  const statusNode = (
    <Select
      size="sm"
      aria-label="状态"
      options={STATUS_OPTIONS(t.status)}
      value={t.status}
      disabled={setStatus.isPending}
      onChange={(e) => onStatus(e.target.value)}
    />
  )

  const assignNode = (
    <Select
      size="sm"
      aria-label="指派"
      options={assigneeOptions(assignees.data, { id: t.assigned_to, email: t.assignee_email })}
      value={t.assigned_to ?? ''}
      disabled={assign.isPending || assignees.isPending}
      onChange={(e) => assign.mutate(e.target.value)}
    />
  )

  const dialogs = (
    <>
      <ConfirmModal
        open={escalating}
        title="升级到 L2？"
        // D-B-6 已决（5.A.2）方案 A：只改状态与优先级，不发通知，文案不承诺「通知二线值班」
        body="工单会标为已升级，优先级至少提到「高」，并在对话里留一条系统记录。目前不会自动通知任何人。"
        confirmLabel="升级"
        tone="danger"
        onCancel={() => setEscalating(false)}
        onConfirm={() =>
          setStatus.mutateAsync({ status: 'escalated' }).then(
            () => setEscalating(false),
            (e: unknown) => {
              if (isApiError(e, 'reauth_required')) return
              fail(e, { intent: statusKey })
            },
          )
        }
      />
      <CloseDialog
        open={closing}
        busy={setStatus.isPending}
        onCancel={() => setClosing(false)}
        onConfirm={(reason, setError) =>
          setStatus.mutate(reason ? { status: 'closed', reason } : { status: 'closed' }, {
            onSuccess: () => setClosing(false),
            onError: (e) => void fail(e, { fields: (f) => setError(f.reason ?? f.status ?? '关闭失败'), intent: statusKey }),
          })
        }
      />
    </>
  )

  return {
    status: statusNode,
    assign: assignNode,
    escalate: {
      escalated: t.escalated_at !== undefined,
      open: () => setEscalating(true),
    },
    dialogs,
  }
}

const REASON_MAX = 500

function CloseDialog({
  open,
  busy,
  onCancel,
  onConfirm,
}: {
  open: boolean
  busy: boolean
  onCancel: () => void
  onConfirm: (reason: string, setError: (message: string) => void) => void
}) {
  const [reason, setReason] = useState('')
  const [error, setError] = useState<string | null>(null)
  const tooLong = [...reason.trim()].length > REASON_MAX
  const close = () => {
    setReason('')
    setError(null)
    onCancel()
  }
  return (
    <Modal
      open={open}
      onClose={close}
      dismissible={!busy}
      title="关闭工单"
      actions={
        <>
          <Button size="dialog" onClick={close} disabled={busy}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={busy} disabled={tooLong} onClick={() => onConfirm(reason.trim(), setError)}>
            关闭工单
          </Button>
        </>
      }
    >
      <p className={css.dialogText}>用户会看到工单已被客服关闭。之后任何一方回复都会重新打开它。</p>
      <TextArea
        label="关闭说明（选填）"
        rows={3}
        value={reason}
        onChange={(e) => {
          setReason(e.target.value)
          setError(null)
        }}
        error={error ?? (tooLong ? `最多 ${REASON_MAX} 字` : undefined)}
        hint="会写进对话里的系统记录，用户可见。"
      />
    </Modal>
  )
}
