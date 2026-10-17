import { useState, type KeyboardEvent } from 'react'
import { ApiError } from '../../../core/api'
import { href } from '../../../core/router'
import { ConfirmModal, QueryView, Tag, TextArea, useToast } from '../../../ui'
import { Btn } from '../../kit'
import { endsIntent, useIntentKey } from '../common/intent'
import { useCloseTicket, useReplyTicket, useTicket, useWithdrawTicket, type TicketDetail } from './api'
import { authorLabel, canClose, canReply, canWithdraw, messageTime, REPLY_MAX, ticketStatus } from './model'
import css from './Tickets.module.css'

export function TicketView({ id, category }: { id: string; category: (code: string) => string }) {
  const ticket = useTicket(id)
  return (
    <article className={css.th}>
      <a href={href('/tickets')} className={css.back}>
        ‹ 全部工单
      </a>
      <QueryView query={ticket} rows={4} empty={null}>
        {(t) => <Thread ticket={t} category={category(t.category)} />}
      </QueryView>
    </article>
  )
}

function Thread({ ticket, category }: { ticket: TicketDetail; category: string }) {
  const toast = useToast()
  const status = ticketStatus(ticket)
  const close = useCloseTicket(ticket.id)
  const withdraw = useWithdrawTicket(ticket.id)
  const closeKey = useIntentKey()
  const [confirm, setConfirm] = useState<'close' | 'withdraw' | null>(null)
  const [error, setError] = useState<string | null>(null)

  async function doClose() {
    try {
      await close.mutateAsync(closeKey.keyFor({ close: ticket.id }))
      closeKey.reset()
      toast('感谢反馈，工单已关闭')
    } catch (e) {
      if (endsIntent(e)) closeKey.reset()
      setError(e instanceof Error ? e.message : '关闭失败，请稍后重试')
    }
    setConfirm(null)
  }

  async function doWithdraw() {
    try {
      await withdraw.mutateAsync()
      toast('工单已撤回')
    } catch (e) {
      setError(e instanceof Error ? e.message : '撤回失败，请稍后重试')
    }
    setConfirm(null)
  }

  return (
    <>
      <header className={css.head}>
        <div className={css.headMain}>
          <h2 className={css.subject}>{ticket.subject}</h2>
          <div className={css.headMeta}>
            <Tag tone={status.tone}>{status.label}</Tag>
            <span>{category}</span>
            <span className={css.mono}>#{ticket.ticket_no}</span>
            {ticket.related_order && (
              <a href={href(`/orders/${ticket.related_order.id}`)}>关联订单 {ticket.related_order.order_no} ›</a>
            )}
          </div>
        </div>
        <div className={css.acts}>
          {canWithdraw(ticket) && (
            <Btn tone="soft" size="sm" onClick={() => setConfirm('withdraw')}>
              撤回
            </Btn>
          )}
          {canClose(ticket) && (
            <Btn tone="soft" size="sm" onClick={() => setConfirm('close')}>
              问题已解决
            </Btn>
          )}
        </div>
      </header>
      {error && (
        <p className={css.error} role="alert">
          {error}
        </p>
      )}
      <ol className={css.msgs} aria-label="工单对话">
        {ticket.messages.map((m) =>
          m.author_kind === 'system' ? (
            <li key={m.id} className={css.system}>
              {m.body} · {messageTime(m.created_at)}
            </li>
          ) : (
            <li key={m.id} className={css.m} data-mine={m.author_kind === 'user' || undefined}>
              <span className={css.who}>
                {m.author_kind === 'agent' && <i aria-hidden="true">客</i>}
                {authorLabel(m)} · {messageTime(m.created_at)}
              </span>
              <p className={css.body}>{m.body}</p>
            </li>
          ),
        )}
      </ol>
      {!canReply(ticket) ? (
        // 已关闭的三种结尾都引导新建；已撤回没有 ✓ 圆标
        <div className={css.thEnd}>
          {ticket.closed_reason === 'withdrawn' ? (
            <b>你撤回了这张工单</b>
          ) : ticket.closed_reason === 'auto_closed' ? (
            <b>这张工单已自动关闭</b>
          ) : (
            <>
              <div className={css.ok} aria-hidden="true">
                ✓
              </div>
              <b>这张工单已关闭</b>
            </>
          )}
          还有问题的话，<a className={css.endLink} href={href('/tickets/new')}>提交新工单</a>
        </div>
      ) : ticket.reply_blocked ? (
        <div className={css.thWait}>
          <i aria-hidden="true" />
          已收到你的消息，请等客服回复后再继续发送。
        </div>
      ) : (
        <ReplyBox id={ticket.id} onError={setError} />
      )}
      <ConfirmModal
        open={confirm === 'withdraw'}
        title="撤回工单？"
        body="客服尚未回复，撤回后工单关闭，不计入处理记录。"
        confirmLabel="撤回工单"
        cancelLabel="再想想"
        tone="danger"
        onConfirm={doWithdraw}
        onCancel={() => setConfirm(null)}
      />
      <ConfirmModal
        open={confirm === 'close'}
        title="问题已经解决了？"
        body="关闭后不能再回复这张工单，如有新问题请重新提交。"
        confirmLabel="关闭工单"
        cancelLabel="再想想"
        onConfirm={doClose}
        onCancel={() => setConfirm(null)}
      />
    </>
  )
}

function ReplyBox({ id, onError }: { id: string; onError: (message: string | null) => void }) {
  const reply = useReplyTicket(id)
  const intentKey = useIntentKey()
  const [text, setText] = useState('')
  const [fieldError, setFieldError] = useState<string | null>(null)

  function send() {
    const body = text.trim()
    if (!body || reply.isPending) return
    if ([...body].length > REPLY_MAX) return setFieldError(`最多 ${REPLY_MAX} 字`)
    const request = { id, body }
    reply.mutate(
      { body, key: intentKey.keyFor(request) },
      {
        onSuccess: () => {
          intentKey.reset()
          setText('')
          setFieldError(null)
          onError(null)
        },
        onError: (e) => {
          if (endsIntent(e)) intentKey.reset()
          const field = e instanceof ApiError ? e.fields.body : undefined
          if (field) setFieldError(field)
          else onError(e.message || '发送失败，请稍后重试')
        },
      },
    )
  }

  // Enter 发送、Shift+Enter 换行；输入法组字时的回车是选词，不是发送
  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault()
      send()
    }
  }

  return (
    <div className={css.compose}>
      <TextArea
        aria-label="补充信息"
        placeholder="补充信息…"
        rows={1}
        fieldClassName={css.grow}
        className={css.input}
        value={text}
        error={fieldError ?? undefined}
        onChange={(e) => {
          setText(e.target.value)
          setFieldError(null)
        }}
        onKeyDown={onKeyDown}
      />
      <Btn busy={reply.isPending} disabled={!text.trim()} onClick={send}>
        发送
      </Btn>
    </div>
  )
}
