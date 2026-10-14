import { href, useHashLocation } from '../../../core/router'
import { Card, Empty, Skeleton } from '../../../ui'
import { usePageHead } from '../../head'
import type { PortalScreenProps } from '../index'
import { LoadError } from '../common/Blocks'
import { usePackCatalog, usePlans } from '../common/catalog'
import { flowCss } from '../common/Flow'
import { useHoldings } from '../common/holdings'
import { ConfirmForm } from './Confirm'
import { Done } from './Done'
import { parseTarget, type Problem as ProblemKind } from './model'
import { Pay } from './Pay'

// ---------------------------------------------------------------------------
// 确认（#/checkout?…，地址见 model.ts）、付款（#/checkout/pay/<订单>）、完成（#/checkout/done/<订单>）
// ---------------------------------------------------------------------------
export default function Checkout({ rest }: PortalScreenProps) {
  const [step, orderId] = rest
  if (step === 'pay' && orderId) return <Pay orderId={orderId} />
  if (step === 'done' && orderId) return <Done orderId={orderId} />
  return <Confirm />
}

function Confirm() {
  const { query } = useHashLocation()
  const h = useHoldings()
  const plans = usePlans()
  const packs = usePackCatalog()
  const queries = [h.subs, plans, packs]
  if (queries.some((q) => q.isPending)) {
    return (
      <Card aria-busy="true" className={flowCss.narrow}>
        <Skeleton width={180} height={18} />
        <Skeleton height={200} />
      </Card>
    )
  }
  const failed = queries.find((q) => q.isError)
  if (failed) {
    return (
      <Card>
        <LoadError error={failed.error} onRetry={() => queries.forEach((q) => void q.refetch())} what="价格" />
      </Card>
    )
  }
  const parsed = parseTarget(query, h.held, plans.data!, packs.data!)
  if ('problem' in parsed) return <Problem kind={parsed.problem} />
  const t = parsed.target
  // 目标变了就整块重建：选项、余额开关与幂等键都从头来
  const identity = `${t.kind}:${'subId' in t ? t.subId : ''}:${'planId' in t ? t.planId : ''}:${'packId' in t ? t.packId : ''}`
  return <ConfirmForm key={identity} target={t} h={h} plans={plans.data!} packs={packs.data!} requested={query.get('price')} />
}

const PROBLEMS: Readonly<Record<ProblemKind, readonly [string, string]>> = {
  missing: ['还没选要买什么', '先到选购页挑一个套餐或流量包。'],
  plan_gone: ['这个套餐已经停售了', '去选购页看看别的套餐。'],
  pack_gone: ['这个流量包已经下架了', '去选购页看看别的流量包。'],
  sub_gone: ['这一份现在不能这样操作', '它可能已经停用了，或者不在你的账号下。可以另买一份。'],
}

function Problem({ kind }: { kind: ProblemKind }) {
  usePageHead('确认', '/plans')
  const [title, description] = PROBLEMS[kind]
  return (
    <Empty
      title={title}
      description={description}
      action={
        <a className={flowCss.cta} href={href('/plans', kind === 'pack_gone' ? { tab: 'packs' } : undefined)}>
          去选购
        </a>
      }
    />
  )
}
