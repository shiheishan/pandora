import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { formatDateTime } from '../../../core/format'
import { Button, Empty, Modal, Skeleton, Tabs, Tag } from '../../../ui'
import { useUser, type UserDetail as UserDetailData } from './api'
import css from './Detail.module.css'
import { ProfileTab } from './ProfileTab'
import { RiskTab } from './RiskTab'
import { OrdersTab, SubscriptionsTab } from './tabs'
import { initial, shortId, USER_STATUS_VIEW, userLabel } from './model'

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

export function UserDetail({ id, tab, onTab, onClose, now }: { id: string | null; tab: DetailTab; onTab: (t: DetailTab) => void; onClose: () => void; now: Date }) {
  const q = useUser(id)
  const d = q.data as UserDetailData | undefined
  return (
    <Modal
      open={id !== null}
      onClose={onClose}
      size={760}
      tall
      divided
      title={d ? <DetailTitle d={d} /> : '用户详情'}
      subtitle={d ? `用户 ID #${shortId(d.id)} · 注册时间 ${formatDateTime(d.created_at).slice(0, 10)}` : undefined}
    >
      {q.isPending ? (
        <div className={css.skeleton} role="status" aria-label="加载中">
          <Skeleton height={30} />
          <Skeleton height={80} />
          <Skeleton height={160} />
        </div>
      ) : q.isError ? (
        isApiError(q.error, 'not_found') ? (
          <Empty bare title="用户不存在" description="它可能已被删除，或链接有误。" />
        ) : (
          <Empty
            bare
            title="用户详情读取失败"
            description={q.error.message}
            action={
              <Button size="sm" onClick={() => void q.refetch()}>
                重试
              </Button>
            }
          />
        )
      ) : (
        <Body key={d!.id} d={d!} tab={tab} onTab={onTab} now={now} />
      )}
    </Modal>
  )
}

function DetailTitle({ d }: { d: UserDetailData }) {
  const st = USER_STATUS_VIEW[d.status]
  const deleted = d.status === 'deleted'
  return (
    <span className={css.titleRow}>
      <span className={css.avatar} aria-hidden="true">
        {deleted ? '#' : initial(d.email, d.display_name)}
      </span>
      <span className={css.userName}>{userLabel(d)}</span>
      <Tag tone={st.tone}>{st.label}</Tag>
      {d.status === 'banned' && <Tag tone="danger">封禁</Tag>}
    </span>
  )
}

function Body({ d, tab, onTab, now }: { d: UserDetailData; tab: DetailTab; onTab: (t: DetailTab) => void; now: Date }) {
  const [balanceOpen, setBalanceOpen] = useState(false)
  const current: DetailTab = isDetailTab(tab) ? tab : 'profile'
  return (
    <div className={css.body}>
      {d.status === 'deleted' && (
        <p className={css.notice}>
          {d.deleted_at
            ? `该账号已于 ${formatDateTime(d.deleted_at).slice(0, 10)} 注销，资料已匿名化；订单与账本保留。`
            : '该账号已注销，资料已匿名化；订单与账本保留。'}
        </p>
      )}
      {d.admin_note && (
        <p className={css.notice} aria-label="后台备注">
          备注：{d.admin_note}
        </p>
      )}
      <Tabs label="用户详情" items={DETAIL_TABS} value={current} onChange={onTab} />
      <div className={css.tabPanel} role="tabpanel">
        {current === 'profile' && <ProfileTab d={d} now={now} onAdjustBalance={() => { setBalanceOpen(true); onTab('orders') }} />}
        {current === 'sub' && <SubscriptionsTab d={d} now={now} />}
        {current === 'orders' && <OrdersTab d={d} now={now} balanceOpen={balanceOpen} onBalanceOpen={setBalanceOpen} />}
        {current === 'risk' && <RiskTab d={d} now={now} />}
      </div>
    </div>
  )
}
