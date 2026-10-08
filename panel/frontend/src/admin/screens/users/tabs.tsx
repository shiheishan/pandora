import { useState, type ReactNode } from 'react'
import { formatBytes, formatDateTime, formatMoney, relativeTime } from '../../../core/format'
import { href, navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Empty, Select, StatStrip, Tag, useToast } from '../../../ui'
import { useCan, useFailure } from '../../actions'
import { okSchema, useInvalidateUsers, useUserGroups, useUserProfile, type SubscriptionRow, type UserDetail } from './api'
import { ExtendDialog } from './ExtendDialog'
import {
  currentSubscription,
  deviceLimitLabel,
  expiryView,
  extendableSubscriptions,
  isLiveSub,
  isOrderTarget,
  ORDER_STATUS_VIEW,
  orderWhat,
  paidTotalsLabel,
  RISK_VIEW,
  SUB_STATUS_VIEW,
  subName,
  trafficQuota,
  trafficView,
} from './model'
import { ResetDialog } from './Resets'
import { TrafficPackDialog } from './TrafficPackDialog'
import css from './Users.module.css'

// ===========================================================================
// 画像
// ===========================================================================
export function ProfileTab({ d, now }: { d: UserDetail; now: Date }) {
  const can = useCan()
  const risk = RISK_VIEW[d.risk_level]
  // 注册 IP 是明文 IP，只从风控画像接口取，没有 security.audit.read 时整行不显示（契约）
  const profile = useUserProfile(d.id, can('security.audit.read'))
  const facts: Array<[string, ReactNode]> = [
    ['显示名', d.display_name || '—'],
    ['邮箱验证', d.email_verified ? '已验证' : <span className={css.tone_warn}>未验证</span>],
    ['风险等级', <Tag tone={risk.tone}>{risk.label}</Tag>],
    ['角色', d.roles.length ? d.roles.join('、') : '普通用户'],
    ['余额', <span className={css.mono}>{formatMoney(d.balance, d.currency)}</span>],
    [
      '邀请人',
      d.referrer ? (
        <a href={href(`/users/list/${encodeURIComponent(d.referrer.id)}`)} className={css.link}>
          {d.referrer.email}
        </a>
      ) : (
        '—'
      ),
    ],
  ]
  if (can('security.audit.read')) facts.push(['注册 IP', <span className={css.mono}>{profile.data ? profile.data.registered_ip || '无记录' : '…'}</span>])
  facts.push(['最近登录', d.last_login_at ? `${relativeTime(d.last_login_at, now)} · ${formatDateTime(d.last_login_at)}` : '从未登录'])
  facts.push(['Telegram', d.telegram ? `已绑定 @${d.telegram.username} · ${formatDateTime(d.telegram.bound_at).slice(0, 10)}` : '未绑定'])

  return (
    <>
      <StatStrip
        label="用户统计"
        items={[
          { label: '累计消费', value: paidTotalsLabel(d.stats.paid_totals, d.currency) },
          { label: '订单数', value: String(d.stats.order_count) },
          { label: '邀请人数', value: String(d.stats.referral_count) },
        ]}
      />
      <dl className={css.facts}>
        <dt>用户组</dt>
        <dd>
          <GroupPicker d={d} />
        </dd>
        {facts.map(([k, v]) => (
          <FactRow key={k} label={k} value={v} />
        ))}
      </dl>
    </>
  )
}

function FactRow({ label, value }: { label: string; value: ReactNode }) {
  return (
    <>
      <dt>{label}</dt>
      <dd>{value}</dd>
    </>
  )
}

/** 用户组：POST v1/users/{id}/group（iam.user.write，无幂等）；「未分组（默认）」传空串（契约） */
function GroupPicker({ d }: { d: UserDetail }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateUsers()
  const groups = useUserGroups()
  const [busy, setBusy] = useState(false)
  if (!can('iam.user.write')) return <>{d.group_name || '未分组（默认）'}</>
  const options = (groups.data ?? []).map((g) => ({ value: g.id, label: g.name }))
  const assign = async (groupId: string) => {
    setBusy(true)
    try {
      await api.post(`v1/users/${encodeURIComponent(d.id)}/group`, okSchema, { body: { group_id: groupId } })
      toast(groupId ? `已分配到「${options.find((o) => o.value === groupId)?.label ?? '所选分组'}」` : '已移出分组')
      void invalidate(true)
    } catch (e) {
      fail(e)
    } finally {
      setBusy(false)
    }
  }
  return <Select size="sm" aria-label="用户组" emptyOption="未分组（默认）" options={options} value={d.group_id ?? ''} disabled={busy || groups.isPending} onChange={(e) => void assign(e.target.value)} fieldClassName={css.groupSelect} />
}

// ===========================================================================
// 订阅：全部订阅（待补·前端，设计只画一条），当前订阅排最前。
// 购买模型统一后每份订阅各自一行：显示备注名、这一份的流量包余量，开单、加时长、加流量、
// 重置流量都落在被点的这一份上，不再由系统替管理员挑
// ===========================================================================
type RowAction = { kind: 'extend' | 'traffic' | 'reset'; id: string }

export function SubscriptionsTab({ d, now }: { d: UserDetail; now: Date }) {
  const invalidate = useInvalidateUsers()
  const [action, setAction] = useState<RowAction | null>(null)
  const current = currentSubscription(d.subscriptions)
  const ordered = current ? [current, ...d.subscriptions.filter((s) => s !== current)] : d.subscriptions
  const close = () => setAction(null)
  const done = () => void invalidate()
  return (
    <>
      {/* 保留规则 2：后台看不到订阅地址，设计稿的「订阅地址 + 复制」整块换成这句说明 */}
      <p className={css.notice}>订阅地址仅用户本人可见；如疑似泄露，请点「更换订阅地址」后让用户在门户重新复制。</p>
      {ordered.length === 0 ? (
        <Empty bare title="还没有订阅" description="用户在门户下单，或在订单页人工开单后，这里会出现订阅。" />
      ) : (
        ordered.map((s) => (
          <SubscriptionCard key={s.id} userId={d.id} s={s} now={now} current={s === current} onAction={(kind) => setAction({ kind, id: s.id })} />
        ))
      )}
      <ExtendDialog user={d} subscriptionId={action?.kind === 'extend' ? action.id : null} open={action?.kind === 'extend'} onClose={close} onDone={done} now={now} />
      <TrafficPackDialog user={d} subscriptionId={action?.kind === 'traffic' ? action.id : null} open={action?.kind === 'traffic'} onClose={close} onDone={done} />
      <ResetDialog user={action?.kind === 'reset' ? { id: d.id, email: d.email } : null} subscriptionId={action?.kind === 'reset' ? action.id : null} onClose={close} onDone={close} />
    </>
  )
}

function SubscriptionCard({ userId, s, now, current, onAction }: { userId: string; s: SubscriptionRow; now: Date; current: boolean; onAction: (kind: RowAction['kind']) => void }) {
  const st = SUB_STATUS_VIEW[s.status]
  const exp = expiryView(s.current_period_end, now)
  const quota = trafficQuota(s.quotas)
  const t = quota ? trafficView(quota.limit, quota.consumed) : null
  return (
    <section className={current ? `${css.subCard} ${css.subCurrent}` : css.subCard} aria-label={subName(s)}>
      <div className={css.subHead}>
        <span className={css.subPlan}>{s.label ?? s.plan_name}</span>
        <span className={css.muted}>{s.label ? `${s.plan_name} · v${s.plan_version}` : `v${s.plan_version}`}</span>
        <Tag tone={st.tone}>{st.label}</Tag>
        <span className={css.spacer} />
        {isLiveSub(s.status) && <span className={css[`tone_${exp.tone}`]}>{exp.text}</span>}
      </div>
      <div className={css.subMeta}>
        {formatMoney(s.amount, s.currency)} · {s.auto_renew ? '自动续费' : '不自动续费'}
        {s.current_period_start && ` · 本期 ${formatDateTime(s.current_period_start).slice(0, 10)} 起`}
        {s.current_period_end && ` · 到期 ${formatDateTime(s.current_period_end).slice(0, 10)}`}
      </div>
      {t && (
        <div className={css.quota}>
          <div className={css.quotaRow}>
            <span>本期流量</span>
            <span className={css.mono}>{t.text}</span>
          </div>
          <div className={css.trackLg} aria-hidden="true">
            <span className={`${css.fill} ${css[`fill_${t.tone}`]}`} style={{ width: `${t.percent}%` }} />
          </div>
          {quota && quota.remaining !== null && <div className={css.small}>剩余 {formatBytes(quota.remaining)}</div>}
        </div>
      )}
      <div className={css.quotaRow}>
        <span>流量包余量（只用于这一份）</span>
        <span className={css.mono}>{formatBytes(s.pack_remaining_bytes)}</span>
      </div>
      <DeviceLimit s={s} />
      <RowActions userId={userId} s={s} now={now} onAction={onAction} />
    </section>
  )
}

/**
 * 这一份订阅上的操作：「给这份开单」带入口订阅打开人工开单；加时长、加流量、重置流量各开自己的对话框，
 * 都只作用于这一份。不适用的按钮灰着并在悬停里写明原因，不藏起来（权限不够的才不画）
 */
function RowActions({ userId, s, now, onAction }: { userId: string; s: SubscriptionRow; now: Date; onAction: (kind: RowAction['kind']) => void }) {
  const can = useCan()
  const name = subName(s)
  const canOrder = can('billing.order.write')
  const canAdjust = can('billing.adjustment.write')
  const canReset = can('metering.reset.write')
  if (!canOrder && !canAdjust && !canReset) return null
  const extendable = extendableSubscriptions([s], now).length > 0
  const live = isLiveSub(s.status)
  return (
    <div className={css.subActions}>
      {canOrder && (
        <Button
          size="xs"
          aria-label={`给${name}开单`}
          disabled={!isOrderTarget(s, now)}
          title={isOrderTarget(s, now) ? '打开人工开单，并预选这一份作为落点' : '这一份已彻底停用，不能作为落点；要另开请用上方「为其开单」'}
          onClick={() => navigate('/billing/orders', { query: { new: userId, sub: s.id } })}
        >
          给这份开单
        </Button>
      )}
      {canAdjust && (
        <Button
          size="xs"
          aria-label={`给${name}加时长`}
          disabled={!extendable}
          title={extendable ? undefined : '只有生效中、试用中，或过期不满 30 天的订阅能加时长'}
          onClick={() => onAction('extend')}
        >
          加时长
        </Button>
      )}
      {canAdjust && (
        <Button size="xs" aria-label={`给${name}加流量`} disabled={!live} title={live ? undefined : '这一份已不在使用中，流量包用不上'} onClick={() => onAction('traffic')}>
          加流量
        </Button>
      )}
      {canReset && (
        <Button
          size="xs"
          aria-label={`重置${name}的本期流量`}
          disabled={s.status !== 'active'}
          title={s.status === 'active' ? undefined : s.status === 'trialing' ? '试用订阅不能手动重置' : '这一份不在生效中'}
          onClick={() => onAction('reset')}
        >
          重置流量
        </Button>
      )}
    </div>
  )
}

/**
 * 本订阅设备上限（覆盖全局策略）：POST v1/subscriptions/{id}/device-limit（iam.user.write，无幂等）。
 * 设计稿范围 1–20，后端 0–1000，前端按 1–1000；0（不限）与 null（恢复套餐默认）走两个快捷按钮
 */
function DeviceLimit({ s }: { s: SubscriptionRow }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateUsers()
  const [busy, setBusy] = useState(false)
  const shown = deviceLimitLabel(s)
  const value = s.device_limit_override ?? s.plan_max_devices ?? 0
  const set = async (limit: number | null) => {
    setBusy(true)
    try {
      await api.post(`v1/subscriptions/${encodeURIComponent(s.id)}/device-limit`, okSchema, { body: { limit } })
      toast(limit === null ? '已恢复套餐默认的设备上限' : limit === 0 ? '已改为不限设备数' : `设备上限已改为 ${limit} 台`)
      void invalidate()
    } catch (e) {
      fail(e)
    } finally {
      setBusy(false)
    }
  }
  const source = shown.source === 'override' ? '已覆盖' : shown.source === 'plan' ? '套餐规定' : '未设上限'
  return (
    <div className={css.deviceRow}>
      <span className={css.deviceLabel}>
        本订阅设备上限 <span className={css.muted}>（{source}，在线 {s.online_devices} 台）</span>
      </span>
      {can('iam.user.write') ? (
        <>
          <Button size="xs" aria-label="减少一台" disabled={busy || value <= 1} onClick={() => void set(value - 1)}>
            −
          </Button>
          <span className={css.deviceValue}>{shown.text}</span>
          <Button size="xs" aria-label="增加一台" disabled={busy || value >= 1000} onClick={() => void set(value === 0 ? 1 : value + 1)}>
            +
          </Button>
          <Button size="xs" variant="ghost" disabled={busy || s.device_limit_override === null} onClick={() => void set(null)}>
            恢复套餐默认
          </Button>
          <Button size="xs" variant="ghost" disabled={busy || s.device_limit_override === 0} onClick={() => void set(0)}>
            不限
          </Button>
        </>
      ) : (
        <span className={css.deviceValue}>{shown.text}</span>
      )}
    </div>
  )
}

// ===========================================================================
// 设备：D-B-8 已决（5.A.2）方案 A——节点只上报 IP 哈希，没有客户端名与明文 IP，只给台数与上限
// ===========================================================================
export function DevicesTab({ d }: { d: UserDetail }) {
  const live = d.subscriptions.filter((s) => isLiveSub(s.status))
  if (live.length === 0) return <Empty bare title="暂无在线设备" description="用户没有生效中的订阅。" />
  return (
    <>
      {live.map((s) => {
        const limit = s.device_limit_override ?? s.plan_max_devices ?? 0
        const over = limit > 0 && s.online_devices > limit
        return (
          <div key={s.id} className={css.deviceCard}>
            <span className={css.stack}>
              <span className={css.subPlan}>{subName(s)}</span>
              <span className={css.small}>近 5 分钟在线（按来源 IP 去重）</span>
            </span>
            <span className={css.pips} aria-hidden="true">
              {Array.from({ length: Math.min(Math.max(limit, s.online_devices), 12) }, (_, i) => (
                <span key={i} className={i < s.online_devices ? (over ? `${css.pip} ${css.pipOver}` : `${css.pip} ${css.pipOn}`) : css.pip} />
              ))}
            </span>
            <span className={`${css.mono} ${over ? css.tone_danger : ''}`}>
              {s.online_devices}/{limit === 0 ? '不限' : limit}
            </span>
          </div>
        )
      })}
      <p className={css.small}>逐台设备的客户端与 IP 暂不提供：节点只上报 IP 的哈希。订阅拉取来源可在「风控」标签查看（需要审计读取权限）。</p>
    </>
  )
}

// ===========================================================================
// 订单：最近 20 单；「在订单页查看全部」按 user_id 精确筛选（R63）
// ===========================================================================
export function OrdersTab({ d, now }: { d: UserDetail; now: Date }) {
  const can = useCan()
  if (d.recent_orders.length === 0) return <Empty bare title="还没有订单" description="用户下单或管理员人工开单后，这里会列出最近 20 单。" />
  return (
    <>
      <ul className={css.orders}>
        {d.recent_orders.map((o) => {
          const st = ORDER_STATUS_VIEW[o.status]
          const amount = o.paid_amount || o.payable_amount
          const row = (
            <>
              <span className={css.stack}>
                <span className={css.mono}>
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
              <span className={css.mono}>{formatMoney(amount, o.currency)}</span>
              <Tag tone={st.tone}>{st.label}</Tag>
            </>
          )
          return (
            <li key={o.id}>
              {can('billing.order.read') ? (
                <a className={css.orderRow} href={href(`/billing/orders/${encodeURIComponent(o.id)}`)}>
                  {row}
                </a>
              ) : (
                <div className={css.orderRow}>{row}</div>
              )}
            </li>
          )
        })}
      </ul>
      {can('billing.order.read') && (
        <a className={css.link} href={href('/billing/orders', { user_id: d.id })}>
          在订单页查看全部 {d.stats.order_count} 单
        </a>
      )}
    </>
  )
}
