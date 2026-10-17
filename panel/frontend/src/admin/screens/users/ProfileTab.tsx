import { useState } from 'react'
import { formatMoney } from '../../../core/format'
import { navigate } from '../../../core/router'
import { Button, Empty, StatStrip, Table, Tag } from '../../../ui'
import { Callout, DangerRow, SectionTitle } from '../../kit'
import { useInvalidateUsers, type UserDetail } from './api'
import css from './Detail.module.css'
import { planMetaLine } from './detail'
import { StatusDialog } from './dialogs'
import { DeleteDialog } from './UserEditDialogs'
import {
  currentSubscription,
  ORDER_STATUS_VIEW,
  orderWhat,
  SUB_STATUS_VIEW,
  subName,
  trafficQuota,
  trafficView,
} from './model'

export function ProfileTab({ d, now, onViewOrders }: { d: UserDetail; now: Date; onViewOrders: () => void }) {
  const invalidate = useInvalidateUsers()
  const [dialog, setDialog] = useState<'status' | 'delete' | null>(null)
  const deleted = d.status === 'deleted'
  const banned = d.status === 'banned'
  const disabled = d.status === 'suspended' || banned
  const s = currentSubscription(d.subscriptions)
  const quota = s ? trafficQuota(s.quotas) : undefined
  const traf = quota ? trafficView(quota.limit, quota.consumed) : null
  const recent = d.recent_orders.slice(0, 3)

  return (
    <>
      <StatStrip
        label="用户统计"
        items={[
          { label: '累计实付', value: formatMoney(d.stats.paid_total) },
          { label: '订单数', value: String(d.stats.order_count) },
          { label: '邀请人数', value: String(d.stats.referral_count) },
        ]}
      />

      <section>
        <SectionTitle>当前套餐</SectionTitle>
        <div className={css.plan}>
          {s ? (
            <>
              <div className={css.planTitle}>
                <span>{subName(s)}</span>
                <span className={css.planSpacer} />
                <Tag tone={SUB_STATUS_VIEW[s.status].tone}>{SUB_STATUS_VIEW[s.status].label}</Tag>
              </div>
              <div className={css.planMeta}>{planMetaLine(s, now)}</div>
              {quota && traf && (
                <div className={css.traf}>
                  <span className={css.mono}>{traf.text}</span>
                  <div className={css.bar}>
                    <i
                      className={traf.tone === 'warn' ? css.barWarn : traf.tone === 'danger' ? css.barDanger : undefined}
                      style={{ width: `${traf.percent}%` }}
                    />
                  </div>
                </div>
              )}
            </>
          ) : (
            <p className={css.planEmpty}>没有订阅</p>
          )}
        </div>
      </section>

      <section>
        <SectionTitle action={d.recent_orders.length > 0 ? <Button size="xs" variant="link" onClick={onViewOrders}>查看全部</Button> : undefined}>
          最近订单
        </SectionTitle>
        <Table
          boxed
          label="最近订单"
          rowKey={(o) => o.id}
          rows={recent}
          onRowClick={(o) => navigate(`/billing/orders/${encodeURIComponent(o.id)}`)}
          empty={<Empty bare title="还没有订单" description="用户在用户端下单，或在订单页人工开单后，这里会出现。" />}
          columns={[
            {
              key: 'no',
              header: '订单号',
              mono: true,
              render: (o) => (
                <span className={css.oid}>
                  {o.order_no}
                  {o.manual && <Tag tone="outline">人工</Tag>}
                </span>
              ),
            },
            {
              key: 'what',
              hideOnMobile: true,
              header: '内容',
              render: (o) => <span className={css.dim}>{orderWhat(o)}</span>,
            },
            {
              key: 'amt',
              header: '金额',
              align: 'right',
              mono: true,
              render: (o) => <span className={css.amt}>{formatMoney(o.paid_amount || o.payable_amount)}</span>,
            },
            {
              key: 'st',
              header: '状态',
              align: 'right',
              render: (o) => <Tag tone={ORDER_STATUS_VIEW[o.status].tone}>{ORDER_STATUS_VIEW[o.status].label}</Tag>,
            },
          ]}
        />
      </section>

      {!deleted && (
        <section className={css.disposal} aria-label="账号处置">
          {d.status === 'active' && (
            <DangerRow
              title="禁用账号"
              description="暂停登录与订阅连接，数据保留，可随时启用"
              actionLabel="禁用账号"
              onAction={() => setDialog('status')}
            />
          )}
          {disabled && (
            <Callout action={<Button size="sm" onClick={() => setDialog('status')}>启用账号</Button>}>
              {banned ? '账号已封禁' : '账号已禁用'}，登录与订阅连接已暂停。
            </Callout>
          )}
          {!d.is_admin && (
            <DangerRow
              title="注销账号"
              description="匿名化资料并永久停用；订单与账本保留，不可撤销。"
              actionLabel="注销账号"
              onAction={() => setDialog('delete')}
            />
          )}
        </section>
      )}

      <StatusDialog user={d} open={dialog === 'status'} onClose={() => setDialog(null)} onDone={() => void invalidate()} />
      <DeleteDialog user={d} open={dialog === 'delete'} onClose={() => setDialog(null)} onDone={() => void invalidate()} />
    </>
  )
}
