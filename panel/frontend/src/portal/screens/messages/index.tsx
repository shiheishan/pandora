/**
 * [INPUT]: 依赖 ../../../core/format 的 relativeTime，依赖 ../../../core/router 的 navigate / useHashLocation，依赖 ../../../ui 的 Button / Card / CountBadge / Empty / QueryView / Segmented / Tag / useToast，依赖 ../common/announcements 的 SEVERITY_LABEL / useAnnouncements，依赖 ../common/traffic 的 shortDate，依赖 ./api 与 ./model
 * [OUTPUT]: 默认导出 Messages 页面组件（登记表 React.lazy 的目标）
 * [POS]: portal/screens/messages 的入口：消息（门户-08）。「通知 / 公告」两个标签（?tab=announcements 直达公告），通知页签右上「全部标为已读」；通知点开先标已读、再按 code 跳页，未读有朱砂圆点与加粗；公告标题前按级别加色点（info 不加）、置顶加标签，正文直接展开
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { relativeTime } from '../../../core/format'
import { navigate, useHashLocation } from '../../../core/router'
import { Button, Card, CountBadge, Empty, QueryView, Segmented, Tag, useToast } from '../../../ui'
import { SEVERITY_LABEL, useAnnouncements } from '../common/announcements'
import { shortDate } from '../common/traffic'
import { useMarkAllRead, useMarkRead, useNotifications, type Notification } from './api'
import css from './Messages.module.css'
import { messageTab, notificationTarget, type MessageTab } from './model'

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
      <Card flush className={css.listCard}>
        {tab === 'notifications' ? <NotificationList /> : <AnnouncementList />}
      </Card>
    </div>
  )
}

function ReadAll({ unread }: { unread: number }) {
  const toast = useToast()
  const readAll = useMarkAllRead()
  return (
    <Button
      size="sm"
      disabled={unread === 0}
      busy={readAll.isPending}
      onClick={() =>
        readAll.mutate(undefined, {
          onSuccess: () => toast('已全部标为已读'),
          onError: (e) => toast(e.message || '操作失败，请稍后重试', 'danger'),
        })
      }
    >
      全部标为已读
    </Button>
  )
}

// ---------------------------------------------------------------------------
// 通知：点开先标已读（不等它回来），有去处再跳
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
      empty={<Empty bare title="还没有通知" description="订单支付、工单回复、订阅到期与流量提醒会发到这里。" />}
    >
      {(d) => (
        <ul className={css.list}>
          {d.notifications.map((n) => (
            <li key={n.id}>
              <button type="button" className={css.row} data-unread={!n.read_at || undefined} onClick={() => open(n)}>
                <span className={css.dot} aria-label={n.read_at ? undefined : '未读'} />
                <span className={css.main}>
                  <span className={css.title}>{n.subject}</span>
                  {n.body && <span className={css.body}>{n.body}</span>}
                </span>
                <span className={css.at}>{n.sent_at ? relativeTime(n.sent_at) : ''}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </QueryView>
  )
}

// ---------------------------------------------------------------------------
// 公告：最多 20 条，置顶优先；published_at 可为 null（修订 R71）时不显示日期
// ---------------------------------------------------------------------------
function AnnouncementList() {
  const announcements = useAnnouncements()
  return (
    <QueryView query={announcements} rows={3} empty={<Empty bare title="暂时没有公告" description="维护通知与活动会在这里发布。" />}>
      {(list) => (
        <ul className={css.list}>
          {list.map((a) => (
            <li key={a.id} className={css.row}>
              <span className={css.dot} data-severity={a.severity} aria-label={SEVERITY_LABEL[a.severity] || undefined} />
              <span className={css.main}>
                <span className={css.title}>
                  {a.pinned && <Tag tone="brand">置顶</Tag>} {a.title}
                </span>
                {a.body && <span className={css.body}>{a.body}</span>}
              </span>
              <span className={css.at}>{a.published_at ? shortDate(a.published_at) : ''}</span>
            </li>
          ))}
        </ul>
      )}
    </QueryView>
  )
}
