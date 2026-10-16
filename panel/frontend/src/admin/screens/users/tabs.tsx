import { useState } from 'react'
import { formatBytes, formatDateTime, formatMoney, relativeTime } from '../../../core/format'
import { href, navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Empty, Tag, useToast } from '../../../ui'
import { useFailure } from '../../actions'
import { okSchema, useInvalidateUsers, type SubscriptionRow, type UserDetail } from './api'
import { BalanceForm, RotateDialog } from './dialogs'
import { ExtendDialog } from './ExtendDialog'
import { GrantTrafficDialog } from './GrantTrafficDialog'
import {
  currentSubscription,
  deviceLimitLabel,
  extendableSubscriptions,
  isLive,
  isOrderTarget,
  isPermanent,
  ORDER_STATUS_VIEW,
  orderWhat,
  pips,
  planKind,
  SUB_STATUS_VIEW,
  subExpiryView,
  subName,
  trafficQuota,
  trafficView,
} from './model'
import { PeriodEndDialog, QuotaLimitDialog, ResetTrafficDialog } from './SubEditDialogs'
import css from './Subs.module.css'

type RowAction = { kind: 'extend' | 'traffic' | 'reset' | 'period' | 'quota'; id: string }

export function SubscriptionsTab({ d, now }: { d: UserDetail; now: Date }) {
  const invalidate = useInvalidateUsers()
  const [action, setAction] = useState<RowAction | null>(null)
  const [rotateOpen, setRotateOpen] = useState(false)
  const current = currentSubscription(d.subscriptions)
  const ordered = current ? [current, ...d.subscriptions.filter((s) => s !== current)] : d.subscriptions
  const close = () => setAction(null)
  const done = () => void invalidate()
  const notDeleted = d.status !== 'deleted'

  const targetSub = action ? d.subscriptions.find((s) => s.id === action.id) ?? null : null

  return (
    <>
      <div className={css.notice}>
        <span className={css.noticeText}>
          订阅地址仅用户本人可见；如疑似泄露，更换后让用户在用户端重新复制，旧地址立即失效。
        </span>
        {ordered.length > 0 && notDeleted && (
          <Button size="sm" onClick={() => setRotateOpen(true)}>
            更换订阅地址
          </Button>
        )}
      </div>

      {ordered.length === 0 ? (
        <Empty bare title="还没有订阅" description="用户在用户端下单，或在订单页人工开单后，这里会出现订阅。" />
      ) : (
        ordered.map((s) => (
          <SubscriptionCard
            key={s.id}
            user={d}
            s={s}
            now={now}
            current={s === current}
            notDeleted={notDeleted}
            onAction={(kind) => setAction({ kind, id: s.id })}
          />
        ))
      )}

      <RotateDialog user={d} open={rotateOpen} onClose={() => setRotateOpen(false)} onDone={done} />
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
    </>
  )
}

function SubscriptionCard({
  user,
  s,
  now,
  current,
  notDeleted,
  onAction,
}: {
  user: UserDetail
  s: SubscriptionRow
  now: Date
  current: boolean
  notDeleted: boolean
  onAction: (kind: RowAction['kind']) => void
}) {
  const name = subName(s)
  const isOneTime = planKind(s) === 'one_time'
  const isPerm = isPermanent(s.current_period_end, now)
  const st = SUB_STATUS_VIEW[s.status]
  const exp = subExpiryView(s.current_period_end, now)
  const quota = trafficQuota(s.quotas)
  const t = quota ? trafficView(quota.limit, quota.consumed) : null

  // 有效期说明
  const startStr = s.current_period_start ? formatDateTime(s.current_period_start).slice(0, 10) : '—'
  const endStr = s.current_period_end ? formatDateTime(s.current_period_end).slice(0, 10) : '—'
  const validityDesc = isOneTime
    ? `${formatMoney(s.amount)} · 开通 ${startStr} · ${isPerm ? '永久有效' : `到期 ${endStr}`}`
    : `${formatMoney(s.amount)} · ${startStr} 起 · 到期 ${endStr}`

  // 有效期按钮逻辑
  const extendable = extendableSubscriptions([s], now).length > 0 && !isPerm
  const extendTitle = isPerm ? '永久有效，不用加时长' : extendable ? undefined : '只有生效中，或过期不满 30 天的订阅能加时长'
  const editablePeriod = s.status === 'active' && !isPerm
  const periodTitle = isPerm ? '永久有效，没有到期时间' : s.status === 'active' ? undefined : '这一份不在生效中'

  // 流量按钮逻辑
  const live = isLive(s.status)
  const trafficActive = s.status === 'active'
  const quotaTitle = s.status !== 'active' ? '这一份不在生效中' : !quota ? '这一份没有流量配额' : undefined

  return (
    <section className={current ? `${css.subCard} ${css.subCurrent}` : css.subCard} aria-label={name}>
      <div className={css.subHead}>
        <span className={css.subPlan}>{s.label ?? s.plan_name}</span>
        <span className={css.muted}>{s.label ? `${s.plan_name} · v${s.plan_version}` : `v${s.plan_version}`}</span>
        <span className={css.subType}>{isOneTime ? '一次性套餐' : '订阅套餐'}</span>
        <Tag tone={st.tone}>{st.label}</Tag>
        <span className={css.spacer} />
        {live && <span className={css[`tone_${exp.tone}`]}>{exp.text}</span>}
      </div>

      {/* 有效期块 */}
      <div className={css.subSection}>
        <div className={css.sectionMain}>
          <span className={css.sectionLabel}>有效期</span>
          <span className={css.sectionDesc}>{validityDesc}</span>
        </div>
        {notDeleted && (
          <div className={css.actions}>
            <Button
              size="xs"
              aria-label={`给${name}加时长`}
              disabled={!extendable}
              title={extendTitle}
              onClick={() => onAction('extend')}
            >
              加时长
            </Button>
            <Button
              size="xs"
              aria-label={`改${name}的到期时间`}
              disabled={!editablePeriod}
              title={periodTitle}
              onClick={() => onAction('period')}
            >
              改到期
            </Button>
          </div>
        )}
      </div>

      {/* 流量块 */}
      <div className={css.subSection}>
        <div className={css.sectionMain}>
          <span className={css.sectionLabel}>{isOneTime ? '总流量' : '本期流量'}</span>
          {t ? (
            <>
              <div className={css.sectionDesc}>
                <span className={css.mono}>{t.text}</span>
              </div>
              <div className={css.track} aria-hidden="true">
                <span className={`${css.fill} ${css[`fill_${t.tone}`]}`} style={{ width: `${t.percent}%` }} />
              </div>
              <div className={css.small}>
                剩余 {quota && quota.remaining !== null ? formatBytes(quota.remaining) : '不限'}
                {isOneTime ? ' · 不清零，用完即止' : ' · 新周期清零'}
              </div>
            </>
          ) : (
            <div className={css.sectionDesc}>没有流量配额</div>
          )}
        </div>
        {notDeleted && (
          <div className={css.actions}>
            <Button
              size="xs"
              aria-label={`给${name}加流量`}
              disabled={!live}
              title={live ? undefined : '这一份已不在使用中'}
              onClick={() => onAction('traffic')}
            >
              加流量
            </Button>
            <Button
              size="xs"
              aria-label={`重置${name}的本期流量`}
              disabled={!trafficActive}
              title={trafficActive ? undefined : '这一份不在生效中'}
              onClick={() => onAction('reset')}
            >
              重置流量
            </Button>
            <Button
              size="xs"
              aria-label={`改${name}的流量上限`}
              disabled={!trafficActive || !quota}
              title={quotaTitle}
              onClick={() => onAction('quota')}
            >
              改上限
            </Button>
          </div>
        )}
      </div>

      {/* 设备块 */}
      <DeviceLimit s={s} name={name} notDeleted={notDeleted} />

      {/* 卡底按钮 */}
      {notDeleted && (
        <div className={css.subFooter}>
          <Button
            size="sm"
            disabled={!isOrderTarget(s, now)}
            aria-label={isOneTime ? `给${name}开单` : `续费${name}`}
            onClick={() => navigate('/billing/orders', { query: { new: user.id, sub: s.id } })}
          >
            {isOneTime ? '给这份开单' : '续费这一份'}
          </Button>
        </div>
      )}
    </section>
  )
}

function DeviceLimit({ s, name, notDeleted }: { s: SubscriptionRow; name: string; notDeleted: boolean }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateUsers()
  const [busy, setBusy] = useState(false)
  const shown = deviceLimitLabel(s)
  const limit = s.device_limit_override ?? s.plan_max_devices ?? 0
  const isOver = limit > 0 && s.online_devices > limit
  const pipList = pips(s.online_devices, limit)

  const set = async (limitVal: number | null) => {
    setBusy(true)
    try {
      await api.post(`v1/subscriptions/${encodeURIComponent(s.id)}/device-limit`, okSchema, { body: { limit: limitVal } })
      toast(limitVal === null ? '已恢复套餐默认的设备上限' : limitVal === 0 ? '已改为不限设备数' : `设备上限已改为 ${limitVal} 台`)
      void invalidate()
    } catch (e) {
      fail(e)
    } finally {
      setBusy(false)
    }
  }

  const sourceText = shown.source === 'override' ? '已覆盖套餐上限' : shown.source === 'plan' ? '套餐规定上限' : '未设上限'

  return (
    <div className={css.subSection}>
      <div className={css.sectionMain}>
        <span className={css.sectionLabel}>设备 · {sourceText}</span>
        <div className={css.sectionDesc}>
          <span className={css.pips} aria-hidden="true">
            {pipList.map((p, i) => (
              <span
                key={i}
                className={`${css.pip} ${p === 'over' ? css.pipOver : p === 'on' ? css.pipOn : css.pipOff}`}
              />
            ))}
          </span>{' '}
          <span className={isOver ? css.tone_danger : undefined}>
            在线 {s.online_devices} / {shown.text}
          </span>
        </div>
      </div>
      {notDeleted && (
        <div className={css.deviceControls}>
          <div className={css.deviceStepper}>
            <Button size="xs" aria-label={`减少${name}的一台设备上限`} disabled={busy || limit <= 1} onClick={() => void set(limit - 1)}>
              −
            </Button>
            <span className={css.deviceValue}>{shown.text}</span>
            <Button size="xs" aria-label={`增加${name}的一台设备上限`} disabled={busy || limit >= 1000} onClick={() => void set(limit === 0 ? 1 : limit + 1)}>
              +
            </Button>
          </div>
          <Button size="xs" variant="ghost" aria-label={`设置${name}的设备上限为不限`} disabled={busy || s.device_limit_override === 0} onClick={() => void set(0)}>
            不限
          </Button>
          <Button size="xs" variant="ghost" aria-label={`恢复${name}的设备上限默认`} disabled={busy || s.device_limit_override === null} onClick={() => void set(null)}>
            恢复默认
          </Button>
        </div>
      )}
    </div>
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
            <Button size="sm" variant="primary" onClick={() => navigate('/billing/orders', { query: { new: d.id } })}>
              新开订单
            </Button>
          </div>
        )}
      </div>

      {balanceOpen && notDeleted && (
        <BalanceForm user={d} onClose={() => onBalanceOpen(false)} onDone={() => void invalidate()} />
      )}

      {d.recent_orders.length === 0 ? (
        <Empty bare title="还没有订单" description="用户在用户端下单，或在订单页人工开单后，这里会列出最近 20 单。" />
      ) : (
        <div className={css.ordersTable}>
          <div className={css.ordersHead}>
            <span>订单</span>
            <span>金额</span>
            <span>状态</span>
          </div>
          <ul className={css.ordersList}>
            {d.recent_orders.map((o) => {
              const st = ORDER_STATUS_VIEW[o.status]
              const amount = o.paid_amount || o.payable_amount
              return (
                <li key={o.id} className={css.orderItem}>
                  <a className={css.orderLink} href={href(`/billing/orders/${encodeURIComponent(o.id)}`)}>
                    <span className={css.orderMain}>
                      <span className={css.orderNo}>
                        {o.order_no}
                        {o.manual && (
                          <Tag tone="outline" className={css.manualTag}>
                            人工
                          </Tag>
                        )}
                      </span>
                      <span className={css.small}>
                        {orderWhat(o)} · {relativeTime(o.created_at, now)}
                      </span>
                    </span>
                    <span className={css.mono}>{formatMoney(amount)}</span>
                    <Tag tone={st.tone}>{st.label}</Tag>
                  </a>
                </li>
              )
            })}
          </ul>
        </div>
      )}

      {d.recent_orders.length > 0 && (
        <a className={css.allOrdersLink} href={href('/billing/orders', { user_id: d.id })}>
          在订单页查看全部 {d.stats.order_count} 单 →
        </a>
      )}
    </>
  )
}
