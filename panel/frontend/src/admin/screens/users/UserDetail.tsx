import { CalendarAdd01Icon, ChartHistogramIcon, Wallet01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { useState } from 'react'
import { formatDateTime } from '../../../core/format'
import { navigate } from '../../../core/router'
import { Button, QueryView, Tag } from '../../../ui'
import { Avatar, Callout, CopyButton, DetailPage, StatusDot } from '../../kit'
import { useInvalidateUsers, useUser, type UserDetail as UserDetailData } from './api'
import css from './Detail.module.css'
import { dotTone, headerTarget } from './detail'
import { GrantTrafficDialog } from './GrantTrafficDialog'
import { ProfileTab } from './ProfileTab'
import { RiskTab } from './RiskTab'
import { OrdersTab, SubscriptionsTab } from './tabs'
import { initial, shortId, USER_STATUS_VIEW, userLabel } from './model'
import { UserSide } from './UserSide'

export type DetailTab = 'profile' | 'sub' | 'orders' | 'risk'

export function isDetailTab(v: string | undefined): v is DetailTab {
  return v === 'profile' || v === 'sub' || v === 'orders' || v === 'risk'
}

const DETAIL_TABS: ReadonlyArray<{ value: DetailTab; label: string }> = [
  { value: 'profile', label: '资料' },
  { value: 'sub', label: '订阅与设备' },
  { value: 'orders', label: '订单与余额' },
  { value: 'risk', label: '风控' },
]

/** 用户详情整页（kit DetailPage）：加载 / 出错 / 404 由 QueryView 接住，拿到数据进 Body */
export function UserDetail({ id, tab, onTab, backHref, now }: { id: string; tab: DetailTab; onTab: (t: DetailTab) => void; backHref: string; now: Date }) {
  const q = useUser(id)
  const d = q.data as UserDetailData | undefined
  if (!d) {
    return (
      <DetailPage back={{ href: backHref, label: '用户列表' }} title="用户详情">
        <QueryView query={q} rows={6} isEmpty={() => false} empty={null}>{() => null}</QueryView>
      </DetailPage>
    )
  }
  return <Body key={d.id} d={d} tab={tab} onTab={onTab} backHref={backHref} now={now} />
}

function Body({ d, tab, onTab, backHref, now }: { d: UserDetailData; tab: DetailTab; onTab: (t: DetailTab) => void; backHref: string; now: Date }) {
  const [balanceOpen, setBalanceOpen] = useState(false)
  const [grantOpen, setGrantOpen] = useState(false)
  const invalidate = useInvalidateUsers()
  const deleted = d.status === 'deleted'
  const st = USER_STATUS_VIEW[d.status]
  const { target, renewLabel, reason } = headerTarget(d.subscriptions)

  const adjustBalance = () => {
    setBalanceOpen(true)
    onTab('orders')
  }

  return (
    <>
      <DetailPage
        back={{ href: backHref, label: '用户列表' }}
        lead={<Avatar size="lg" text={deleted ? '#' : initial(d.email, d.display_name)} />}
        title={userLabel(d)}
        meta={
          <>
            <span className={css.mono}>{shortId(d.id)}</span>
            <CopyButton value={d.id} label="复制用户 ID" />
            <StatusDot tone={dotTone(st.tone)}>{st.label}</StatusDot>
            {d.is_admin && <Tag>管理员</Tag>}
            {d.status === 'banned' && <Tag tone="danger">封禁</Tag>}
          </>
        }
        actions={
          deleted
            ? undefined
            : (
              <>
                <Button size="sm" icon={CalendarAdd01Icon} disabled={!target} title={reason ?? undefined} onClick={() => target && navigate('/billing/orders', { query: { new: d.id, sub: target.id } })}>
                  {renewLabel}
                </Button>
                <Button size="sm" icon={ChartHistogramIcon} disabled={!target} title={reason ?? undefined} onClick={() => setGrantOpen(true)}>
                  加流量
                </Button>
                <Button size="sm" icon={Wallet01Icon} onClick={adjustBalance}>
                  调整余额
                </Button>
              </>
            )
        }
        tabs={{ label: '用户详情', items: DETAIL_TABS, value: tab, onChange: onTab }}
        side={<UserSide d={d} now={now} onAdjustBalance={adjustBalance} />}
      >
        {deleted && (
          <Callout>
            {d.deleted_at
              ? `该账号已于 ${formatDateTime(d.deleted_at).slice(0, 10)} 注销，资料已匿名化；订单与账本保留。`
              : '该账号已注销，资料已匿名化；订单与账本保留。'}
          </Callout>
        )}
        {d.admin_note && <Callout>备注：{d.admin_note}</Callout>}
        {tab === 'profile' && <ProfileTab d={d} now={now} onViewOrders={() => onTab('orders')} />}
        {tab === 'sub' && <SubscriptionsTab d={d} now={now} />}
        {tab === 'orders' && <OrdersTab d={d} now={now} balanceOpen={balanceOpen} onBalanceOpen={setBalanceOpen} />}
        {tab === 'risk' && <RiskTab d={d} now={now} />}
      </DetailPage>
      <GrantTrafficDialog user={d} subscription={grantOpen ? target ?? null : null} onClose={() => setGrantOpen(false)} onDone={() => void invalidate()} />
    </>
  )
}
