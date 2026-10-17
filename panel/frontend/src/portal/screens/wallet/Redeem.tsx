import { useState, type Dispatch, type SetStateAction } from 'react'
import { formatMoney } from '../../../core/format'
import { Input, QueryView } from '../../../ui'
import { usePageHead } from '../../head'
import { useBalance } from '../../queries'
import { Btn, Eyebrow, Num, Row, Rows, SectionTitle, Sheet } from '../../kit'
import { priceFor, usePlans } from '../common/catalog'
import { Callout, ChoiceList, flowCss } from '../common/Flow'
import { useHoldings } from '../common/holdings'
import { ImportSheet, type ImportTarget } from '../common/ImportSheet'
import { endsIntent, useIntentKey } from '../common/intent'
import { LinkBox } from '../common/LinkBox'
import { day, gb, heldSubs, makeNaming, money, type Naming } from '../common/purchase'
import { ResultView, type ResultInfo } from '../common/Result'
import { UpdateHelpLink } from '../common/UpdateHelp'
import type { Subscription } from '../common/subscriptions'
import { shortDate } from '../common/traffic'
import {
  useGiftPreview,
  useMyGiftCards,
  useRedeemGift,
  type GiftCard,
  type PlacementOption,
  type RedeemResult,
} from './api'
import {
  giftFace,
  giftKind,
  giftNote,
  normalizeGiftCode,
  placementBlocked,
  redeemViews,
  redemptionGain,
  selectedView,
  simpleRedeem,
} from './model'
import css from './Redeem.module.css'

/**
 * 兑换卡弹层与结果页：先查卡面，选项与默认值由服务端给；没有默认值时按钮置灰「先选一种用法」。
 */

export type RedeemDone = ResultInfo & { newSub?: Subscription }

export function RedeemSheet({
  open,
  onClose,
  onDone,
}: {
  open: boolean
  onClose: () => void
  onDone: (r: RedeemDone) => void
}) {
  const preview = useGiftPreview()
  const h = useHoldings()
  const plans = usePlans()
  const [code, setCode] = useState('')
  const [error, setError] = useState<string | null>(null)
  // 兑换在 RedeemSheet 这一层：兑换中弹层不能关
  const redeem = useRedeemGift()

  const normalized = normalizeGiftCode(code)
  const card = preview.data && preview.variables === normalized ? preview.data.card : null
  const placement = card?.placement ?? null
  const options = placement?.options ?? []
  const views =
    card
      ? redeemViews(
          {
            card,
            held: h.held,
            naming: h.naming,
            monthPrice: (id) => plans.data?.find((p) => p.id === id) && priceFor(plans.data.find((p) => p.id === id)!, '1m')?.unit_amount,
          },
          options,
          placement?.default_key ?? '',
        )
      : []

  const title = !card ? '兑换卡' : views.length > 1 ? placement?.question || '这张卡怎么用？' : '兑换这张卡'

  function handleClose() {
    preview.reset()
    setCode('')
    setError(null)
    onClose()
  }

  return (
    <Sheet open={open} onClose={handleClose} title={title} dismissible={!redeem.isPending}>
      {open && (
        <RedeemBody
          code={code}
          setCode={setCode}
          error={error}
          setError={setError}
          preview={preview}
          normalized={normalized}
          card={card}
          views={views}
          redeem={redeem}
          holdings={h}
          onClose={handleClose}
          onDone={onDone}
        />
      )}
    </Sheet>
  )
}

function RedeemBody({
  code,
  setCode,
  error,
  setError,
  preview,
  normalized,
  card,
  views,
  redeem,
  holdings: h,
  onClose,
  onDone,
}: {
  code: string
  setCode: Dispatch<SetStateAction<string>>
  error: string | null
  setError: Dispatch<SetStateAction<string | null>>
  preview: ReturnType<typeof useGiftPreview>
  normalized: string
  card: GiftCard | null
  views: ReturnType<typeof redeemViews>
  redeem: ReturnType<typeof useRedeemGift>
  holdings: ReturnType<typeof useHoldings>
  onClose: () => void
  onDone: (r: RedeemDone) => void
}) {
  const balance = useBalance()
  const intentKey = useIntentKey()
  const [picked, setPicked] = useState<string | null>(null)
  const [panelError, setPanelError] = useState<string | null>(null)

  const placement = card?.placement ?? null
  const options = placement?.options ?? []
  const selected = selectedView(views, picked, placement?.default_key ?? '')
  const blocked = placement !== null && options.length === 0 && card ? placementBlocked(card) : false
  const simple = card && (placement === null || options.length === 0) ? simpleRedeem(card) : null

  function lookup() {
    if (normalized.length < 8) return setError('先输入完整的卡号')
    setError(null)
    preview.mutate(normalized, { onError: (e) => setError(e.message || '没找到这张卡，检查一下有没有输错。') })
  }

  function toHistory() {
    onClose()
    requestAnimationFrame(() => document.getElementById('gift-history')?.scrollIntoView({ block: 'start' }))
  }

  function backToStep1() {
    preview.reset()
    setPicked(null)
    setPanelError(null)
  }

  function go() {
    if (!card) return
    const option = options.find((o) => o.key === selected?.key)
    const body = { code: normalized, ...(option ? { choice: { kind: option.kind, ...(option.subscription_id ? { subscription_id: option.subscription_id } : {}) } } : {}) }
    const before = new Set(h.held.map((s) => s.id))
    setPanelError(null)
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
          setPanelError(e.message || '没兑换成，请稍后再试')
        },
      },
    )
  }

  if (!card) {
    return (
      <>
        <p className={css.p}>套餐卡、加时长卡、流量重置卡、送流量卡和盲盒都在这里兑换。余额卡兑换后直接进钱包。</p>
        <Input
          id="code-input"
          aria-label="卡号"
          mono
          autoComplete="off"
          autoFocus
          placeholder="卡上的一串字母和数字"
          className={css.code}
          value={code}
          onChange={(e) => {
            setCode(e.target.value)
            setError(null)
          }}
          onKeyDown={(e) => e.key === 'Enter' && lookup()}
        />
        {error && (
          <p className={css.error} role="alert">
            {error}
          </p>
        )}
        <Btn tone="hot" full className={css.go} busy={preview.isPending} disabled={!normalized} onClick={lookup}>
          兑换
        </Btn>
        <button type="button" className={css.link} onClick={toHistory}>
          兑换过的卡 ›
        </button>
      </>
    )
  }

  return (
    <>
      <div className={css.gc}>
        <Eyebrow>{giftKind(card)}</Eyebrow>
        <h3 className={css.gcName}>{giftFace(card)}</h3>
        <Num className={css.gcCode}>{normalized}</Num>
        {giftNote(card) && <p className={css.gcNote}>{giftNote(card)}</p>}
      </div>
      {blocked ? (
        <p className={css.error}>现在没有在用的套餐，这张卡暂时用不了。先续费再来兑换，卡不会过期。</p>
      ) : simple ? (
        <>
          <Callout id="redeem-sentence">{simple.sentence}</Callout>
          <Btn tone="hot" full className={css.go} busy={redeem.isPending} onClick={go} id="btn-redeem-go">
            {simple.verb}
          </Btn>
        </>
      ) : (
        <>
          {views.length > 1 && (
            <ChoiceList
              label="怎么用这张卡"
              selected={selected?.key ?? null}
              onSelect={setPicked}
              items={views.map((v) => ({ key: v.key, label: v.label, desc: v.desc, badge: v.badge }))}
            />
          )}
          {selected && <Callout id="redeem-sentence">{selected.sentence}</Callout>}
          <Btn
            tone="hot"
            full
            className={css.go}
            busy={redeem.isPending}
            disabled={!selected}
            onClick={go}
            id="btn-redeem-go"
          >
            {selected ? selected.verb : '先选一种用法'}
          </Btn>
        </>
      )}
      {panelError && (
        <p className={css.error} role="alert">
          {panelError}
        </p>
      )}
      {!redeem.isPending && (
        <button type="button" className={css.link} onClick={backToStep1}>
          换一张卡
        </button>
      )}
    </>
  )
}

export function RedeemResult({ info }: { info: RedeemDone }) {
  const h = useHoldings()
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

export function GiftHistory() {
  const mine = useMyGiftCards()
  return (
    <section id="gift-history" className={css.history} aria-label="兑换过的卡">
      <SectionTitle className={css.historyTitle}>兑换过的卡</SectionTitle>
      <QueryView query={mine} rows={2} empty={<p className={css.hint}>兑换过的卡会显示在这里。</p>}>
        {(rows) => (
          <Rows>
            {rows.map((r, i) => (
              <Row
                key={`${r.redeemed_at}-${i}`}
                icon="card"
                iconStyle="round"
                title={redemptionGain(r)}
                sub={`${shortDate(r.redeemed_at)} · ${r.code_hint}`}
              />
            ))}
          </Rows>
        )}
      </QueryView>
    </section>
  )
}

/** 兑换好了：按选定的用法写，不用服务端 summary（那里的话术带「订阅」等词） */
function redeemResult(
  card: GiftCard,
  o: PlacementOption | undefined,
  r: RedeemResult,
  fresh: readonly Subscription[],
  before: ReadonlySet<string>,
  naming: Naming,
  balance: number | null,
): RedeemDone {
  const sub = fresh.find((s) => s.id === o?.subscription_id)
  const who = sub ? naming.who(sub) : '这一份'
  const end = sub?.current_period_end ? day(sub.current_period_end) : o?.new_period_end ? day(o.new_period_end) : ''
  const np = card.plan_name ?? r.plan_granted ?? ''
  const balanceLine = r.balance ? [`余额 +${formatMoney(r.balance)}${balance !== null ? `，现在余额 ${money(balance)}` : ''}（结账时自动先用）`] : []
  const prize = r.prize_label ? [`抽中了「${r.prize_label}」`] : []
  if (o?.kind === 'new') {
    const created = fresh.find((s) => !before.has(s.id))
    const newEnd = o?.new_permanent ? '永久有效' : created?.current_period_end ? day(created.current_period_end) : end
    return {
      title: '新的一份开好了',
      happened: [`${created ? naming.dn(created) : np}，${newEnd}`],
      kept: before.size ? ['原来的照常用，链接没变'] : [],
      changeable: '可以在「我的套餐」里给它起个名字，好分清。',
      newSub: created,
    }
  }
  if (o?.kind === 'change') {
    const credit = o.credit ?? 0
    const changeEnd = o.new_permanent ? '永久有效' : end
    return {
      title: o.expired ? '已恢复使用' : `已换成${np}`,
      next: (
        <>
          <p className={flowCss.lead}>在 App 里点一次「更新」，就能看到{np}的节点。不用重新添加。</p>
          <UpdateHelpLink />
        </>
      ),
      happened: [
        `${naming.multi && sub ? `「${naming.sn(sub)}」` : '你原来的套餐'}现在是${np}，${changeEnd}`,
        ...(credit > 0 ? [`${money(credit)} 已退到余额${balance !== null ? `，现在余额 ${money(balance)}` : ''}（在「钱包」里，可用于续费、买套餐）`] : []),
      ],
      kept: ['链接没变'],
    }
  }
  if (o?.kind === 'stack') {
    const stackEnd = o.new_permanent ? '永久有效' : end
    return {
      title: '已叠加',
      happened: [`${who}总流量加上了${o.new_permanent ? '，永久有效' : stackEnd ? `，用到 ${stackEnd}` : ''}`, ...balanceLine],
      kept: ['链接没变，设备不用重新添加'],
    }
  }
  if (o?.kind === 'renew' || o?.kind === 'extend_days') {
    const renewEnd = o.new_permanent ? '永久有效' : end
    return {
      title: '兑换好了',
      happened: [
        ...prize,
        `${who}${o.new_permanent ? '永久有效' : `用到 ${renewEnd}`}${o.period_end && !o.new_permanent ? `（原来 ${day(o.period_end)}）` : ''}`,
        ...balanceLine,
      ],
      kept: ['链接没变，设备不用重新添加'],
    }
  }
  if (o?.kind === 'reset_traffic') {
    return {
      title: '流量已清零重算',
      happened: [...prize, `${who}本期又有 ${gb(o.traffic_cap ?? 0)} 能用`, ...balanceLine],
      kept: ['链接没变', ...(end ? [`到期日不变（${end}）`] : [])],
    }
  }
  if (o?.kind === 'add_traffic' || r.traffic_bytes) {
    return {
      title: `已加 ${gb(r.traffic_bytes ?? 0)}`,
      happened: [
        ...prize,
        sub
          ? `${gb(r.traffic_bytes ?? 0)} 加到了${who}${sub.plan_kind === 'one_time' ? '的总流量上' : '本期的流量上，只在本期有效'}`
          : `本期流量 +${gb(r.traffic_bytes ?? 0)}`,
        ...balanceLine,
      ],
      kept: sub ? ['链接没变，不用重新添加'] : [],
    }
  }
  return { title: '兑换好了', happened: [...prize, ...(balanceLine.length ? balanceLine : ['已经生效'])], kept: [] }
}
