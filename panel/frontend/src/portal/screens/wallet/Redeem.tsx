import { useState } from 'react'
import { formatMoney } from '../../../core/format'
import { Button, Card, Input, QueryView } from '../../../ui'
import { usePageHead } from '../../head'
import { useBalance } from '../../queries'
import { priceFor, usePlans } from '../common/catalog'
import { Callout, ChoiceList, flowCss } from '../common/Flow'
import { useHoldings, type Holdings } from '../common/holdings'
import { ImportSheet, type ImportTarget } from '../common/ImportSheet'
import { endsIntent, useIntentKey } from '../common/intent'
import { LinkBox } from '../common/LinkBox'
import { day, gb, heldSubs, makeNaming, money, type Naming } from '../common/purchase'
import { ResultView, type ResultInfo } from '../common/Result'
import type { Subscription } from '../common/subscriptions'
import { shortDate } from '../common/traffic'
import { useGiftPreview, useMyGiftCards, useRedeemGift, type GiftCard, type PlacementOption, type RedeemResult } from './api'
import { giftFace, giftNote, normalizeGiftCode, placementBlocked, redeemViews, redemptionGain, selectedView, simpleRedeem } from './model'
import css from './Wallet.module.css'

/**
 * 兑换卡（原型 redeem，#/wallet/redeem）：先查卡面，选项与默认值由服务端给；没有默认值时按钮置灰
 * 「先选一种用法」，每个选项旁写会发生什么；兑换时带 choice。只有一项可用时不问。
 */
export function Redeem() {
  usePageHead('兑换卡', '/wallet')
  const h = useHoldings()
  const preview = useGiftPreview()
  const mine = useMyGiftCards()
  const [code, setCode] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [done, setDone] = useState<Done | null>(null)
  const normalized = normalizeGiftCode(code)
  const card = preview.data && preview.variables === normalized ? preview.data.card : null

  function lookup() {
    if (normalized.length < 8) return setError('先输入完整的卡号')
    setError(null)
    preview.mutate(normalized, { onError: (e) => setError(e.message || '没找到这张卡，检查一下有没有输错。') })
  }

  if (done) return <ResultScreen info={done} h={h} />

  return (
    <div className={`${flowCss.stack} ${flowCss.narrow}`}>
      <div className={css.field}>
        <label className={css.caption} htmlFor="code-input">
          卡号
        </label>
        <div className={css.giftRow}>
          <Input
            id="code-input"
            mono
            autoComplete="off"
            placeholder="卡上的一串字母和数字"
            className={css.upper}
            fieldClassName={css.grow}
            value={code}
            onChange={(e) => {
              setCode(e.target.value)
              setError(null)
            }}
            onKeyDown={(e) => e.key === 'Enter' && lookup()}
          />
          <Button onClick={lookup} busy={preview.isPending} disabled={!normalized} id="btn-lookup">
            查询
          </Button>
        </div>
        {error && (
          <p className={css.error} role="alert">
            {error}
          </p>
        )}
      </div>
      {card && <CardPanel key={normalized} code={normalized} card={card} h={h} onDone={setDone} />}
      <Card title="兑换过的卡" className={css.mine}>
        <QueryView query={mine} rows={2} empty={<div className={css.hint}>兑换过的卡会显示在这里。</div>}>
          {(rows) => (
            <ul className={css.list}>
              {rows.map((r, i) => (
                <li key={`${r.redeemed_at}-${i}`} className={css.mineRow}>
                  <span className={css.mineCode}>{r.code_hint}</span>
                  <span className={css.mineGain}>{redemptionGain(r)}</span>
                  <span className={css.mineAt}>{shortDate(r.redeemed_at)}</span>
                </li>
              ))}
            </ul>
          )}
        </QueryView>
      </Card>
    </div>
  )
}

function CardPanel({ code, card, h, onDone }: { code: string; card: GiftCard; h: Holdings; onDone: (r: Done) => void }) {
  const plans = usePlans()
  const balance = useBalance()
  const redeem = useRedeemGift()
  const intentKey = useIntentKey()
  const [picked, setPicked] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const placement = card.placement
  const options = placement?.options ?? []
  const views = redeemViews({ card, held: h.held, naming: h.naming, monthPrice: (id) => plans.data?.find((p) => p.id === id) && priceFor(plans.data.find((p) => p.id === id)!, '1m')?.unit_amount }, options, placement?.default_key ?? '')
  const selected = selectedView(views, picked, placement?.default_key ?? '')
  const blocked = placement !== null && options.length === 0 && placementBlocked(card)
  const simple = placement === null || options.length === 0 ? simpleRedeem(card) : null

  function go() {
    const option = options.find((o) => o.key === selected?.key)
    const body = { code, ...(option ? { choice: { kind: option.kind, ...(option.subscription_id ? { subscription_id: option.subscription_id } : {}) } } : {}) }
    const before = new Set(h.held.map((s) => s.id))
    setError(null)
    redeem.mutate(
      { body, key: intentKey.keyFor(body) },
      {
        onSuccess: async (r) => {
          intentKey.reset()
          const [subs, links] = await Promise.all([h.subs.refetch(), h.links.refetch()])
          const fresh = heldSubs(subs.data ?? [])
          const bal = (await balance.refetch()).data?.balance ?? null
          onDone(redeemResult(card, option, r, fresh, before, makeNaming(fresh, links.data), bal))
        },
        onError: (e) => {
          if (endsIntent(e)) intentKey.reset()
          setError(e.message || '没兑换成，请稍后再试')
        },
      },
    )
  }

  return (
    <Card>
      <div className={css.caption}>这张卡</div>
      <h3 className={css.cardTitle}>{giftFace(card)}</h3>
      {giftNote(card) && <p className={css.hint}>{giftNote(card)}</p>}
      {blocked ? (
        <p className={css.error}>现在没有在用的套餐，这张卡暂时用不了。先续费再来兑换，卡不会过期。</p>
      ) : simple ? (
        <>
          <Callout id="redeem-sentence">{simple.sentence}</Callout>
          <Button variant="primary" block busy={redeem.isPending} onClick={go} id="btn-redeem-go">
            {simple.verb}
          </Button>
        </>
      ) : (
        <>
          {views.length > 1 && (
            <>
              <p className={flowCss.q}>{placement?.question || '这张卡怎么用？'}</p>
              <ChoiceList label="怎么用这张卡" selected={selected?.key ?? null} onSelect={setPicked} items={views.map((v) => ({ key: v.key, label: v.label, desc: v.desc, badge: v.badge }))} />
            </>
          )}
          {selected && <Callout id="redeem-sentence">{selected.sentence}</Callout>}
          <Button variant="primary" block busy={redeem.isPending} disabled={!selected} onClick={go} id="btn-redeem-go">
            {selected ? selected.verb : '先选一种用法'}
          </Button>
        </>
      )}
      {error && (
        <p className={css.error} role="alert">
          {error}
        </p>
      )}
    </Card>
  )
}

type Done = ResultInfo & { newSub?: Subscription }

function ResultScreen({ info, h }: { info: Done; h: Holdings }) {
  usePageHead('完成', '/wallet')
  const [importing, setImporting] = useState<ImportTarget | null>(null)
  const sub = info.newSub ? (h.held.find((s) => s.id === info.newSub!.id) ?? info.newSub) : undefined
  const next = sub ? (
    <>
      <p className={flowCss.lead}>在要用它的设备上把新链接添加到 App：</p>
      <LinkBox
        sub={sub}
        url={h.urlOf(sub.id)}
        naming={h.naming}
        label="新链接"
        onImport={() => {
          const url = h.urlOf(sub.id)
          if (url) setImporting({ sub, url })
        }}
      />
    </>
  ) : (
    info.next
  )
  return (
    <>
      <ResultView info={{ ...info, next }} />
      <ImportSheet target={importing} naming={h.naming} onClose={() => setImporting(null)} />
    </>
  )
}

/** 兑换好了：按选定的用法写，不用服务端 summary（那里的话术带「订阅」等词） */
function redeemResult(card: GiftCard, o: PlacementOption | undefined, r: RedeemResult, fresh: readonly Subscription[], before: ReadonlySet<string>, naming: Naming, balance: number | null): Done {
  const sub = fresh.find((s) => s.id === o?.subscription_id)
  const who = sub ? naming.who(sub) : '这一份'
  const end = sub?.current_period_end ? day(sub.current_period_end) : o?.new_period_end ? day(o.new_period_end) : ''
  const np = card.plan_name ?? r.plan_granted ?? ''
  const balanceLine = r.balance ? [`余额 +${formatMoney(r.balance)}${balance !== null ? `，现在余额 ${money(balance)}` : ''}（结账时自动先用）`] : []
  const prize = r.prize_label ? [`抽中了「${r.prize_label}」`] : []
  if (o?.kind === 'new') {
    const created = fresh.find((s) => !before.has(s.id))
    return {
      title: '新的一份开好了',
      happened: [`${created ? naming.dn(created) : np}，用到 ${created?.current_period_end ? day(created.current_period_end) : end}`],
      kept: before.size ? ['原来的照常用，链接没变'] : [],
      changeable: '可以在「我的套餐」里给它起个名字，好分清。',
      newSub: created,
    }
  }
  if (o?.kind === 'change') {
    const credit = o.credit ?? 0
    return {
      title: o.expired ? '已恢复使用' : `已换成${np}`,
      next: <p className={flowCss.lead}>在 App 里点一次「更新」，就能看到{np}的节点。不用重新添加。</p>,
      happened: [`${naming.multi && sub ? `「${naming.sn(sub)}」` : '你原来的套餐'}现在是${np}，用到 ${end}`, ...(credit > 0 ? [`${money(credit)} 已退到余额${balance !== null ? `，现在余额 ${money(balance)}` : ''}（在「钱包」里，可用于续费、加流量、买套餐）`] : [])],
      kept: ['链接没变'],
    }
  }
  if (o?.kind === 'renew' || o?.kind === 'extend_days') {
    return { title: '兑换好了', happened: [...prize, `${who}用到 ${end}${o.period_end ? `（原来 ${day(o.period_end)}）` : ''}`, ...balanceLine], kept: ['链接没变，设备不用重新添加'] }
  }
  if (o?.kind === 'reset_traffic') {
    return { title: '流量已清零重算', happened: [...prize, `${who}这个月又有 ${gb(o.traffic_cap ?? 0)} 能用`, ...balanceLine], kept: ['链接没变', ...(end ? [`到期日不变（${end}）`] : [])] }
  }
  if (o?.kind === 'add_traffic' || r.traffic_bytes) {
    return {
      title: `已加 ${gb(r.traffic_bytes ?? 0)}`,
      happened: [...prize, sub ? `${gb(r.traffic_bytes ?? 0)} 加到了${who}，用完为止` : `${gb(r.traffic_bytes ?? 0)} 流量包先存着，有在用的套餐时可以加上`, ...balanceLine],
      kept: sub ? ['链接没变，不用重新添加'] : [],
    }
  }
  return { title: '兑换好了', happened: [...prize, ...(balanceLine.length ? balanceLine : ['已经生效'])], kept: [] }
}
