import { useState } from 'react'
import { formatMoney, relativeTime } from '../../../core/format'
import { href, navigate } from '../../../core/router'
import { Button, Empty, Menu, Table, Tag, type MenuEntry } from '../../../ui'
import { Callout, Row, RowList, SectionTitle } from '../../kit'
import { subActions, type SubActionKey } from './detail'
import { DeviceLimitDialog } from './DeviceLimitDialog'
import { BalanceForm, RotateDialog } from './dialogs'
import { ExtendDialog } from './ExtendDialog'
import { GrantTrafficDialog } from './GrantTrafficDialog'
import { useInvalidateUsers, type SubscriptionRow, type UserDetail } from './api'
import {
  currentSubscription,
  deviceLimitLabel,
  deviceView,
  ORDER_STATUS_VIEW,
  orderWhat,
  planKind,
  SUB_STATUS_VIEW,
  subExpiryView,
  subName,
  trafficQuota,
  trafficView,
} from './model'
import { PeriodEndDialog, QuotaLimitDialog, ResetTrafficDialog } from './SubEditDialogs'
import css from './Subs.module.css'

// 菜单里能打开对话框的操作（order 直接跳订单页，不落状态）
type RowAction = { kind: 'extend' | 'traffic' | 'reset' | 'period' | 'quota' | 'devices' | 'rotate'; id: string }

export function SubscriptionsTab({ d, now }: { d: UserDetail; now: Date }) {
  const invalidate = useInvalidateUsers()
  const [action, setAction] = useState<RowAction | null>(null)
  const current = currentSubscription(d.subscriptions)
  const ordered = current ? [current, ...d.subscriptions.filter((s) => s !== current)] : d.subscriptions
  const close = () => setAction(null)
  const done = () => void invalidate()
  const notDeleted = d.status !== 'deleted'

  const targetSub = action ? d.subscriptions.find((s) => s.id === action.id) ?? null : null

  const run = (key: SubActionKey, s: SubscriptionRow) => {
    if (key === 'order') {
      navigate('/billing/orders', { query: { new: d.id, sub: s.id } })
      return
    }
    setAction({ kind: key, id: s.id })
  }

  // 每行的「操作」菜单：项与灰着的原因都来自 subActions；换发订阅链接前面插一条分隔线
  const menuEntries = (s: SubscriptionRow): MenuEntry[] =>
    subActions(s, d.subscriptions, now).flatMap((a) => [
      ...(a.key === 'rotate' ? [{ kind: 'separator' as const, key: 'sep' }] : []),
      {
        kind: 'item' as const,
        key: a.key,
        label: a.label,
        disabled: a.reason !== null,
        hint: a.reason ?? undefined,
        onSelect: () => run(a.key, s),
      },
    ])

  // 每份一行：表格里的弹出菜单会被 Table 的横向滚动裁掉，所以用 kit 的 RowList（不裁切）装进带框盒子
  const subRow = (s: SubscriptionRow) => {
    const quota = trafficQuota(s.quotas)
    const oneTime = planKind(s) === 'one_time'
    const exp = subExpiryView(s.current_period_end, now)
    const dv = deviceView(s.online_devices, s.device_limit_override ?? s.plan_max_devices ?? 0)
    const src = deviceLimitLabel(s).source
    const tone = (t: string) => (t === 'warn' ? css.toneWarn : t === 'danger' ? css.toneDanger : undefined)
    const st = SUB_STATUS_VIEW[s.status]
    return (
      <Row
        key={s.id}
        title={
          <>
            {s.label ?? s.plan_name}
            <span className={css.planVer}> · v{s.plan_version}</span>
          </>
        }
        meta={
          <>
            {s.label ? `${s.plan_name} · ` : ''}
            {oneTime ? '一次性套餐' : '订阅套餐'} · <span className={tone(exp.tone)}>{exp.text}</span>
          </>
        }
        extra={
          <span className={css.subFacts}>
            {quota ? (
              <span>
                <span className={css.num}>{trafficView(quota.limit, quota.consumed).text}</span>
                <span className={css.dim}>{oneTime ? ' 总量 · 不清零' : ' 本期 · 新周期清零'}</span>
              </span>
            ) : (
              <span className={css.dim}>没有流量配额</span>
            )}
            <span title={`在线 ${s.online_devices} 台 · 上限${src === 'override' ? '已单独设置' : src === 'plan' ? '跟套餐' : '未设'}`}>
              <span className={css.dim}>设备 </span>
              <span className={`${css.num} ${tone(dv.tone) ?? ''}`}>{dv.text}</span>
            </span>
          </span>
        }
        value={<Tag tone={st.tone}>{st.label}</Tag>}
        actions={
          notDeleted ? (
            <Menu
              label={`${subName(s)}的操作`}
              triggerLabel={`${subName(s)}的操作`}
              triggerClassName={css.menuTrigger}
              trigger="操作"
              align="end"
              entries={menuEntries(s)}
            />
          ) : undefined
        }
      />
    )
  }

  return (
    <>
      <Callout>
        订阅地址仅用户本人可见。疑似泄露时，在那一份的「操作」里换发订阅链接，旧地址立即失效，再让用户到用户端重新复制。
      </Callout>

      <section>
        <SectionTitle>订阅</SectionTitle>
        {ordered.length === 0 ? (
          <Empty bare title="还没有订阅" description="用户在用户端下单，或在订单页人工开单后，这里会出现订阅。" />
        ) : (
          <div className={css.subBox}>
            <RowList label="订阅">{ordered.map(subRow)}</RowList>
          </div>
        )}
      </section>

      <RotateDialog
        user={d}
        subscriptionId={action?.kind === 'rotate' ? action.id : null}
        open={action?.kind === 'rotate'}
        onClose={close}
        onDone={done}
      />
      <ExtendDialog
        user={d}
        subscriptionId={action?.kind === 'extend' ? action.id : null}
        open={action?.kind === 'extend'}
        onClose={close}
        onDone={done}
        now={now}
      />
      <GrantTrafficDialog
        user={d}
        subscription={action?.kind === 'traffic' ? targetSub : null}
        onClose={close}
        onDone={done}
      />
      <ResetTrafficDialog
        user={{ id: d.id, email: d.email }}
        subscription={action?.kind === 'reset' ? targetSub : null}
        onClose={close}
        onDone={done}
      />
      <PeriodEndDialog
        subscription={action?.kind === 'period' ? targetSub : null}
        open={action?.kind === 'period'}
        onClose={close}
        onDone={done}
        now={now}
      />
      <QuotaLimitDialog
        subscription={action?.kind === 'quota' ? targetSub : null}
        open={action?.kind === 'quota'}
        onClose={close}
        onDone={done}
      />
      <DeviceLimitDialog subscription={action?.kind === 'devices' ? targetSub : null} onClose={close} onDone={done} />
    </>
  )
}

export function OrdersTab({
  d,
  now,
  balanceOpen,
  onBalanceOpen,
}: {
  d: UserDetail
  now: Date
  balanceOpen: boolean
  onBalanceOpen: (open: boolean) => void
}) {
  const invalidate = useInvalidateUsers()
  const notDeleted = d.status !== 'deleted'

  return (
    <>
      <section>
        <div className={css.ordersTop}>
          <div className={css.balanceBlock}>
            <span className={css.balanceLabel}>账户余额</span>
            <span className={css.balanceAmount}>{formatMoney(d.balance)}</span>
          </div>
          {notDeleted && (
            <div className={css.topActions}>
              <Button size="sm" aria-expanded={balanceOpen} onClick={() => onBalanceOpen(!balanceOpen)}>
                调整余额
              </Button>
              <Button size="sm" onClick={() => navigate('/billing/orders', { query: { new: d.id } })}>
                新开订单
              </Button>
            </div>
          )}
        </div>
        {balanceOpen && notDeleted && (
          <BalanceForm user={d} onClose={() => onBalanceOpen(false)} onDone={() => void invalidate()} />
        )}
      </section>

      <section>
        <SectionTitle hint="最近 20 单">订单</SectionTitle>
        <Table
          boxed
          label="订单"
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
            { key: 'what', header: '内容', hideOnMobile: true, render: (o) => <span className={css.dim}>{orderWhat(o)}</span> },
            {
              key: 'time',
              hideOnMobile: true,
              header: '时间',
              width: '100px',
              render: (o) => <span className={css.dim}>{relativeTime(o.created_at, now)}</span>,
            },
            {
              key: 'amount',
              header: '金额',
              width: '96px',
              align: 'right',
              mono: true,
              render: (o) => formatMoney(o.paid_amount || o.payable_amount),
            },
            {
              key: 'status',
              header: '状态',
              width: '80px',
              align: 'right',
              render: (o) => {
                const st = ORDER_STATUS_VIEW[o.status]
                return <Tag tone={st.tone}>{st.label}</Tag>
              },
            },
          ]}
          rows={d.recent_orders}
          rowKey={(o) => o.id}
          onRowClick={(o) => navigate(`/billing/orders/${encodeURIComponent(o.id)}`)}
          empty={
            <Empty bare title="还没有订单" description="用户在用户端下单，或在订单页人工开单后，这里会列出最近 20 单。" />
          }
        />
        {d.recent_orders.length > 0 && (
          <a className={css.allOrdersLink} href={href('/billing/orders', { user_id: d.id })}>
            在订单页查看全部 {d.stats.order_count} 单 →
          </a>
        )}
      </section>
    </>
  )
}
