import { useEffect, useState } from 'react'
import { formatMoney, relativeTime } from '../../../core/format'
import { href } from '../../../core/router'
import { Note, Sheet } from '../../kit'
import { useAnnouncements, type Announcement } from '../common/announcements'
import { expiryNote, orderTitle, useOpenOrders } from '../common/orders'
import css from './Subs.module.css'

export function TopNotes() {
  const [reading, setReading] = useState<Announcement | null>(null)
  const [now, setNow] = useState(() => new Date())

  useEffect(() => {
    const timer = setInterval(() => {
      setNow(new Date())
    }, 30_000)
    return () => clearInterval(timer)
  }, [])

  const announcementsQuery = useAnnouncements()
  // 与外框侧栏的待支付数同一个查询（useOpenOrders 同键）
  const pendingOrdersQuery = useOpenOrders()

  const criticalAnnouncements = (announcementsQuery.data ?? []).filter(
    (a) => a.severity === 'critical',
  )
  const pendingOrders = pendingOrdersQuery.data?.orders ?? []

  if (criticalAnnouncements.length === 0 && pendingOrders.length === 0) {
    return null
  }

  return (
    <>
      <div className={css.notes}>
        {criticalAnnouncements.map((a) => (
          <Note
            key={a.id}
            tag="重要"
            tagTone="danger"
            go="查看 ›"
            onClick={() => setReading(a)}
          >
            {a.title}
          </Note>
        ))}
        {pendingOrders.map((o) => (
          <Note
            key={o.id}
            tag="待支付"
            tagTone="warn"
            href={href(`/orders/${o.id}`)}
            go="去支付 ›"
            sub={expiryNote(o.expires_at, now)}
          >
            {orderTitle(o)} <b className={css.num}>{formatMoney(o.payable_amount)}</b>
          </Note>
        ))}
      </div>
      <Sheet
        open={reading !== null}
        onClose={() => setReading(null)}
        title={reading?.title ?? ''}
      >
        <p className={css.eyebrowLine}>
          公告{reading?.published_at ? ` · ${relativeTime(reading.published_at)}` : ''}
        </p>
        <p className={css.annBody}>{reading?.body}</p>
      </Sheet>
    </>
  )
}
