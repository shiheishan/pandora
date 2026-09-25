/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useMutation，依赖 react 的 useEffect / useRef / useState，依赖 ../../../core/api 的 isApiError，依赖 ../../../core/format 的 formatDateTime，依赖 ../../../core/router 的 href，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / ConfirmModal / Empty / Modal / Select / Skeleton / Tag / TextArea / useToast，依赖 ../../actions 的 useCan / useFailure / useIntentKey，依赖 ./api、./model、./Composer、./Queue 的 useNow，依赖 ./Tickets.module.css
 * [OUTPUT]: 对外提供 Detail
 * [POS]: 工单页右栏：详情头（标题、编号 · 用户 · 套餐 · 创建时间、已升级徽标、关闭原因、关联订单、SLA 两行）、状态 / 指派下拉、查看用户、升级到 L2（D-B-6 未决前只改状态与优先级、不说「通知值班」）、对话流（用户 / 客服 / 内部备注 / 系统四种气泡）与底部 Composer；没有 ops.ticket.write 时只读
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { isApiError } from '../../../core/api'
import { formatDateTime } from '../../../core/format'
import { href } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Empty, Modal, Select, Skeleton, Tag, TextArea, useToast } from '../../../ui'
import { useCan, useFailure, useIntentKey } from '../../actions'
import { okSchema, useAssignees, useInvalidateTickets, useTicketDetail, type TicketDetail, type TicketStatus } from './api'
import { Composer } from './Composer'
import { assigneeOptions, CATEGORY_LABELS, CLOSED_REASON_LABELS, messageView, PRIORITY_VIEW, slaLines, STATUS_OPTIONS, systemText } from './model'
import { useNow } from './Queue'
import css from './Tickets.module.css'

export function Detail({ id }: { id: string | null }) {
  const q = useTicketDetail(id)
  if (id === null) return <Empty bare className={css.detailEmpty} title="选择左侧的一张工单" description="对话、指派与状态都在这里处理。" />
  if (q.isPending) {
    return (
      <div className={css.detailSkeleton} role="status" aria-label="加载中">
        <Skeleton width="60%" height={20} />
        <Skeleton width="40%" height={12} />
        <Skeleton height={200} />
      </div>
    )
  }
  if (q.isError) {
    // 契约：工单不存在回 404（CodeNotFound）；缺读权限的人进不来这一页
    if (isApiError(q.error, 'not_found')) return <Empty bare className={css.detailEmpty} title="工单不存在" description="它可能已被删除，或链接有误。" />
    return (
      <Empty
        bare
        className={css.detailEmpty}
        title="工单读取失败"
        description={q.error.message}
        action={
          <Button size="sm" onClick={() => void q.refetch()}>
            重试
          </Button>
        }
      />
    )
  }
  return <Loaded key={q.data.id} t={q.data} />
}

function Loaded({ t }: { t: TicketDetail }) {
  const can = useCan()
  const canWrite = can('ops.ticket.write')
  const now = useNow()
  const pri = PRIORITY_VIEW[t.priority]
  const sla = slaLines(t, now)

  return (
    <div className={css.detail}>
      <header className={css.detailHead}>
        <div className={css.titleRow}>
          <div className={css.titleBox}>
            <h2 className={css.title}>{t.subject}</h2>
            <div className={css.subline}>
              <span className={css.mono}>{t.ticket_no}</span> · {t.user_email ?? '—'}
              {t.user_active_plan ? ` · ${t.user_active_plan}` : ''} · {CATEGORY_LABELS[t.category]} · 优先级{pri.label} · 创建于 {formatDateTime(t.created_at)}
              {t.related_order && (
                <>
                  {' · 关联订单 '}
                  {can('billing.order.read') ? (
                    <a href={href(`/billing/orders/${encodeURIComponent(t.related_order.id)}`)} className={css.mono}>
                      {t.related_order.order_no}
                    </a>
                  ) : (
                    <span className={css.mono}>{t.related_order.order_no}</span>
                  )}
                </>
              )}
            </div>
          </div>
          {t.closed_reason && <Tag tone="neutral">{CLOSED_REASON_LABELS[t.closed_reason]}</Tag>}
          {/* 契约：徽标以 escalated_at 为准，升级后被回复离开 escalated 状态仍然显示 */}
          {t.escalated_at && <Tag tone="danger">已升级 · L2</Tag>}
        </div>
        {sla.length > 0 && (
          <div className={css.sla}>
            {sla.map((l) => (
              <span key={l.label} className={css[`sla_${l.tone}`]}>
                {l.label}：{l.text}
              </span>
            ))}
          </div>
        )}
        <Controls t={t} canWrite={canWrite} canViewUser={can('iam.user.read')} />
      </header>
      <Messages t={t} now={now} />
      {canWrite ? <Composer ticket={t} /> : <div className={css.readonly}>当前账号只能查看工单，回复与处理需要工单处理权限。</div>}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 状态 / 指派 / 查看用户 / 升级
// ---------------------------------------------------------------------------
function Controls({ t, canWrite, canViewUser }: { t: TicketDetail; canWrite: boolean; canViewUser: boolean }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateTickets()
  const assignees = useAssignees(true)
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

  return (
    <div className={css.controls}>
      <label className={css.inline}>
        状态
        <Select
          size="sm"
          aria-label="状态"
          options={STATUS_OPTIONS(t.status)}
          value={t.status}
          disabled={!canWrite || setStatus.isPending}
          onChange={(e) => onStatus(e.target.value)}
        />
      </label>
      <label className={css.inline}>
        指派
        <Select
          size="sm"
          aria-label="指派"
          options={assigneeOptions(assignees.data, { id: t.assigned_to, email: t.assignee_email })}
          value={t.assigned_to ?? ''}
          disabled={!canWrite || assign.isPending || assignees.isPending}
          onChange={(e) => assign.mutate(e.target.value)}
        />
      </label>
      <div className={css.spacer} />
      {canViewUser && t.user_id ? (
        <a className={css.linkButton} href={href(`/users/list/${encodeURIComponent(t.user_id)}`)}>
          查看用户
        </a>
      ) : (
        <Button size="sm" disabled title="需要用户读取权限">
          查看用户
        </Button>
      )}
      {canWrite && (
        <Button size="sm" variant="secondary" className={css.escalate} disabled={t.escalated_at !== undefined} onClick={() => setEscalating(true)}>
          升级到 L2
        </Button>
      )}
      <ConfirmModal
        open={escalating}
        title="升级到 L2？"
        // D-B-6 未决前按方案 A：只改状态与优先级，不发通知，文案不承诺「通知二线值班」
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
    </div>
  )
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

// ---------------------------------------------------------------------------
// 对话流
// ---------------------------------------------------------------------------
function Messages({ t, now }: { t: TicketDetail; now: Date }) {
  const end = useRef<HTMLDivElement>(null)
  const last = t.messages.at(-1)?.id
  // 打开工单与来了新消息时滚到底
  useEffect(() => {
    end.current?.scrollIntoView({ block: 'end' })
  }, [last])
  return (
    <div className={css.messages} role="log" aria-label="对话记录">
      {t.messages.length === 0 && <Empty bare title="还没有消息" description="用户的第一条描述会出现在这里。" />}
      {t.messages.map((m) => {
        const v = messageView(m, t.user_email, now)
        if (v.kind === 'system') {
          return (
            <div key={m.id} className={css.system}>
              {systemText(m.body)} · {v.at}
            </div>
          )
        }
        return (
          <div key={m.id} className={v.kind === 'user' ? css.msgLeft : css.msgRight}>
            <div className={css.msgMeta}>
              {v.who} · {v.at}
              {v.kind === 'note' && <span className={css.noteTag}>仅内部可见</span>}
            </div>
            <div className={css[`bubble_${v.kind}`]}>{m.body}</div>
          </div>
        )
      })}
      <div ref={end} />
    </div>
  )
}
