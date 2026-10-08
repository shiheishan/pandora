import { useEffect, useState } from 'react'
import { formatMoney, relativeTime } from '../../../core/format'
import { href } from '../../../core/router'
import { Button, Card, Empty, Modal, Skeleton, Tag } from '../../../ui'
import { useBalance, useCommissionAvailable } from '../../queries'
import { SEVERITY_LABEL, useAnnouncements, type Announcement } from '../common/announcements'
import { LoadError, Slot } from '../common/Blocks'
import { usePackCatalog } from '../common/catalog'
import { flowCss } from '../common/Flow'
import { useHoldings } from '../common/holdings'
import { expiryNote, orderTitle, usePendingOrders } from '../common/orders'
import { SubCard } from '../common/SubCard'
import { isLive, type Subscription } from '../common/subscriptions'
import { shortDate } from '../common/traffic'
import css from './Overview.module.css'

/** 倒计时文案每 30 秒刷新一次 */
function useNow(intervalMs = 30_000): Date {
  const [now, setNow] = useState(() => new Date())
  useEffect(() => {
    const timer = setInterval(() => setNow(new Date()), intervalMs)
    return () => clearInterval(timer)
  }, [intervalMs])
  return now
}

export default function Overview() {
  const h = useHoldings()
  const packs = usePackCatalog()
  const minPack = packs.data?.length ? Math.min(...packs.data.map((p) => p.unit_amount)) : null
  const [reading, setReading] = useState<Announcement | null>(null)
  const noop = () => undefined

  return (
    <div className={css.page}>
      <Slot name="portal.home.banner" />
      <CriticalBanners onOpen={setReading} />
      <PendingOrders />
      <div className={css.top}>
        <div className={css.subs}>
          {h.subs.isPending ? (
            <Card aria-busy="true">
              <Skeleton width={120} height={26} />
              <Skeleton height={8} />
              <Skeleton height={52} radius="var(--radius-md)" />
            </Card>
          ) : h.subs.isError ? (
            <Card>
              <LoadError error={h.subs.error} onRetry={() => void h.subs.refetch()} what="套餐" />
            </Card>
          ) : h.held.length ? (
            <>
              {h.held.map((sub) => (
                <SubCard key={sub.id} compact sub={sub} naming={h.naming} link={h.linkOf(sub.id)} minPack={minPack} actions={{ onImport: noop, onRename: noop, onRotate: noop }} />
              ))}
              <a className={flowCss.textButton} href={href('/subs')}>
                去我的套餐：添加到 App、换新链接、改名 →
              </a>
            </>
          ) : (
            <Card tint>
              <Empty
                bare
                title="你还没有套餐"
                description="选一个套餐，拿到链接后添加到 App 就能用。"
                action={
                  <a className={css.emptyAction} href={href('/plans')}>
                    去选购
                  </a>
                }
              />
            </Card>
          )}
        </div>
        <div className={css.side}>
          <Stats held={h.held} loading={h.subs.isPending} />
          <AnnouncementsCard onOpen={setReading} />
        </div>
      </div>
      <Slot name="portal.home.aside" />
      <Modal
        open={reading !== null}
        onClose={() => setReading(null)}
        title={reading?.title ?? ''}
        eyebrow={reading?.published_at ? `公告 · ${relativeTime(reading.published_at)}` : '公告'}
        actions={
          <Button size="dialog" onClick={() => setReading(null)} data-autofocus>
            关闭
          </Button>
        }
      >
        <p className={css.announcementBody}>{reading?.body}</p>
      </Modal>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 契约门户-08 待补·前端的建议：critical 公告在概览顶部显示横幅
// ---------------------------------------------------------------------------
function CriticalBanners({ onOpen }: { onOpen: (a: Announcement) => void }) {
  const { data } = useAnnouncements()
  const critical = data?.filter((a) => a.severity === 'critical') ?? []
  return critical.map((a) => (
    <button key={a.id} type="button" className={css.critical} onClick={() => onOpen(a)}>
      <Tag tone="danger">重要</Tag>
      <span className={css.criticalTitle}>{a.title}</span>
      <span className={css.criticalMore}>查看</span>
    </button>
  ))
}

// ---------------------------------------------------------------------------
// 待支付条：订单 30 分钟过期，按 expires_at 倒数；去支付 → 订单页
// ---------------------------------------------------------------------------
function PendingOrders() {
  const { data } = usePendingOrders()
  const now = useNow()
  if (!data?.length) return null
  return data.map((o) => (
    <div key={o.id} className={css.pending}>
      <Tag tone="warn">待支付</Tag>
      <span className={css.pendingWhat}>{orderTitle(o)}</span>
      <span className={css.pendingAmount}>{formatMoney(o.payable_amount, o.currency)}</span>
      <span className={css.pendingNote}>{expiryNote(o.expires_at, now)}</span>
      <a className={css.pendingGo} href={href(`/orders/${o.id}`)}>
        去支付 →
      </a>
    </div>
  ))
}

// ---------------------------------------------------------------------------
// 三格统计：余额与可提佣金复用外框查询（同键同 schema）；在线设备是在用的各份加起来（没有套餐时显示「—」）
// ---------------------------------------------------------------------------
function Stats({ held, loading }: { held: readonly Subscription[]; loading: boolean }) {
  const balance = useBalance()
  const commission = useCommissionAvailable()
  const live = held.filter(isLive)
  const devices = live.length
    ? { on: live.reduce((n, s) => n + s.online_devices, 0), max: live.some((s) => s.device_limit === null) ? '不限' : String(live.reduce((n, s) => n + (s.device_limit ?? 0), 0)) }
    : null

  return (
    <div className={css.stats}>
      <a className={css.stat} href={href('/wallet')}>
        <span className={css.caption}>余额</span>
        <span className={css.statValue}>{balance.data ? formatMoney(balance.data.balance, balance.data.currency) : balance.isError ? '—' : <Skeleton width={72} height={24} />}</span>
      </a>
      <a className={css.stat} href={href('/referral')}>
        <span className={css.caption}>可提佣金</span>
        <span className={css.statValue}>
          {commission.data ? formatMoney(commission.data.available, commission.data.currency) : commission.isError ? '—' : <Skeleton width={72} height={24} />}
        </span>
      </a>
      <a className={css.stat} href={href('/subs')}>
        <span className={css.caption}>在线设备</span>
        <span className={css.statValue}>
          {loading ? (
            <Skeleton width={48} height={24} />
          ) : devices ? (
            <>
              {devices.on}
              <span className={css.statUnit}> / {devices.max}</span>
            </>
          ) : (
            '—'
          )}
        </span>
      </a>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 公告：最多 20 条、置顶优先；点击看正文
// ---------------------------------------------------------------------------
function AnnouncementsCard({ onOpen }: { onOpen: (a: Announcement) => void }) {
  const list = useAnnouncements()
  return (
    <Card flush title="公告" extra={<a href={href('/messages', { tab: 'announcements' })}>全部消息</a>} className={css.announcements}>
      {list.isPending ? (
        <div className={css.announcementSkeleton}>
          <Skeleton height={16} />
          <Skeleton width="70%" height={16} />
        </div>
      ) : list.isError ? (
        <LoadError error={list.error} onRetry={() => void list.refetch()} what="公告" />
      ) : list.data.length === 0 ? (
        <Empty bare title="暂时没有公告" description="维护通知与活动会在这里发布。" />
      ) : (
        <ul className={css.announcementList}>
          {list.data.slice(0, 5).map((a) => (
            <li key={a.id}>
              <button type="button" className={css.announcement} onClick={() => onOpen(a)}>
                {a.pinned && <span className={css.pinned}>置顶</span>}
                {a.severity !== 'info' && <span className={css.severity} data-severity={a.severity} aria-label={SEVERITY_LABEL[a.severity]} />}
                <span className={css.announcementTitle}>{a.title}</span>
                {a.published_at && <span className={css.announcementAt}>{shortDate(a.published_at)}</span>}
              </button>
            </li>
          ))}
        </ul>
      )}
    </Card>
  )
}
