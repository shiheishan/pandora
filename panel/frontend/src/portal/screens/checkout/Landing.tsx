import { useEffect } from 'react'
import { href, navigate } from '../../../core/router'
import { isApiError } from '../../../core/api'
import { Skeleton } from '../../../ui'
import { usePageHead } from '../../head'
import { Btn } from '../../kit'
import { LoadError } from '../common/Blocks'
import type { Plan } from '../common/catalog'
import { ChoiceList } from '../common/Flow'
import type { Holdings } from '../common/holdings'
import { useLandings, type Landing } from '../common/landings'
import { isLive } from '../common/subscriptions'
import css from './Checkout.module.css'
import { landingChoice, landingHref } from './model'

/** 买这个套餐前选「放到哪里」：不预选；只有一个可选项且没有置灰项时自动跳转；新开一份到了每人份数上限时置灰写原因 */
export function LandingChooser({ planId, plan, h, price }: { planId: string; plan: Plan; h: Holdings; price: string | null }) {
  const landings = useLandings(planId)
  usePageHead(`买${plan.name}`, '/plans')

  if (landings.isPending) {
    return (
      <div className={css.land} aria-busy="true">
        <Skeleton width={220} height={18} />
        <Skeleton height={160} />
      </div>
    )
  }

  if (landings.isError) {
    const err = landings.error
    const business = isApiError(err) && err.status === 409
    if (business) {
      return (
        <div className={css.card}>
          <p>{err.message}</p>
          <Btn href={href('/plans')} tone="soft">去选购</Btn>
        </div>
      )
    }
    return (
      <div className={css.card}>
        <LoadError error={err} onRetry={() => void landings.refetch()} what="可选项" />
      </div>
    )
  }

  const options = (landings.data ?? []).filter((l) => l.mode === 'new' || h.held.some((s) => s.id === l.subscription_id))
  const choice = landingChoice(options)

  if (choice.auto) {
    return <AutoLanding option={choice.auto} planId={planId} price={price} />
  }

  const go = (l: Landing) => goToCheckout(planId, l, price, false)

  return (
    <div className={css.land} data-screen="checkout-landing">
      <p className={css.landQ}>{choice.usable.length === 0 ? `${plan.name}现在不能再开一份` : `${plan.name}放到哪里？`}</p>
      <ChoiceList
        label={`${plan.name}放到哪里`}
        selected={null}
        onSelect={(key) => {
          const l = options.find((o) => optionKey(o) === key)
          if (l && !l.disabled_reason) go(l)
        }}
        items={options.map((l) => ({
          key: optionKey(l),
          label: optionLabel(l, plan, h),
          desc: l.disabled_reason ?? optionDesc(l, plan, h),
          disabled: !!l.disabled_reason,
        }))}
      />
      {choice.usable.length === 0 && (
        <div className={css.landAct}>
          <Btn href={href('/subs')} tone="soft">去我的套餐</Btn>
        </div>
      )}
    </div>
  )
}

function optionKey(l: Landing): string {
  return l.mode === 'new' ? 'new' : `${l.mode}:${l.subscription_id}`
}

function optionLabel(l: Landing, plan: Plan, h: Holdings): string {
  const s = l.subscription_id ? h.held.find((x) => x.id === l.subscription_id) : undefined
  switch (l.mode) {
    case 'new':
      return '另开一份'
    case 'renew':
      return s ? `续在${h.naming.who(s)}上` : '续在原来的那份上'
    case 'stack':
      return s ? `叠加到${h.naming.who(s)}` : '叠加到原来的那份'
    case 'change':
      return s ? `把${h.naming.who(s)}换成${plan.name}` : `换成${plan.name}`
  }
}

function optionDesc(l: Landing, plan: Plan, h: Holdings): string {
  const s = l.subscription_id ? h.held.find((x) => x.id === l.subscription_id) : undefined
  const expired = s !== undefined && !isLive(s)
  switch (l.mode) {
    case 'new':
      return '新链接，和现在的分开：各自的流量、各自到期'
    case 'renew':
      return expired ? '已过期，续上马上恢复 · 链接不变' : '链接不变，到期日往后延'
    case 'stack': {
      const once = plan.kind === 'one_time'
      const days = plan.prices[0]?.validity_days ?? null
      if (once && (s?.permanent || days === null)) return '总流量加上 · 链接不变'
      return '总流量加上，天数顺延 · 链接不变'
    }
    case 'change':
      return expired ? '已过期，换好马上恢复 · 链接不变' : '链接不变；原来那份没用完的部分按比例算进价钱'
  }
}

function goToCheckout(planId: string, l: Landing, price: string | null, replace: boolean) {
  const full = landingHref(planId, l, price)
  const hash = full.startsWith('#') ? full.slice(1) : full
  const [path, search] = hash.split('?')
  const query = search ? Object.fromEntries(new URLSearchParams(search)) : undefined
  navigate(path ?? '', { query, replace })
}

function AutoLanding({ option, planId, price }: { option: Landing; planId: string; price: string | null }) {
  useEffect(() => {
    goToCheckout(planId, option, price, true)
  }, [option, planId, price])
  return (
    <div className={css.land} aria-busy="true">
      <Skeleton height={200} />
    </div>
  )
}
