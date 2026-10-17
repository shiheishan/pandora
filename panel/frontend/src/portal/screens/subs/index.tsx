import { href, useHashLocation } from '../../../core/router'
import { Empty, Skeleton } from '../../../ui'
import { Btn, Eyebrow, Hero, Meta, Row, Rows, SectionTitle } from '../../kit'
import { usePageHead } from '../../head'
import type { PortalScreenProps } from '../index'
import { LoadError, Slot } from '../common/Blocks'
import { flowCss } from '../common/Flow'
import { useHoldings } from '../common/holdings'
import { LinkBox } from '../common/LinkBox'
import { ResultView } from '../common/Result'
import { UpdateHelpLink } from '../common/UpdateHelp'
import { ChangePick } from './ChangePick'
import { Detail } from './Detail'
import { NewPick } from './NewPick'
import { useSheets } from './sheets'
import { Copy } from './Copy'
import { TopNotes } from './TopNotes'
import { Copies } from './Copies'
import css from './Subs.module.css'
import { useSubDomainProbe } from './probe'
import { Traffic } from './Traffic'

// ---------------------------------------------------------------------------
// 我的套餐（原型 home）与它的子页（地址见 .claude/rules/screens-portal.md）：
//   #/subs                    每份一张卡
//   #/subs/<id>/detail        节点与每天用量
//   #/subs/<id>/traffic       流量不够
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
  useSubDomainProbe()
  const h = useHoldings()
  const { actions, sheets } = useSheets(h)
  const { query } = useHashLocation()

  if (h.subs.isPending) {
    return (
      <div className={css.page} aria-busy="true">
        <Slot name="portal.home.banner" />
        <TopNotes />
        <Slot name="portal.subscribe.notice" />
        <Hero tone="empty">
          <Skeleton width={120} height={26} />
          <Skeleton height={48} />
          <Skeleton height={8} />
        </Hero>
        <SectionTitle as="h3" className={css.moreTitle}>更多</SectionTitle>
        <Rows>
          <Row icon="card" title="有卡？去兑换" href={href('/wallet/redeem')} />
        </Rows>
        <Slot name="portal.home.aside" />
      </div>
    )
  }
  if (h.subs.isError) {
    return (
      <div className={css.page}>
        <Slot name="portal.home.banner" />
        <TopNotes />
        <Slot name="portal.subscribe.notice" />
        <LoadError error={h.subs.error} onRetry={() => void h.subs.refetch()} what="套餐" />
        <SectionTitle as="h3" className={css.moreTitle}>更多</SectionTitle>
        <Rows>
          <Row icon="card" title="有卡？去兑换" href={href('/wallet/redeem')} />
        </Rows>
        <Slot name="portal.home.aside" />
      </div>
    )
  }

  const queryC = query.get('c')
  const matched = h.held.find((s) => s.id === queryC)
  const selected = matched ? matched.id : (h.held[0]?.id ?? '')
  const multi = h.held.length > 1

  return (
    <div className={css.page}>
      <Slot name="portal.home.banner" />
      <TopNotes />
      <Slot name="portal.subscribe.notice" />
      {h.held.length === 0 ? (
        <Hero tone="empty">
          <Eyebrow>{h.site}</Eyebrow>
          <h2 className={css.gone}>你还没有套餐</h2>
          <Meta className={css.gap10}>
            {h.subs.data && h.subs.data.length > 0
              ? '之前的套餐已经停用了。选一个套餐，拿到链接后添加到 App 就能用。'
              : '选一个套餐，拿到链接后添加到 App 就能用。'}
          </Meta>
          <ol className={css.steps}>
            <li>
              <b>1</b>选套餐、付款
            </li>
            <li>
              <b>2</b>复制链接
            </li>
            <li>
              <b>3</b>添加到 App
            </li>
          </ol>
          <div className={css.heroFoot}>
            <Btn tone="hot" grow href={href('/plans')}>
              去选购
            </Btn>
          </div>
        </Hero>
      ) : (
        <>
          {multi && <Copies held={h.held} naming={h.naming} selected={selected} />}
          {h.held.map((sub) => (
            <Copy
              key={sub.id}
              sub={sub}
              naming={h.naming}
              link={h.linkOf(sub.id)}
              site={h.site}
              actions={actions}
              hidden={multi && sub.id !== selected}
              tabId={multi ? `tab-${sub.id}` : undefined}
            />
          ))}
        </>
      )}
      <SectionTitle as="h3" className={css.moreTitle}>更多</SectionTitle>
      <Rows>
        {h.held.length > 0 && (
          <Row
            icon="plus"
            title="再买一份，分开用"
            sub="单独的链接、单独的流量，和现在的互不影响"
            href={href('/subs/new')}
          />
        )}
        <Row icon="card" title="有卡？去兑换" href={href('/wallet/redeem')} />
      </Rows>
      <Slot name="portal.home.aside" />
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
  if (!sub) {
    return (
      <Empty
        title="找不到这一份"
        description="可能已经停用了。"
        action={
          <Btn href={href('/subs')} tone="soft">
            回到我的套餐
          </Btn>
        }
      />
    )
  }
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
              <p className={flowCss.faint}>添加好以后在 App 里点一次更新，看到节点就说明新链接能用了；原来那条连着旧链接，可以删掉。</p>
              <UpdateHelpLink />
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
