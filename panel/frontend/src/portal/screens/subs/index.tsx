import { href, useHashLocation } from '../../../core/router'
import { Card, Empty, Skeleton } from '../../../ui'
import { usePageHead } from '../../head'
import { useUnattachedPackBytes } from '../../queries'
import type { PortalScreenProps } from '../index'
import { LoadError, Slot } from '../common/Blocks'
import { usePackCatalog } from '../common/catalog'
import { flowCss } from '../common/Flow'
import { useHoldings } from '../common/holdings'
import { LinkBox } from '../common/LinkBox'
import { ResultView } from '../common/Result'
import { ChangePick } from './ChangePick'
import { Detail } from './Detail'
import { NewPick } from './NewPick'
import { moveTargets, useSheets } from './sheets'
import { SubCard } from '../common/SubCard'
import css from './Subs.module.css'
import { Traffic } from './Traffic'
import { TransferBars } from './Transfer'

// ---------------------------------------------------------------------------
// 我的套餐（原型 home）与它的子页（地址见 .claude/rules/screens-portal.md）：
//   #/subs                    每份一张卡
//   #/subs/<id>/detail        节点与每天用量
//   #/subs/<id>/traffic       加流量
//   #/subs/<id>/change        换个套餐
//   #/subs/<id>/rotated       换新链接之后的完成页
//   #/subs/new                再买一份
// ---------------------------------------------------------------------------
export default function Subs({ rest }: PortalScreenProps) {
  const [first, second] = rest
  if (first === 'new') return <NewPick />
  if (first && second === 'traffic') return <Traffic id={first} />
  if (first && second === 'change') return <ChangePick id={first} />
  if (first && second === 'detail') return <Detail id={first} />
  if (first && second === 'rotated') return <Rotated id={first} />
  return <MySubs />
}

function MySubs() {
  const h = useHoldings()
  const packs = usePackCatalog()
  const unattached = useUnattachedPackBytes()
  const { actions, sheets } = useSheets(h)
  const minPack = packs.data?.length ? Math.min(...packs.data.map((p) => p.unit_amount)) : null

  if (h.subs.isPending) {
    return (
      <div className={css.cards} aria-busy="true">
        <Card>
          <Skeleton width={160} height={24} />
          <Skeleton height={8} />
          <Skeleton height={52} radius="var(--radius-md)" />
        </Card>
      </div>
    )
  }
  if (h.subs.isError) {
    return (
      <Card>
        <LoadError error={h.subs.error} onRetry={() => void h.subs.refetch()} what="套餐" />
      </Card>
    )
  }

  if (h.held.length === 0) {
    return (
      <div className={css.page}>
        <Slot name="portal.subscribe.notice" />
        <TransferBars h={h} unattached={unattached.data ?? 0} />
        <Empty
          title="你还没有套餐"
          description={h.subs.data.length > 0 ? '之前的套餐已经停用了。选一个套餐，拿到链接后添加到 App 就能用。' : '选一个套餐，拿到链接后添加到 App 就能用。'}
          action={
            <a className={flowCss.cta} href={href('/plans')}>
              去选购
            </a>
          }
        />
        <div className={css.foot}>
          <a className={flowCss.textButton} href={href('/wallet/redeem')} id="btn-home-redeem">
            有卡？去兑换
          </a>
        </div>
      </div>
    )
  }

  return (
    <div className={css.page}>
      <Slot name="portal.subscribe.notice" />
      <TransferBars h={h} unattached={unattached.data ?? 0} />
      <div className={css.cards}>
        {h.held.map((sub) => (
          <SubCard key={sub.id} sub={sub} naming={h.naming} link={h.linkOf(sub.id)} minPack={minPack} actions={actions} moveTo={moveTargets(h, sub)} />
        ))}
      </div>
      <a className={css.addRow} href={href('/subs/new')} id="btn-new-copy">
        <span className={css.addIcon} aria-hidden="true">
          ＋
        </span>
        <span className={css.addText}>
          <b>再买一份，分开用</b>
          <small>给家人或另一个人：单独的链接、单独的流量，和现在的互不影响</small>
        </span>
      </a>
      <div className={css.foot}>
        <a className={flowCss.textButton} href={href('/wallet/redeem')} id="btn-home-redeem">
          有卡？去兑换
        </a>
      </div>
      {sheets}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 换新链接之后：新链接与「添加到 App」放在最上面
// ---------------------------------------------------------------------------
function Rotated({ id }: { id: string }) {
  usePageHead('完成', '/subs')
  const h = useHoldings()
  const { query } = useHashLocation()
  const { actions, sheets } = useSheets(h)
  const sub = h.held.find((s) => s.id === id)
  if (h.subs.isPending || h.links.isPending) return <Skeleton height={200} />
  if (!sub) return <Empty title="找不到这一份" description="可能已经停用了。" action={<a href={href('/subs')}>回到我的套餐</a>} />
  const others = h.held.filter((s) => s !== sub)
  const old = query.get('old')
  return (
    <>
      <ResultView
        info={{
          title: '已换新链接',
          next: (
            <>
              <p className={flowCss.lead}>在{h.naming.multi ? `用「${h.naming.sn(sub)}」的` : '你的每台'}设备上把新链接添加到 App。设备不在身边？点「添加到 App」→「别的设备」，复制链接和说明发给对方。</p>
              <LinkBox sub={sub} url={h.urlOf(sub.id)} naming={h.naming} label="新链接" onImport={() => actions.onImport(sub)} />
            </>
          ),
          happened: [`${h.naming.who(sub)}的旧链接${old ? ` ····${old}` : ''} 已失效`],
          kept: ['到期日、流量都没变', ...(others.length ? [`${others.map((x) => `「${h.naming.sn(x)}」`).join('、')}不受影响，不用动`] : [])],
        }}
      />
      {sheets}
    </>
  )
}
