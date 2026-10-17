import { navigate, useHashLocation } from '../../../core/router'
import { CountBadge, Empty, QueryView, Segmented, Tag, useToast } from '../../../ui'
import { GroupHeader, IconDisc, Rows } from '../../kit'
import { SEVERITY_LABEL, useAnnouncements } from '../common/announcements'
import { shortDate } from '../common/traffic'
import { useMarkAllRead, useMarkRead, useNotifications, type Notification } from './api'
import css from './Messages.module.css'
import { groupByDay, messageTab, notificationIcon, notificationTarget, noticeTime, type MessageTab } from './model'

export default function Messages() {
  const { query } = useHashLocation()
  const tab = messageTab(query)
  const notifications = useNotifications()
  const unread = notifications.data?.unread ?? 0

  return (
    <div className={css.page}>
      <div className={css.bar}>
        <Segmented<MessageTab>
          label="消息类型"
          value={tab}
          onChange={(v) => navigate('/messages', { query: v === 'announcements' ? { tab: v } : undefined, replace: true })}
          options={[
            {
              value: 'notifications',
              label: (
                <span className={css.tabLabel}>
                  通知 <CountBadge count={unread} aria-label={`${unread} 条未读`} />
                </span>
              ),
            },
            { value: 'announcements', label: '公告' },
          ]}
        />
        {tab === 'notifications' && <ReadAll unread={unread} />}
      </div>
      {tab === 'notifications' ? <NotificationList /> : <AnnouncementList />}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 全部标为已读：文字按钮（样稿 .read-all）
// ---------------------------------------------------------------------------
function ReadAll({ unread }: { unread: number }) {
  const toast = useToast()
  const readAll = useMarkAllRead()
  return (
    <button
      type="button"
      className={css.readAll}
      disabled={unread === 0 || readAll.isPending}
      onClick={() =>
        readAll.mutate(undefined, {
          onSuccess: () => toast('已全部标为已读'),
          onError: (e) => toast(e.message || '操作失败，请稍后重试', 'danger'),
        })
      }
    >
      全部标为已读
    </button>
  )
}

// ---------------------------------------------------------------------------
// 通知：按「今天 / 更早」分组；点开先标已读（不等它回来），有去处再跳
// 已读且没有去处的（如「套餐已到期」）是不可点的 div
// ---------------------------------------------------------------------------
function NotificationList() {
  const notifications = useNotifications()
  const markRead = useMarkRead()

  function open(n: Notification) {
    if (!n.read_at) markRead.mutate(n.id)
    const target = notificationTarget(n.code)
    if (target) navigate(target)
  }

  return (
    <QueryView
      query={notifications}
      rows={3}
      isEmpty={(d) => d.notifications.length === 0}
      empty={
        <Rows>
          <Empty bare title="还没有通知" description="订单支付、工单回复、套餐到期与流量提醒会发到这里。" />
        </Rows>
      }
    >
      {(d) =>
        groupByDay(d.notifications).map((g) => (
          <section key={g.label}>
            <GroupHeader>{g.label}</GroupHeader>
            <Rows as="ul" className={css.list}>
              {g.items.map((n) => {
                const target = notificationTarget(n.code)
                const clickable = target !== null || !n.read_at
                const inner = (
                  <>
                    <IconDisc name={notificationIcon(n.code)} className={css.ntIco} />
                    <span className={css.text}>
                      {!n.read_at && <span className={css.sr}>未读：</span>}
                      <b>{n.subject}</b>
                      {n.body && <p>{n.body}</p>}
                    </span>
                    <span className={css.side}>
                      {n.sent_at ? noticeTime(n.sent_at) : ''}
                      <i aria-hidden="true" />
                    </span>
                  </>
                )
                return (
                  <li key={n.id}>
                    {clickable ? (
                      <button type="button" className={css.nt} data-unread={!n.read_at || undefined} onClick={() => open(n)}>
                        {inner}
                      </button>
                    ) : (
                      <div className={css.nt}>{inner}</div>
                    )}
                  </li>
                )
              })}
            </Rows>
          </section>
        ))
      }
    </QueryView>
  )
}

// ---------------------------------------------------------------------------
// 公告：最多 20 条，置顶优先；published_at 可为 null（修订 R71）时不显示日期。
// 等级用左侧色条 + 文字标签；正文完整显示（pre-line，不截断）
// ---------------------------------------------------------------------------
function AnnouncementList() {
  const announcements = useAnnouncements()
  return (
    <QueryView
      query={announcements}
      rows={3}
      empty={
        <Rows>
          <Empty bare title="暂时没有公告" description="维护通知与活动会在这里发布。" />
        </Rows>
      }
    >
      {(list) => (
        <Rows as="ul" className={css.anList}>
          {list.map((a) => {
            const label = SEVERITY_LABEL[a.severity]
            return (
              <li key={a.id} className={css.an} data-severity={a.severity} data-pinned={a.pinned || undefined}>
                <div className={css.anText}>
                  {(a.pinned || label) && (
                    <div className={css.tags}>
                      {a.pinned && <Tag tone="brandSolid">置顶</Tag>}
                      {a.severity === 'critical' && <Tag tone="danger">{label}</Tag>}
                      {a.severity === 'warning' && <Tag tone="warn">{label}</Tag>}
                      {a.severity === 'notice' && <Tag tone="brand">{label}</Tag>}
                    </div>
                  )}
                  <b>{a.title}</b>
                  {a.body && <p>{a.body}</p>}
                </div>
                <span className={css.date}>{a.published_at ? shortDate(a.published_at) : ''}</span>
              </li>
            )
          })}
        </Rows>
      )}
    </QueryView>
  )
}
