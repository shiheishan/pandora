import { useQueries } from '@tanstack/react-query'
import { href, navigate, useHashLocation } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Card, Empty, Skeleton, Tag } from '../../../ui'
import { LoadError, Slot } from '../common/Blocks'
import { usePackCatalog, usePlans, type Pack } from '../common/catalog'
import { Chips, flowCss } from '../common/Flow'
import { useHoldings, type Holdings } from '../common/holdings'
import { PlanCard } from '../common/PlanCard'
import { gb, leftOf, moneyShort } from '../common/purchase'
import { quoteSchema, type QuoteRow } from '../common/quote'
import { isLive, type Subscription } from '../common/subscriptions'
import { planCardView, type PlansMode } from './labels'
import css from './Plans.module.css'

// ---------------------------------------------------------------------------
// 选购（原型 plans）：套餐 / 流量包两个标签。有套餐时套餐标签顶部先选「给现在的续费或换套餐」（默认）
// 还是「另买一份」；切到后者时所有套餐卡都是「买这个」。地址：?tab=packs、?mode=new、?sub=<流量包加到哪一份>
// ---------------------------------------------------------------------------
type Tab = 'plans' | 'packs'

export default function Plans() {
  const { query } = useHashLocation()
  const tab: Tab = query.get('tab') === 'packs' ? 'packs' : 'plans'
  const set = (patch: Record<string, string | null>) => navigate('/plans', { query: { ...Object.fromEntries(query), ...patch }, replace: true })
  const h = useHoldings()

  return (
    <div className={flowCss.stack}>
      <Slot name="portal.plans.notice" />
      <div className={css.seg} role="tablist" aria-label="商品类型">
        <button type="button" role="tab" aria-selected={tab === 'plans'} onClick={() => set({ tab: null })} id="tab-plans">
          套餐<small>按月，每月给流量</small>
        </button>
        <button type="button" role="tab" aria-selected={tab === 'packs'} onClick={() => set({ tab: 'packs' })} id="tab-packs">
          流量包<small>用完为止，不过期</small>
        </button>
      </div>
      {h.subs.isPending ? (
        <Skeleton height={240} />
      ) : h.subs.isError ? (
        <LoadError error={h.subs.error} onRetry={() => void h.subs.refetch()} what="套餐" />
      ) : tab === 'plans' ? (
        <PlansTab h={h} mode={h.held.length && query.get('mode') === 'new' ? 'new' : 'mine'} onMode={(m) => set({ mode: m === 'new' ? 'new' : null })} />
      ) : (
        <PacksTab h={h} picked={query.get('sub')} onPick={(id) => set({ sub: id })} />
      )}
    </div>
  )
}

/** 每份能换的套餐一次报价（按订阅展开），给「换成 X」下面的「今天付 ¥x」用 */
function useChangeQuotes(subs: readonly Subscription[]) {
  const api = useApi()
  const results = useQueries({
    queries: subs.map((s) => ({
      queryKey: ['portal', 'quote', { action: 'change', subscription_id: s.id }],
      queryFn: ({ signal }: { signal: AbortSignal }) => api.post('v1/me/checkout/quote', quoteSchema, { body: { action: 'change', subscription_id: s.id }, signal }),
      retry: false,
      meta: { topics: ['orders.changed', 'subscriptions.changed'] },
    })),
  })
  return (subId: string, planId: string): QuoteRow | undefined => {
    const i = subs.findIndex((s) => s.id === subId)
    return results[i]?.data?.quotes.find((q) => q.plan_id === planId)
  }
}

function PlansTab({ h, mode, onMode }: { h: Holdings; mode: PlansMode; onMode: (m: PlansMode) => void }) {
  const plans = usePlans()
  const changeable = h.held.filter((s) => s.changeable)
  // 只有一份能换时卡片上直接写「今天付 ¥x」；多份时下一步再选，不预先报价
  const quoteFor = useChangeQuotes(changeable.length === 1 ? changeable : [])
  if (plans.isPending) return <Skeleton height={240} />
  if (plans.isError) return <LoadError error={plans.error} onRetry={() => void plans.refetch()} what="套餐" />
  return (
    <>
      {h.held.length > 0 && (
        <div className={flowCss.modes} role="radiogroup" aria-label="想做什么">
          <button type="button" role="radio" aria-checked={mode === 'mine'} className={flowCss.mode} onClick={() => onMode('mine')} id="mode-mine">
            <span className={flowCss.modeTitle}>给现在的续费或换套餐</span>
            <span className={flowCss.modeNote}>链接不变</span>
          </button>
          <button type="button" role="radio" aria-checked={mode === 'new'} className={flowCss.mode} onClick={() => onMode('new')} id="mode-new">
            <span className={flowCss.modeTitle}>另买一份</span>
            <span className={flowCss.modeNote}>新链接，给家人或别人用</span>
          </button>
        </div>
      )}
      {mode === 'new' && <p className={flowCss.faint}>会得到一个新链接，和你现在的分开：各自的流量、各自到期。</p>}
      {plans.data.length === 0 ? (
        <Empty title="暂时没有可买的套餐" description="新套餐上架后会出现在这里，也可以先看看流量包。" action={<a href={href('/plans', { tab: 'packs' })}>看看流量包</a>} />
      ) : (
        <div className={flowCss.cards}>
          {plans.data.map((plan) => {
            const v = planCardView(plan, mode, h.held, h.naming, quoteFor)
            return (
              <PlanCard
                key={plan.id}
                plan={plan}
                current={v.current}
                tag={v.tag && <Tag tone={v.tag.tone === 'neutral' ? undefined : v.tag.tone}>{v.tag.text}</Tag>}
                action={
                  v.href ? (
                    <a className={v.primary ? flowCss.cta : flowCss.ctaQuiet} href={v.href} id={`btn-plan-${plan.id}`}>
                      {v.label}
                    </a>
                  ) : (
                    <span className={flowCss.ctaOff} aria-disabled="true" id={`btn-plan-${plan.id}`}>
                      {v.label}
                    </span>
                  )
                }
                note={v.note}
              />
            )
          })}
        </div>
      )}
    </>
  )
}

// ---------------------------------------------------------------------------
// 流量包：要加到一份在用的套餐上；多份时选「加到哪一份」，预选剩得最少的那份
// ---------------------------------------------------------------------------
const leftBytes = (s: Subscription) => leftOf(s).left ?? Number.MAX_SAFE_INTEGER

function PacksTab({ h, picked, onPick }: { h: Holdings; picked: string | null; onPick: (id: string) => void }) {
  const packs = usePackCatalog()
  const live = h.held.filter(isLive)
  if (packs.isPending) return <Skeleton height={200} />
  if (packs.isError) return <LoadError error={packs.error} onRetry={() => void packs.refetch()} what="流量包" />
  if (!live.length) {
    return (
      <div className={flowCss.tip}>
        流量包要加到一份在用的套餐上。先<a href={href('/subs')}>续费</a>或<a href={href('/plans')}>买个套餐</a>。
      </div>
    )
  }
  const target = live.find((s) => s.id === picked) ?? [...live].sort((a, b) => leftBytes(a) - leftBytes(b))[0]!
  const left = leftOf(target).left
  return (
    <>
      {live.length > 1 ? (
        <div className={css.field}>
          <span className={css.fieldLabel}>加到哪一份</span>
          <Chips label="加到哪一份" selected={target.id} onSelect={onPick} items={live.map((s) => ({ key: s.id, label: h.naming.sn(s), note: leftOf(s).left === null ? '不限' : `剩 ${gb(leftOf(s).left!)}` }))} />
        </div>
      ) : (
        <p className={flowCss.lead}>
          会加到你的{target.plan_name}，现在剩 {left === null ? '不限' : gb(left)}。
        </p>
      )}
      {packs.data.length === 0 ? (
        <Empty title="暂时没有可买的流量包" description="流量包上架后会出现在这里。" />
      ) : (
        <div className={flowCss.stack}>
          {packs.data.map((p) => (
            <PackRow key={p.id} pack={p} target={target} left={left} />
          ))}
        </div>
      )}
      <p className={flowCss.faint}>先用套餐每月的流量，不够了再用流量包。流量包不会过期，续费、换套餐后跟着这份走。</p>
    </>
  )
}

function PackRow({ pack, target, left }: { pack: Pack; target: Subscription; left: number | null }) {
  return (
    <Card className={css.packRow}>
      <div className={css.packText}>
        <b>{gb(pack.traffic_bytes)}</b> {pack.recommended && <Tag tone="ok">最划算</Tag>}
        <small>
          {moneyShort(pack.unit_amount)}
          {left !== null && <> · 加完能用 {gb(left + pack.traffic_bytes)}</>}
        </small>
      </div>
      <a className={css.packBuy} href={href('/checkout', { pack: pack.id, sub: target.id })} id={`btn-pack-${pack.id}`}>
        买 {gb(pack.traffic_bytes)}
      </a>
    </Card>
  )
}
