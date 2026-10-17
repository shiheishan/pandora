import { ArrowLeft01Icon, ArrowUp01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { Fragment, useEffect, useRef, useState } from 'react'
import { isApiError } from '../../../core/api'
import { href } from '../../../core/router'
import { Button, Empty, Skeleton, Tag } from '../../../ui'
import { Avatar } from '../../kit'
import { useTicketDetail, type TicketDetail } from './api'
import { Composer } from './Composer'
import { useTicketControls } from './Controls'
import { CATEGORY_LABELS, CLOSED_REASON_LABELS, dayLabel, messageTime, messageView, systemText } from './model'
import { Props } from './Props'
import { useNow } from './Queue'
import css from './Detail.module.css'

function useMediaQuery(query: string): boolean {
  const [matches, setMatches] = useState(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false
    return window.matchMedia(query).matches
  })
  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return
    const mq = window.matchMedia(query)
    const onChange = () => setMatches(mq.matches)
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [query])
  return matches
}

export function Detail({ id, backHref }: { id: string | null; backHref: string }) {
  const q = useTicketDetail(id)
  if (id === null) return <Empty bare className={css.detailEmpty} title="选择一张工单" description="对话、指派与状态都在这里处理。" />
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
    // 契约：工单不存在回 404（CodeNotFound）
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
  return <Loaded key={q.data.id} t={q.data} backHref={backHref} />
}

function Loaded({ t, backHref }: { t: TicketDetail; backHref: string }) {
  const now = useNow()
  const compact = useMediaQuery('(max-width: 1279px)')
  const controls = useTicketControls(t)

  return (
    <div className={compact ? css.detail : `${css.detail} ${css.withProps}`}>
      <section className={css.conv}>
        <header className={css.head}>
          <a className={css.back} href={backHref}>
            <HugeiconsIcon icon={ArrowLeft01Icon} size={15} strokeWidth={1.8} aria-hidden />
            返回工单列表
          </a>
          <div className={css.titleRow}>
            <h2>{t.subject}</h2>
            {/* 契约：徽标以 escalated_at 为准，升级后被回复离开 escalated 状态仍然显示 */}
            {t.escalated_at && <Tag tone="warn">已升级</Tag>}
          </div>
          <div className={css.meta}>
            <span className={css.mono}>{t.ticket_no}</span>
            <span>{t.user_email}</span>
            <span>创建于 {messageTime(t.created_at, now)}</span>
            {compact && (
              <>
                {t.user_active_plan && <span>{t.user_active_plan}</span>}
                <span>{CATEGORY_LABELS[t.category]}</span>
                {t.related_order && (
                  <span>
                    关联订单{' '}
                    <a
                      className={css.mono}
                      href={href(`/billing/orders/${encodeURIComponent(t.related_order.id)}`)}
                    >
                      {t.related_order.order_no}
                    </a>
                  </span>
                )}
                {t.closed_reason && <span>{CLOSED_REASON_LABELS[t.closed_reason]}</span>}
              </>
            )}
          </div>
          {compact && (
            <div className={css.ctl}>
              <label className={css.ctlLabel}>
                状态 {controls.status}
              </label>
              <label className={css.ctlLabel}>
                指派 {controls.assign}
              </label>
              <Button
                size="sm"
                variant="secondary"
                disabled={controls.escalate.escalated}
                onClick={controls.escalate.open}
              >
                <HugeiconsIcon icon={ArrowUp01Icon} size={14} strokeWidth={1.8} aria-hidden />
                {controls.escalate.escalated ? '已升级' : '升级到 L2'}
              </Button>
            </div>
          )}
        </header>
        <Messages t={t} now={now} />
        <Composer ticket={t} />
      </section>
      {!compact && <Props t={t} controls={controls} />}
      {controls.dialogs}
    </div>
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
    <div className={css.thr} role="log" aria-label="对话记录">
      {t.messages.length === 0 && <Empty bare title="还没有消息" description="用户的第一条描述会出现在这里。" />}
      {t.messages.map((m, idx) => {
        const prev = t.messages[idx - 1]
        const showDay = !prev || dayLabel(prev.created_at, now) !== dayLabel(m.created_at, now)
        const v = messageView(m, t.user_email, now)

        if (v.kind === 'system') {
          return (
            <Fragment key={m.id}>
              {showDay && <span className={css.day}>{dayLabel(m.created_at, now)}</span>}
              <div className={css.sys}>
                {systemText(m.body)} · {v.at}
              </div>
            </Fragment>
          )
        }

        const msgKindClass = v.kind === 'user' ? css.msgUser : v.kind === 'agent' ? css.msgAgent : css.msgNote

        return (
          <Fragment key={m.id}>
            {showDay && <span className={css.day}>{dayLabel(m.created_at, now)}</span>}
            <div className={`${css.msg} ${msgKindClass}`}>
              <Avatar text={v.who.toUpperCase()} size="sm" />
              <div className={css.msgBody}>
                <div className={css.msgWho}>
                  <b>{v.who}</b> · {v.at}
                  {v.kind === 'note' && <Tag tone="warn">仅内部可见</Tag>}
                </div>
                <div className={css.bb}>{m.body}</div>
              </div>
            </div>
          </Fragment>
        )
      })}
      <div ref={end} />
    </div>
  )
}
