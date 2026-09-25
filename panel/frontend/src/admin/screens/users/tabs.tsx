/**
 * [INPUT]: 依赖 react 的 useState / ReactNode，依赖 ../../../core/format 的 formatBytes / formatDateTime / formatMoney / relativeTime，依赖 ../../../core/router 的 href，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / Empty / Select / StatStrip / Tag / useToast，依赖 ../../actions 的 useCan / useFailure，依赖 ./api，依赖 ./model，依赖 ./Users.module.css
 * [OUTPUT]: 对外提供 ProfileTab、SubscriptionsTab、DevicesTab、OrdersTab
 * [POS]: 用户抽屉的四个资料标签页：画像（累计消费 / 订单数 / 邀请人数统计条、用户组分配、显示名 / 邮箱验证 / 风险 / 角色 / 余额 / 邀请人 / 注册 IP / 最近登录 / Telegram）、订阅（全部订阅，流量配额、设备上限 − / + 与「恢复套餐默认」「不限」，按保留规则 2 不出现订阅地址）、设备（D-B-8 未决前按方案 A：只有在线台数与上限）、订单（最近 20 单，跳订单页按 user_id 精确筛选）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState, type ReactNode } from 'react'
import { formatBytes, formatDateTime, formatMoney, relativeTime } from '../../../core/format'
import { href } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Empty, Select, StatStrip, Tag, useToast } from '../../../ui'
import { useCan, useFailure } from '../../actions'
import { okSchema, useInvalidateUsers, useUserGroups, useUserProfile, type SubscriptionRow, type UserDetail } from './api'
import {
  currentSubscription,
  deviceLimitLabel,
  expiryView,
  isLiveSub,
  ORDER_STATUS_VIEW,
  orderWhat,
  RISK_VIEW,
  SUB_STATUS_VIEW,
  trafficQuota,
  trafficView,
} from './model'
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
          { label: '累计消费', value: formatMoney(d.stats.paid_total, d.currency) },
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
  const options = [{ value: '', label: '未分组（默认）' }, ...(groups.data ?? []).map((g) => ({ value: g.id, label: g.name }))]
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
  return <Select size="sm" aria-label="用户组" options={options} value={d.group_id ?? ''} disabled={busy || groups.isPending} onChange={(e) => void assign(e.target.value)} fieldClassName={css.groupSelect} />
}

// ===========================================================================
// 订阅：全部订阅（待补·前端，设计只画一条），当前订阅排最前
// ===========================================================================
export function SubscriptionsTab({ d, now }: { d: UserDetail; now: Date }) {
  const current = currentSubscription(d.subscriptions)
  const ordered = current ? [current, ...d.subscriptions.filter((s) => s !== current)] : d.subscriptions
  return (
    <>
      {/* 保留规则 2：后台看不到订阅地址，设计稿的「订阅地址 + 复制」整块换成这句说明 */}
      <p className={css.notice}>订阅地址仅用户本人可见；如疑似泄露，请点「更换订阅地址」后让用户在门户重新复制。</p>
      {ordered.length === 0 ? (
        <Empty bare title="还没有订阅" description="用户在门户下单，或在订单页人工开单后，这里会出现订阅。" />
      ) : (
        ordered.map((s) => <SubscriptionCard key={s.id} s={s} now={now} current={s === current} />)
      )}
    </>
  )
}

function SubscriptionCard({ s, now, current }: { s: SubscriptionRow; now: Date; current: boolean }) {
  const st = SUB_STATUS_VIEW[s.status]
  const exp = expiryView(s.current_period_end, now)
  const quota = trafficQuota(s.quotas)
  const t = quota ? trafficView(quota.limit, quota.consumed) : null
  return (
    <section className={current ? `${css.subCard} ${css.subCurrent}` : css.subCard}>
      <div className={css.subHead}>
        <span className={css.subPlan}>{s.plan_name}</span>
        <span className={css.muted}>v{s.plan_version}</span>
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
      <DeviceLimit s={s} />
    </section>
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
// 设备：D-B-8 未决前按方案 A——节点只上报 IP 哈希，没有客户端名与明文 IP，只给台数与上限
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
              <span className={css.subPlan}>{s.plan_name}</span>
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
                <span className={css.mono}>{o.order_no}</span>
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
