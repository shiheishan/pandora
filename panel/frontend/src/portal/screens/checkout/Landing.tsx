import { useEffect } from 'react'
import { href, navigate } from '../../../core/router'
import { isApiError } from '../../../core/api'
import { Card, Skeleton } from '../../../ui'
import { usePageHead } from '../../head'
import { LoadError } from '../common/Blocks'
import type { Plan } from '../common/catalog'
import { ChoiceList, flowCss } from '../common/Flow'
import type { Holdings } from '../common/holdings'
import { useLandings, type Landing } from '../common/landings'
import { isLive } from '../common/subscriptions'
import { landingHref } from './model'

/** 买这个套餐前选「放到哪里」：不预选；只有一个选项时自动跳转 */
export function LandingChooser({ planId, plan, h, price }: { planId: string; plan: Plan; h: Holdings; price: string | null }) {
  const landings = useLandings(planId)
  usePageHead(`买${plan.name}`, '/plans')

  if (landings.isPending) {
    return (
      <Card aria-busy="true" className={flowCss.narrow}>
        <Skeleton width={220} height={18} />
        <Skeleton height={160} />
      </Card>
    )
  }

  if (landings.isError) {
    const err = landings.error
    const business = isApiError(err) && err.status === 409
    if (business) {
      return (
        <Card className={flowCss.narrow}>
          <p>{err.message}</p>
          <a className={flowCss.cta} href={href('/plans')}>
            去选购
          </a>
        </Card>
      )
    }
    return (
      <Card>
        <LoadError error={err} onRetry={() => void landings.refetch()} what="可选项" />
      </Card>
    )
  }

  const options = (landings.data ?? []).filter((l) => l.mode === 'new' || h.held.some((s) => s.id === l.subscription_id))

  if (options.length === 1) {
    return <AutoLanding option={options[0]!} planId={planId} price={price} />
  }

  const go = (l: Landing) => goToCheckout(planId, l, price, false)

  return (
    <div className={`${flowCss.stack} ${flowCss.narrow}`} data-screen="checkout-landing">
      <p className={flowCss.q}>{plan.name}放到哪里？</p>
      <ChoiceList
        label={`${plan.name}放到哪里`}
        selected={null}
        onSelect={(key) => {
          const l = options.find((o) => optionKey(o) === key)
          if (l) go(l)
        }}
        items={options.map((l) => ({ key: optionKey(l), label: optionLabel(l, plan, h), desc: optionDesc(l, plan, h) }))}
      />
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
    <Card aria-busy="true" className={flowCss.narrow}>
      <Skeleton height={200} />
    </Card>
  )
}
