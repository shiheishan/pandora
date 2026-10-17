import { useId, useState, type KeyboardEvent } from 'react'
import { ApiError } from '../../../core/api'
import { href, navigate } from '../../../core/router'
import { Input, Select, Skeleton, TextArea, useToast } from '../../../ui'
import { Btn } from '../../kit'
import { endsIntent, useIntentKey } from '../common/intent'
import { orderTitle, useOrder } from '../common/orders'
import { useCategories, useCreateTicket, useRecentOrders } from './api'
import { validateTicket, type TicketErrors } from './model'
import css from './Tickets.module.css'

export function NewTicket({ orderId }: { orderId: string | null }) {
  const toast = useToast()
  const categories = useCategories()
  const create = useCreateTicket()
  const intentKey = useIntentKey()
  const catId = useId()
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
      { body: request, key: intentKey.keyFor(request) },
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

  const cats = categories.data ?? [{ code: 'general', name: '一般咨询' }]

  // 方向键在分类之间移动并聚焦（radiogroup 约定）
  const onCatsKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const delta = e.key === 'ArrowRight' || e.key === 'ArrowDown' ? 1 : e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? -1 : 0
    if (!delta) return
    e.preventDefault()
    const codes = cats.map((c) => c.code)
    const next = (codes.indexOf(category) + delta + codes.length) % codes.length
    setCategory(codes[next]!)
    e.currentTarget.querySelectorAll<HTMLButtonElement>('[role="radio"]')[next]?.focus()
  }

  return (
    <article className={css.th}>
      <a href={href('/tickets')} className={css.back}>
        ‹ 全部工单
      </a>
      <div className={css.nf}>
        <h2 className={css.nfTitle}>提交新工单</h2>
        <p className={css.faint}>客服回复后会发消息提醒你。</p>
        <span className={css.lbl} id={catId}>
          问题分类
        </span>
        {categories.isPending ? (
          <Skeleton height={34} width={320} />
        ) : (
          <div className={css.cats} role="radiogroup" aria-labelledby={catId} onKeyDown={onCatsKeyDown}>
            {cats.map((c) => (
              <button key={c.code} type="button" role="radio" aria-checked={category === c.code} className={css.cat} onClick={() => setCategory(c.code)}>
                {c.name}
              </button>
            ))}
          </div>
        )}
        <div className={css.fields}>
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
            placeholder="详细描述：用的是哪个 App、哪个节点、什么时候出现的…"
            maxLength={5000}
            value={body}
            error={errors.body}
            onChange={(e) => {
              setBody(e.target.value)
              setErrors((x) => ({ ...x, body: undefined }))
            }}
          />
        </div>
        {errors.form && (
          <p className={css.error} role="alert">
            {errors.form}
          </p>
        )}
        <div className={css.foot}>
          <span className={css.faint}>
            至少 10 个字 · {[...body.trim()].length} / 5000
          </span>
          <Btn tone="hot" busy={create.isPending} onClick={submit}>
            提交
          </Btn>
        </div>
      </div>
    </article>
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
