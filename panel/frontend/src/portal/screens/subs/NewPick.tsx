import { href } from '../../../core/router'
import { Empty, Skeleton, Tag } from '../../../ui'
import { usePageHead } from '../../head'
import { LoadError } from '../common/Blocks'
import { usePlans, type Plan } from '../common/catalog'
import { Callout, flowCss } from '../common/Flow'
import { useHoldings } from '../common/holdings'
import { headlinePrice, PlanCard } from '../common/PlanCard'
import { addPeriod, day, devicesText } from '../common/purchase'
import { isLive, type Subscription } from '../common/subscriptions'
import { useSheets } from './sheets'

/** 「买这个」的卡：同款已有时标「你已有一份」，按钮下写「新链接 · 今天起到 …」 */
export function NewPlanCard({ plan, held }: { plan: Plan; held: readonly Subscription[] }) {
  const holders = held.filter((s) => s.plan_id === plan.id)
  const price = headlinePrice(plan)
  const tag = holders.length ? <Tag>你已有{holders.length > 1 ? ` ${holders.length} 份` : '一份'}</Tag> : plan.recommended ? <Tag tone="brand">多数人选这个</Tag> : null
  return (
    <PlanCard
      plan={plan}
      tag={tag}
      action={
        <a className={plan.recommended ? flowCss.cta : flowCss.ctaQuiet} href={href('/checkout', { new: plan.id })} id={`btn-new-${plan.id}`}>
          买这个
        </a>
      }
      note={price ? `新链接 · 今天起到 ${day(addPeriod(new Date(), price.billing_interval, price.interval_count))}` : undefined}
    />
  )
}

/**
 * 再买一份（原型 new-pick）：我的套餐底部「再买一份，分开用」进来。先说会发生什么，
 * 再提醒「只是自己多一台设备？不用再买」，下面是套餐卡「买这个」。
 */
export function NewPick() {
  usePageHead('再买一份', '/subs')
  const h = useHoldings()
  const plans = usePlans()
  const { actions, sheets } = useSheets(h)
  if (h.subs.isPending || plans.isPending) return <Skeleton height={240} />
  const failed = h.subs.isError ? h.subs : plans.isError ? plans : null
  if (failed) return <LoadError error={failed.error} onRetry={() => void failed.refetch()} what="套餐" />
  const first = h.held.find(isLive)
  return (
    <div className={flowCss.stack}>
      <Callout>会得到一个新链接，和现在的分开用：各自的流量、各自到期。</Callout>
      {first && (
        <div className={flowCss.tip}>
          <b>只是自己多一台设备？不用再买。</b>
          <br />
          {h.naming.multi ? `「${h.naming.dn(first)}」` : `你的${first.plan_name}`}最多 {devicesText(first.device_limit)}同时用，现在 {first.online_devices} 台在用。在新设备上添加现在的链接就行。
          <div>
            <button type="button" className={flowCss.textButton} onClick={() => actions.onImport(first)} id="btn-newpick-import">
              把现在的链接添加到新设备 →
            </button>
          </div>
        </div>
      )}
      {plans.data!.length === 0 ? (
        <Empty title="暂时没有可买的套餐" description="新套餐上架后会出现在这里。" />
      ) : (
        <div className={flowCss.cards}>
          {plans.data!.map((p) => (
            <NewPlanCard key={p.id} plan={p} held={h.held} />
          ))}
        </div>
      )}
      {sheets}
    </div>
  )
}
