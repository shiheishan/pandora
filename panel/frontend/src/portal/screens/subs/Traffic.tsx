import { useState } from 'react'
import { href } from '../../../core/router'
import { Empty, Skeleton } from '../../../ui'
import { usePageHead } from '../../head'
import { LoadError } from '../common/Blocks'
import { usePackCatalog, usePlans, type Pack } from '../common/catalog'
import { flowCss } from '../common/Flow'
import { useHoldings } from '../common/holdings'
import { day, gb, leftOf, money, moneyShort, planTraffic } from '../common/purchase'
import { useQuote } from '../common/quote'
import { isLive, type Subscription } from '../common/subscriptions'
import { pickTrafficQuota } from '../common/traffic'
import { periodWord } from '../common/card-text'
import css from './Subs.module.css'

/** 推荐的那一档；没有推荐时取中间那档 */
export const defaultPack = (packs: readonly Pack[]) => packs.find((p) => p.recommended) ?? packs[Math.floor((packs.length - 1) / 2)] ?? null

/**
 * 加流量（原型 traffic）：从卡片进来，已知是哪一份。流量包宫格 + 主按钮「加 100G · ¥18.00」；
 * 隔一条线的一行文字链「每个月都不够用？看看换成…」，金额来自报价接口。
 */
export function Traffic({ id }: { id: string }) {
  usePageHead('加流量', '/subs')
  const h = useHoldings()
  const packs = usePackCatalog()
  if (h.subs.isPending || packs.isPending) return <Skeleton height={240} />
  const failed = h.subs.isError ? h.subs : packs.isError ? packs : null
  if (failed) return <LoadError error={failed.error} onRetry={() => void failed.refetch()} what="流量包" />
  const sub = h.held.find((s) => s.id === id)
  if (!sub || !isLive(sub)) {
    return <Empty title="这一份现在不能加流量" description="流量包要加到一份在用的套餐上。已过期的先续费，就能接着用。" action={<a href={href('/subs')}>回到我的套餐</a>} />
  }
  if (!packs.data?.length) return <Empty title="暂时没有流量包可买" description="流量包上架后会出现在这里。" action={<a href={href('/subs')}>回到我的套餐</a>} />
  return <TrafficForm sub={sub} packs={packs.data} naming={h.naming} />
}

function TrafficForm({ sub, packs, naming }: { sub: Subscription; packs: readonly Pack[]; naming: ReturnType<typeof useHoldings>['naming'] }) {
  const [chosen, setChosen] = useState<string | null>(null)
  const pack = packs.find((p) => p.id === chosen) ?? defaultPack(packs)!
  const { left } = leftOf(sub)
  const limit = pickTrafficQuota(sub.quotas)?.limit ?? null
  return (
    <div className={`${flowCss.stack} ${flowCss.narrow}`}>
      {naming.multi && (
        <p className={flowCss.lead}>
          给 <b>{naming.dn(sub)}</b> 加流量
        </p>
      )}
      <p className={flowCss.lead}>
        {periodWord(sub)}还剩 <b>{left === null ? '不限' : gb(left)}</b>。
        {sub.current_period_end && limit !== null && `${day(sub.current_period_end)} 续上后，新的一期又有 ${gb(limit)}。`}
      </p>
      <h3 className={css.sec}>
        买流量包<small>马上到账，用完为止</small>
      </h3>
      <div className={css.packGrid} role="radiogroup" aria-label="流量包">
        {packs.map((p) => (
          <button key={p.id} type="button" role="radio" aria-checked={p.id === pack.id} className={css.pack} onClick={() => setChosen(p.id)} id={`pack-${p.id}`}>
            <span className={css.packSize}>{gb(p.traffic_bytes)}</span>
            <span className={css.packPrice}>{moneyShort(p.unit_amount)}</span>
            {left !== null && <span className={css.packAfter}>加完能用 {gb(left + p.traffic_bytes)}</span>}
            {p.recommended && <em className={css.best}>最划算</em>}
          </button>
        ))}
      </div>
      <a className={flowCss.twoLinePrimary} href={href('/checkout', { pack: pack.id, sub: sub.id })} id="btn-pack-go">
        加 {gb(pack.traffic_bytes)} · {money(pack.unit_amount)}
        <small>链接不变，不用重新添加</small>
      </a>
      <BiggerPlan sub={sub} />
    </div>
  )
}

/** 流量更多的套餐里最小的那个：「看看换成进阶版（每月 300G，今天付 ¥42.60）→」 */
function BiggerPlan({ sub }: { sub: Subscription }) {
  const plans = usePlans()
  const quote = useQuote(sub.changeable ? { action: 'change', subscription_id: sub.id } : null)
  const mine = pickTrafficQuota(sub.quotas)?.limit ?? null
  if (mine === null || !plans.data || !quote.data) return null
  const bigger = quote.data.quotes
    .map((row) => ({ row, plan: plans.data.find((p) => p.id === row.plan_id) }))
    .map((x) => ({ ...x, limit: x.plan?.quotas.find((q) => q.metric === 'traffic.bytes')?.limit }))
    .filter((x) => x.plan && (x.limit === null || (x.limit ?? 0) > mine))
    .sort((a, b) => (a.limit ?? Number.MAX_SAFE_INTEGER) - (b.limit ?? Number.MAX_SAFE_INTEGER))[0]
  if (!bigger?.plan) return null
  const pay = bigger.row.total > 0 ? `今天付 ${money(bigger.row.total)}` : '今天不用付'
  return (
    <div className={css.aside}>
      <span className={flowCss.faint}>每个月都不够用？</span>
      <a className={flowCss.textButton} href={href('/checkout', { change: sub.id, plan: bigger.plan.id })} id={`btn-change-${bigger.plan.id}`}>
        看看换成{bigger.plan.name}（{planTraffic(bigger.plan)}，{pay}）→
      </a>
    </div>
  )
}
