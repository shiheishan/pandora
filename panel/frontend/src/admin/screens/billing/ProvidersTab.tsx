/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 ConfirmModal / Empty / Menu / QueryView / Switch / Tag / useToast，依赖 ../../actions 的 useCan / useFailure，依赖 ./api 的 useProviders / toggledSchema / useInvalidateBilling / Provider，依赖 ./model，依赖 ./Billing.module.css
 * [OUTPUT]: 对外提供 ProvidersTab
 * [POS]: 订单与收款「支付渠道」标签（后台-05）：渠道卡（首字、名称、code、开关、今日成交按币种 / 24 小时成功率 / 币种三格、备注：停收新单或完全停用、未配置凭据、最近回调，R66）。开关按 PAY-009 只动 accepting_new（关 = 结账页不显示、进行中的支付仍回调）；「完全停用（回调也不处理）」放在「更多」菜单并二次确认；系统内置的 offline 只读展示。写接口 billing.provider.write + reauth，没有幂等
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { ConfirmModal, Empty, Menu, QueryView, Switch, Tag, useToast } from '../../../ui'
import { useCan, useFailure } from '../../actions'
import { toggledSchema, useInvalidateBilling, useProviders, type Provider } from './api'
import css from './Billing.module.css'
import { isOffline, providerMode, providerNote, rateLabel, todayLabel, toggleBody, type ProviderMode } from './model'

interface Pending {
  p: Provider
  target: ProviderMode
}

export function ProvidersTab({ now }: { now: Date }) {
  const providers = useProviders()
  const can = useCan()
  const writable = can('billing.provider.write')
  const [pending, setPending] = useState<Pending | null>(null)

  return (
    <>
      <QueryView query={providers} isEmpty={(d) => d.length === 0} empty={<Empty title="还没有支付渠道" description="运维用 aegis-payctl 配置渠道后会出现在这里。" />}>
        {(rows) => (
          <div className={css.cards}>
            {rows.map((p) => (
              <ProviderCard key={p.id} p={p} now={now} writable={writable} onToggle={(target) => setPending({ p, target })} />
            ))}
          </div>
        )}
      </QueryView>
      <ToggleDialog pending={pending} onClose={() => setPending(null)} />
    </>
  )
}

function ProviderCard({ p, now, writable, onToggle }: { p: Provider; now: Date; writable: boolean; onToggle: (target: ProviderMode) => void }) {
  const offline = isOffline(p)
  const mode = providerMode(p)
  const note = providerNote(p, now)
  const toggleable = writable && !offline
  return (
    <section className={`${css.card} ${mode !== 'on' && !offline ? css.cardOff : ''}`} aria-label={p.display_name}>
      <div className={css.cardHead}>
        <span className={css.abbr} aria-hidden="true">
          {[...p.display_name][0] ?? '?'}
        </span>
        <div className={`${css.stack} ${css.spacer}`}>
          <span className={css.cardName}>{p.display_name}</span>
          <span className={`${css.mono} ${css.small}`}>{p.code}</span>
        </div>
        {offline && <Tag tone="neutral">内置</Tag>}
        {toggleable && (
          <>
            <Switch aria-label={`${p.display_name} 收新单`} checked={mode === 'on'} onChange={(e) => onToggle(e.target.checked ? 'on' : 'paused')} />
            {mode !== 'off' && (
              <Menu
                label={`${p.display_name} 更多操作`}
                triggerLabel="更多操作"
                triggerClassName={css.moreButton}
                align="end"
                trigger="⋯"
                entries={[{ key: 'off', label: '完全停用（回调也不处理）', danger: true, onSelect: () => onToggle('off') }]}
              />
            )}
          </>
        )}
      </div>
      <dl className={css.stats}>
        <div>
          <dt>今日成交</dt>
          <dd className={css.statMono}>{todayLabel(p.today)}</dd>
        </div>
        <div>
          <dt>成功率</dt>
          <dd className={css.statMono} title="近 24 小时进入终态的支付尝试里成功的比例">
            {rateLabel(p.success_rate_24h)}
          </dd>
        </div>
        <div>
          <dt>币种</dt>
          <dd>{p.currencies.join(' / ') || '—'}</dd>
        </div>
      </dl>
      <div className={`${css.note} ${css[`tone_${note.tone}`]}`}>{note.text}</div>
    </section>
  )
}

const COPY: Record<ProviderMode, { title: (n: string) => string; body: string; confirm: string; danger: boolean }> = {
  on: { title: (n) => `启用 ${n}？`, body: '启用后门户结账页立即可见。', confirm: '启用', danger: false },
  paused: { title: (n) => `停用 ${n}？`, body: '门户结账页将不再显示该渠道，进行中的支付仍会回调。', confirm: '停用', danger: true },
  off: {
    title: (n) => `完全停用 ${n}？`,
    body: '结账页不再显示，渠道回调也不再处理：进行中的支付即使到账也不会自动入账，需要人工核对后「标记已支付」。一般用「停用」就够了。',
    confirm: '完全停用',
    danger: true,
  },
}

/** POST v1/payment-providers/{code}/toggle：两个布尔都要传（漏传按 false） */
function ToggleDialog({ pending, onClose }: { pending: Pending | null; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateBilling()
  const copy = pending ? COPY[pending.target] : COPY.on
  const confirm = async () => {
    if (!pending) return
    try {
      await api.post(`v1/payment-providers/${encodeURIComponent(pending.p.code)}/toggle`, toggledSchema, { body: toggleBody(pending.target) })
      toast(pending.target === 'on' ? `${pending.p.display_name} 已启用` : pending.target === 'paused' ? `${pending.p.display_name} 已停止收新单` : `${pending.p.display_name} 已完全停用`)
      onClose()
    } catch (e) {
      fail(e)
    } finally {
      void invalidate()
    }
  }
  const noCreds = pending?.target === 'on' && !pending.p.has_credentials
  return (
    <ConfirmModal
      open={pending !== null}
      title={copy.title(pending?.p.display_name ?? '')}
      body={noCreds ? `${copy.body}注意：这个渠道还没有配置凭据，用户选了也付不了款。` : copy.body}
      confirmLabel={copy.confirm}
      tone={copy.danger ? 'danger' : 'primary'}
      onCancel={onClose}
      onConfirm={confirm}
    />
  )
}
