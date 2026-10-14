import { useMemo } from 'react'
import { useAppearance } from '../../queries'
import { heldSubs, makeNaming, siteNameOf } from './purchase'
import { useSubscriptionLinks, useSubscriptions, type Subscription } from './subscriptions'

/**
 * 手上的套餐与叫法：订阅列表与链接列表两条读接口（各自同键共享缓存），多处页面共用。
 * held 是还在手上的份（生效中 + 过期 30 天内能救回），naming 按它决定「一份 / 多份」的说法。
 */
export function useHoldings() {
  const subs = useSubscriptions()
  const links = useSubscriptionLinks()
  const appearance = useAppearance()
  const all = subs.data
  const held = useMemo<Subscription[]>(() => (all ? heldSubs(all) : []), [all])
  const naming = useMemo(() => makeNaming(held, links.data), [held, links.data])
  const site = siteNameOf(all ?? [], appearance.data?.theme?.branding.site_name || 'Pandora')
  const urlOf = (id: string) => links.data?.find((l) => l.subscription_id === id)?.url
  const linkOf = (id: string) => links.data?.find((l) => l.subscription_id === id)
  return { subs, links, held, naming, site, urlOf, linkOf }
}

export type Holdings = ReturnType<typeof useHoldings>
