/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/api 的 ApiError，依赖 ../../../core/router 的 href / navigate / useHashLocation，依赖 ../../../ui 的 Button / Card / ConfirmModal / Empty / Input / QueryView / Select / Skeleton / Tag / TextArea / useToast，依赖 ../common/intent 的 useIntentKey / endsIntent，依赖 ../common/orders 的 orderTitle / useOrder，依赖 ../index 的 PortalScreenProps，依赖 ./api 与 ./model
 * [OUTPUT]: 默认导出 Tickets 页面组件（登记表 React.lazy 的目标）
 * [POS]: portal/screens/tickets 的入口：工单支持（门户-07）。左列「提交新工单」与工单列表，右列新建表单或会话；地址驱动 #/tickets、#/tickets/new[?order=]、#/tickets/<id>，宽屏两列（列表页右侧显示第一张），< 640 列表与会话分屏、会话头有「全部工单」返回。按钮可见条件照后端判定，撤回与关闭都先确认；客服按待决 D-F-2 未决前显示「客服」
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState, type KeyboardEvent } from 'react'
import { ApiError } from '../../../core/api'
import { href, navigate, useHashLocation } from '../../../core/router'
import { Button, Card, ConfirmModal, Empty, Input, QueryView, Select, Skeleton, Tag, TextArea, useToast } from '../../../ui'
import { endsIntent, useIntentKey } from '../common/intent'
import { orderTitle, useOrder } from '../common/orders'
import type { PortalScreenProps } from '../index'
import { useCategories, useCloseTicket, useCreateTicket, useRecentOrders, useReplyTicket, useTicket, useTickets, useWithdrawTicket, type TicketDetail, type TicketRow } from './api'
import { authorLabel, canClose, canReply, canWithdraw, messageTime, REPLY_MAX, ticketRoute, ticketStatus, validateTicket, type TicketErrors } from './model'
import css from './Tickets.module.css'

export default function Tickets({ rest }: PortalScreenProps) {
  const { query } = useHashLocation()
  const route = ticketRoute(rest, query)
  const tickets = useTickets()
  const names = useCategoryNames()
  const first = tickets.data?.[0]
  const selected = route.kind === 'ticket' ? route.id : route.kind === 'list' ? first?.id : undefined

  return (
    <div className={css.layout} data-view={route.kind === 'list' ? 'list' : 'detail'}>
      <div className={css.listPane}>
        <Button variant="outline" block onClick={() => navigate('/tickets/new')}>
          ＋ 提交新工单
        </Button>
        <QueryView query={tickets} rows={3} empty={<Empty title="还没有工单" description="遇到连接、账单或账号问题，提交工单后客服会在这里回复你。" />}>
          {(rows) => (
            <nav className={css.list} aria-label="我的工单">
              {rows.map((t) => (
                <TicketItem key={t.id} ticket={t} category={names(t.category)} current={route.kind !== 'new' && t.id === selected} />
              ))}
            </nav>
          )}
        </QueryView>
      </div>
      <div className={css.detailPane}>
        {route.kind === 'new' ? (
          <NewTicket orderId={route.orderId} />
        ) : selected ? (
          <TicketView key={selected} id={selected} category={names} />
        ) : tickets.isSuccess ? (
          // 宽屏且一张工单都没有：右侧直接给新建表单
          <NewTicket orderId={null} />
        ) : (
          <Skeleton height={320} radius="var(--radius-lg)" />
        )}
      </div>
    </div>
  )
}

/** 分类 code → 名称；分类接口没回来时先显示 code */
function useCategoryNames(): (code: string) => string {
  const categories = useCategories()
  return (code) => categories.data?.find((c) => c.code === code)?.name ?? code
}

function TicketItem({ ticket, category, current }: { ticket: TicketRow; category: string; current: boolean }) {
  const status = ticketStatus(ticket)
  return (
    <a href={href(`/tickets/${ticket.id}`)} className={css.item} aria-current={current || undefined} data-withdrawn={ticket.closed_reason === 'withdrawn' || undefined}>
      <span className={css.itemMeta}>
        <span className={css.ticketNo}>#{ticket.ticket_no}</span>
        <Tag tone={status.tone}>{status.label}</Tag>
        <span className={css.itemCategory}>{category}</span>
      </span>
      <span className={css.itemTitle}>{ticket.subject}</span>
    </a>
  )
}

// ---------------------------------------------------------------------------
// 会话
// ---------------------------------------------------------------------------
function TicketView({ id, category }: { id: string; category: (code: string) => string }) {
  const ticket = useTicket(id)
  return (
    <Card flush className={css.thread}>
      <a href={href('/tickets')} className={css.back}>
        ← 全部工单
      </a>
      <QueryView query={ticket} rows={4} empty={null}>
        {(t) => <Thread ticket={t} category={category(t.category)} />}
      </QueryView>
    </Card>
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
      await close.mutateAsync(closeKey({ close: ticket.id }))
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
          <p className={css.headMeta}>
            #{ticket.ticket_no} · {category} · {status.label}
            {ticket.related_order && (
              <>
                {' · '}
                <a href={href(`/orders/${ticket.related_order.id}`)}>关联订单 {ticket.related_order.order_no}</a>
              </>
            )}
          </p>
        </div>
        <div className={css.headActions}>
          {canWithdraw(ticket) && (
            <Button size="sm" onClick={() => setConfirm('withdraw')}>
              撤回
            </Button>
          )}
          {canClose(ticket) && (
            <Button size="sm" onClick={() => setConfirm('close')}>
              问题已解决
            </Button>
          )}
        </div>
      </header>
      {error && (
        <p className={css.error} role="alert">
          {error}
        </p>
      )}
      <ol className={css.messages} aria-label="工单对话">
        {ticket.messages.map((m) =>
          m.author_kind === 'system' ? (
            <li key={m.id} className={css.system}>
              {m.body} · {messageTime(m.created_at)}
            </li>
          ) : (
            <li key={m.id} className={css.message} data-mine={m.author_kind === 'user' || undefined}>
              <span className={css.who}>
                {authorLabel(m)} · {messageTime(m.created_at)}
              </span>
              <span className={css.bubble}>{m.body}</span>
            </li>
          ),
        )}
      </ol>
      {canReply(ticket) ? <ReplyBox id={ticket.id} onError={setError} /> : <p className={css.closedNote}>工单已关闭，如需继续请新建工单。</p>}
      <ConfirmModal
        open={confirm === 'withdraw'}
        title="撤回工单？"
        body="客服尚未回复，撤回后工单关闭，不计入处理记录。"
        confirmLabel="撤回工单"
        onConfirm={doWithdraw}
        onCancel={() => setConfirm(null)}
      />
      <ConfirmModal
        open={confirm === 'close'}
        title="问题已经解决了？"
        body="关闭后不能再回复这张工单，如有新问题请重新提交。"
        confirmLabel="关闭工单"
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
      { body, key: intentKey(request) },
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

  // 输入法组字时的回车是选词，不是发送
  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter' && !e.nativeEvent.isComposing) send()
  }

  return (
    <div className={css.reply}>
      <Input
        aria-label="补充信息"
        placeholder="补充信息…"
        fieldClassName={css.grow}
        value={text}
        error={fieldError ?? undefined}
        onChange={(e) => {
          setText(e.target.value)
          setFieldError(null)
        }}
        onKeyDown={onKeyDown}
      />
      <Button variant="primary" busy={reply.isPending} disabled={!text.trim()} onClick={send}>
        发送
      </Button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 新建：分类（接口返回的六类）、关联订单（可选）、标题、详细描述
// ---------------------------------------------------------------------------
function NewTicket({ orderId }: { orderId: string | null }) {
  const toast = useToast()
  const categories = useCategories()
  const create = useCreateTicket()
  const intentKey = useIntentKey()
  const [category, setCategory] = useState('general')
  const [order, setOrder] = useState(orderId ?? '')
  const [subject, setSubject] = useState('')
  const [body, setBody] = useState('')
  const [errors, setErrors] = useState<TicketErrors & { order?: string; form?: string }>({})

  function submit() {
    const found = validateTicket({ subject, body })
    setErrors(found)
    if (found.subject || found.body) return
    const request = { subject: subject.trim(), category, body: body.trim(), ...(order ? { order_id: order } : {}) }
    create.mutate(
      { body: request, key: intentKey(request) },
      {
        onSuccess: (t) => {
          intentKey.reset()
          toast('工单已提交')
          navigate(`/tickets/${t.id}`, { replace: true })
        },
        onError: (e) => {
          if (endsIntent(e)) intentKey.reset()
          const f = e instanceof ApiError ? e.fields : {}
          if (f.subject || f.body || f.order_id || f.category) setErrors({ subject: f.subject, body: f.body, order: f.order_id, form: f.category })
          else setErrors({ form: e.message || '提交失败，请稍后重试' })
        },
      },
    )
  }

  return (
    <Card className={css.form}>
      <a href={href('/tickets')} className={css.back}>
        ← 全部工单
      </a>
      <h2 className={css.formTitle}>提交新工单</h2>
      <div className={css.field}>
        <span className={css.caption} id="ticket-category">
          问题分类
        </span>
        {categories.isPending ? (
          <Skeleton height={34} width={320} />
        ) : (
          <div className={css.chips} role="radiogroup" aria-labelledby="ticket-category">
            {(categories.data ?? [{ code: 'general', name: '一般咨询' }]).map((c) => (
              <button key={c.code} type="button" role="radio" aria-checked={category === c.code} className={css.chip} onClick={() => setCategory(c.code)}>
                {c.name}
              </button>
            ))}
          </div>
        )}
      </div>
      <OrderPicker value={order} onChange={setOrder} error={errors.order} />
      <Input
        label="标题"
        placeholder="一句话描述问题"
        maxLength={120}
        value={subject}
        error={errors.subject}
        onChange={(e) => {
          setSubject(e.target.value)
          setErrors((x) => ({ ...x, subject: undefined }))
        }}
      />
      <TextArea
        label="详细描述"
        rows={6}
        placeholder="详细描述：使用的客户端、节点、出现时间…"
        maxLength={5000}
        value={body}
        error={errors.body}
        hint={body.trim() ? `${[...body.trim()].length} / 5000 字` : '至少 10 个字'}
        onChange={(e) => {
          setBody(e.target.value)
          setErrors((x) => ({ ...x, body: undefined }))
        }}
      />
      {errors.form && (
        <p className={css.error} role="alert">
          {errors.form}
        </p>
      )}
      <div className={css.formActions}>
        <Button variant="primary" busy={create.isPending} onClick={submit}>
          提交
        </Button>
      </div>
    </Card>
  )
}

/** 关联订单（可选）：最近 20 张；从订单页带进来的订单不在其中时单独取一次拿订单号 */
function OrderPicker({ value, onChange, error }: { value: string; onChange: (id: string) => void; error?: string }) {
  const recent = useRecentOrders()
  const list = recent.data ?? []
  const missing = value && recent.isSuccess && !list.some((o) => o.id === value) ? value : undefined
  const extra = useOrder(missing)
  // 「不关联」是一个可选回的选项，不用 Select 的占位（占位项不可再选）
  const options = [
    { value: '', label: recent.isPending ? '不关联订单（订单读取中…）' : '不关联订单' },
    ...(missing ? [{ value: missing, label: extra.data ? `${extra.data.order_no} · ${orderTitle(extra.data)}` : '订单读取中…' }] : []),
    ...list.map((o) => ({ value: o.id, label: `${o.order_no} · ${orderTitle(o)}` })),
  ]
  return (
    <Select
      label="关联订单（可选）"
      options={options}
      value={value}
      error={error}
      hint="账单、支付类问题关联上订单，客服能更快核对"
      onChange={(e) => onChange(e.target.value)}
    />
  )
}
