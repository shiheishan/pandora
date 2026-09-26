/**
 * [INPUT]: 依赖 react 的 useEffect / useState，依赖 ../../../core/format 的 formatBytes / formatMoney / relativeTime，依赖 ../../../core/router 的 href，依赖 ../../../ui 的 Button / Card / Empty / Modal / Skeleton / Tag / useToast，依赖 ../../queries 的 useBalance / useCommissionAvailable，依赖 ../common 下的订阅、订单、公告读模型（含 SEVERITY_LABEL）与 Slot / LoadError / UsageCard
 * [OUTPUT]: 默认导出 Overview 页面组件（登记表 React.lazy 的目标）
 * [POS]: portal/screens/overview 的入口：概览（门户-01）。顶部插槽与 critical 公告横幅、待支付条、当前套餐主卡（剩余流量含流量包、到期、重置日、导入 / 复制 / 续费）、三格统计（余额 / 可提佣金 / 在线设备）、公告卡、本期用量图、底部插槽；主卡展示 current_period_end 最晚的生效订阅
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useEffect, useState } from 'react'
import { formatBytes, formatMoney, relativeTime } from '../../../core/format'
import { href } from '../../../core/router'
import { Button, Card, Empty, Modal, Skeleton, Tag, useToast } from '../../../ui'
import { useBalance, useCommissionAvailable } from '../../queries'
import { SEVERITY_LABEL, useAnnouncements, type Announcement } from '../common/announcements'
import { LoadError, Slot } from '../common/Blocks'
import { copyText } from '../common/clients'
import { expiryNote, orderTitle, usePendingOrders } from '../common/orders'
import { canRenew, pickPrimary, usePlanTraffic, useSubscriptionLinks, useSubscriptions, type Subscription } from '../common/subscriptions'
import { bytesParts, daysUntil, expiryInfo, shortDate, usageLevel } from '../common/traffic'
import { UsageCard } from '../common/UsageCard'
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
  const subs = useSubscriptions()
  const primary = subs.data ? pickPrimary(subs.data) : null
  const [reading, setReading] = useState<Announcement | null>(null)

  return (
    <div className={css.page}>
      <Slot name="portal.home.banner" />
      <CriticalBanners onOpen={setReading} />
      <PendingOrders />
      <div className={css.top}>
        {subs.isPending ? (
          <Card tint aria-busy="true" className={css.plan}>
            <Skeleton width={120} height={26} />
            <Skeleton width={200} height={40} />
            <Skeleton height={8} />
            <Skeleton width={260} height={40} radius="var(--radius-md)" />
          </Card>
        ) : subs.isError ? (
          <Card tint className={css.plan}>
            <LoadError error={subs.error} onRetry={() => void subs.refetch()} what="套餐" />
          </Card>
        ) : primary ? (
          <PlanCard sub={primary} />
        ) : (
          <Card tint className={css.plan}>
            <Empty
              bare
              title="您还没有生效中的套餐"
              description="选购一个套餐，拿到订阅地址后导入客户端即可使用。"
              action={
                <a className={css.emptyAction} href={href('/plans')}>
                  选购套餐
                </a>
              }
            />
          </Card>
        )}
        <div className={css.side}>
          <Stats sub={primary} loading={subs.isPending} />
          <AnnouncementsCard onOpen={setReading} />
        </div>
      </div>
      {primary && <PrimaryUsage sub={primary} />}
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
// 当前套餐主卡
// ---------------------------------------------------------------------------
function PlanCard({ sub }: { sub: Subscription }) {
  const toast = useToast()
  const links = useSubscriptionLinks()
  const { summary, resetAt, timeZone } = usePlanTraffic(sub)
  const expiry = expiryInfo(sub.current_period_end, new Date(), timeZone)
  const link = links.data?.find((l) => l.subscription_id === sub.id)
  const level = summary ? usageLevel(summary.ratio) : 'ok'
  const left = bytesParts(summary ? (summary.remaining ?? 0) + summary.pack : 0)
  const renewHref = canRenew(sub) ? href('/checkout', { renew: sub.id }) : null

  async function copy() {
    if (!link) {
      toast(links.isPending ? '订阅地址还在加载，稍后再试' : '还没有订阅地址，请到「我的订阅」重新生成', 'danger')
      return
    }
    const ok = await copyText(link.url)
    toast(ok ? '订阅地址已复制' : '复制失败，请到「我的订阅」手动复制', ok ? 'ok' : 'danger')
  }

  return (
    <Card tint className={css.plan}>
      <div className={css.planHead}>
        <div className={css.planName}>
          <div className={css.caption}>当前套餐</div>
          <div className={css.planTitle}>{sub.plan_name}</div>
        </div>
        <div className={css.planBadges}>
          {sub.status === 'grace' && <Tag tone="warn">宽限期</Tag>}
          {sub.status === 'past_due' && <Tag tone="danger">待续费</Tag>}
          {expiry && <Tag tone={expiry.urgent ? 'warn' : 'ok'}>{expiry.urgent ? `${expiry.days} 天后到期` : `${expiry.days} 天后到期 · ${expiry.date.slice(5)}`}</Tag>}
          {!expiry && <Tag tone="ok">长期有效</Tag>}
          {renewHref && !expiry?.urgent && (
            <a className={css.textLink} href={renewHref}>
              续费
            </a>
          )}
        </div>
      </div>

      {summary ? (
        <div className={css.traffic}>
          <div className={css.trafficRow}>
            <div className={css.trafficLeft}>
              <div className={css.caption}>剩余流量</div>
              <div className={css.bigNumber} data-level={level}>
                {summary.remaining === null ? (
                  '不限'
                ) : (
                  <>
                    {left[0]}
                    <span className={css.unit}>{left[1]}</span>
                  </>
                )}
              </div>
            </div>
            <div className={css.trafficUsed}>
              已用 {formatBytes(summary.used)} / {summary.total === null ? '不限' : formatBytes(summary.total)}
            </div>
          </div>
          {summary.total !== null && (
            <div className={css.meter} role="progressbar" aria-label="本期已用" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(summary.ratio * 100)}>
              <div className={css.meterFill} data-level={level} style={{ width: `${summary.ratio * 100}%` }} />
            </div>
          )}
          <div className={css.trafficNotes}>
            {summary.total !== null && (
              <span className={css.usedNote} data-level={level}>
                已用 {Math.round(summary.ratio * 100)}%{level === 'ok' ? '' : '，流量即将用完'}
              </span>
            )}
            {summary.pack > 0 && <span>含流量包 {formatBytes(summary.pack)}</span>}
            <span className={css.spacer} />
            {resetAt && (
              <span>
                {daysUntil(resetAt)} 天后重置（{shortDate(resetAt, timeZone)}）
              </span>
            )}
          </div>
        </div>
      ) : (
        <div className={css.caption}>这个套餐没有流量额度记录。</div>
      )}

      <div className={css.planActions}>
        <a className={css.primaryAction} href={href('/subs', { sub: sub.id })}>
          导入到客户端
        </a>
        <Button onClick={() => void copy()}>
          复制订阅地址
        </Button>
        {renewHref && expiry?.urgent && (
          <a className={css.renewAction} href={renewHref}>
            立即续费
          </a>
        )}
      </div>
    </Card>
  )
}

function PrimaryUsage({ sub }: { sub: Subscription }) {
  const { summary, resetAt } = usePlanTraffic(sub)
  return <UsageCard subscriptionId={sub.id} summary={summary} resetAt={resetAt} />
}

// ---------------------------------------------------------------------------
// 三格统计：余额与可提佣金复用外框查询（同键同 schema），在线设备取订阅待补字段
// ---------------------------------------------------------------------------
function Stats({ sub, loading }: { sub: Subscription | null; loading: boolean }) {
  const balance = useBalance()
  const commission = useCommissionAvailable()
  const devices =
    sub?.online_devices === undefined ? null : { on: sub.online_devices, max: sub.device_limit === undefined ? null : sub.device_limit === null ? '不限' : String(sub.device_limit) }

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
              {devices.max !== null && <span className={css.statUnit}> / {devices.max}</span>}
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
