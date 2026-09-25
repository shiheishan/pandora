/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/format 的 formatMoney / formatDateTime，依赖 ../../../ui 的 Button / Card / Empty / Input / QueryView / Select / Skeleton / useToast，依赖 ../../queries 的 useBalance，依赖 ../common 的支付方式、幂等键（useIntentKey / usePlacedOrder / endsIntent）、支付弹窗与 shortDate，依赖 ./api 与 ./model
 * [OUTPUT]: 默认导出 Wallet 页面组件（登记表 React.lazy 的目标）
 * [POS]: portal/screens/wallet 的入口：钱包（门户-05）。左列账户余额与充值（预设金额 / 自定义金额、支付方式、建单后走支付弹窗）和余额明细（契约待补·前端），右列兑换礼品卡；< 640 单列时礼品卡排在余额明细之前（查询卡面 → 立即兑换）与我的礼品卡；设计稿的 USDT 与「试试 GC-…」演示提示删除；充值与兑换的幂等键成功或 4xx 后丢弃，同额充值 30 分钟内再点重开刚建的那张单
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { formatDateTime, formatMoney } from '../../../core/format'
import { Button, Card, Empty, Input, QueryView, Select, Skeleton, useToast } from '../../../ui'
import { useBalance } from '../../queries'
import { methodKey, usePaymentMethods } from '../common/catalog'
import { endsIntent, useIntentKey, usePlacedOrder } from '../common/intent'
import { PaymentModal, type PayState } from '../common/PayFlow'
import { shortDate } from '../common/traffic'
import { useGiftPreview, useMyGiftCards, useRedeemGift, useTopup } from './api'
import { giftFace, giftNote, ledgerLabel, normalizeGiftCode, parseTopupAmount, redemptionGain, TOPUP_PRESETS } from './model'
import css from './Wallet.module.css'

export default function Wallet() {
  return (
    <div className={css.grid}>
      <div className={css.balanceArea}>
        <BalanceCard />
      </div>
      <div className={css.giftArea}>
        <GiftCardCard />
      </div>
      <div className={css.historyArea}>
        <HistoryCard />
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 账户余额与充值：金额按钮与输入框是元，建单转分；建单成功后走同一个支付弹窗
// ---------------------------------------------------------------------------
function BalanceCard() {
  const balance = useBalance()
  const methods = usePaymentMethods()
  const topup = useTopup()
  const intentKey = useIntentKey()
  const placed = usePlacedOrder<{ orderId: string; orderNo: string; amount: number; currency: string }>()
  const [amount, setAmount] = useState('100')
  const [methodChoice, setMethodChoice] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [pay, setPay] = useState<PayState | null>(null)

  const list = methods.data ?? []
  const method = list.find((m) => methodKey(m) === methodChoice) ?? list[0] ?? null

  function recharge() {
    const parsed = parseTopupAmount(amount)
    if ('error' in parsed) return setError(parsed.error)
    if (!method) return setError('暂无可用的支付方式')
    setError(null)
    const body = { amount: parsed.cents }
    // 刚建过同额的充值单、还没过期：重开它的支付，不再建第二张
    const again = placed.recall(body)
    if (again) return setPay({ phase: 'redirect', ...again, method })
    topup.mutate(
      { amount: parsed.cents, key: intentKey(body) },
      {
        onSuccess: (order) => {
          intentKey.reset()
          const created = { orderId: order.order_id, orderNo: order.order_no, amount: order.amount, currency: order.currency }
          placed.remember(body, created)
          setPay({ phase: 'redirect', ...created, method })
        },
        onError: (e) => {
          if (endsIntent(e)) intentKey.reset()
          setError(e.message || '充值失败，请稍后重试')
        },
      },
    )
  }

  return (
    <Card className={css.balance}>
      <div>
        <div className={css.caption}>账户余额</div>
        <div className={css.bigAmount}>{balance.data ? formatMoney(balance.data.balance, balance.data.currency) : balance.isError ? '—' : <Skeleton width={140} height={40} />}</div>
        <div className={css.hint}>可用于购买套餐、续费与流量包</div>
      </div>
      <div className={css.field}>
        <div className={css.caption}>充值金额</div>
        <div className={css.presets} role="radiogroup" aria-label="充值金额">
          {TOPUP_PRESETS.map((v) => (
            <button key={v} type="button" role="radio" aria-checked={amount === String(v)} className={css.preset} onClick={() => setAmount(String(v))}>
              ¥{v}
            </button>
          ))}
        </div>
        <Input
          mono
          inputMode="decimal"
          aria-label="其他金额（元）"
          placeholder="或输入其他金额"
          value={TOPUP_PRESETS.some((v) => String(v) === amount) ? '' : amount}
          onChange={(e) => setAmount(e.target.value.replace(/[^\d.]/g, ''))}
          error={error ?? undefined}
        />
      </div>
      <div className={css.payRow}>
        {methods.isPending ? (
          <Skeleton height={40} radius="var(--radius-md)" className={css.grow} />
        ) : (
          <Select
            aria-label="支付方式"
            fieldClassName={css.grow}
            value={method ? methodKey(method) : ''}
            placeholder={list.length ? undefined : '暂无可用的支付方式'}
            options={list.map((m) => ({ value: methodKey(m), label: m.label }))}
            onChange={(e) => setMethodChoice(e.target.value)}
          />
        )}
        <Button variant="primary" busy={topup.isPending} disabled={!method} onClick={recharge}>
          充值
        </Button>
      </div>
      <PaymentModal state={pay} onClose={() => setPay(null)} />
    </Card>
  )
}

// ---------------------------------------------------------------------------
// 余额明细（契约待补·前端）：最近 100 条，先显示 8 条
// ---------------------------------------------------------------------------
const HISTORY_PREVIEW = 8

function HistoryCard() {
  const balance = useBalance()
  const [all, setAll] = useState(false)
  return (
    <Card flush title="余额明细" className={css.listCard}>
      <QueryView query={balance} rows={3} isEmpty={(d) => d.history.length === 0} empty={<Empty bare title="还没有余额变动" description="充值、下单抵扣、礼品卡与佣金转入都会记在这里。" />}>
        {(d) => (
          <>
            <ul className={css.list}>
              {(all ? d.history : d.history.slice(0, HISTORY_PREVIEW)).map((h, i) => (
                <li key={`${h.at}-${i}`} className={css.historyRow}>
                  <span className={css.historyMain}>
                    <span className={css.historyKind}>{ledgerLabel(h.kind) ?? (h.memo || h.kind)}</span>
                    <span className={css.historyMeta}>
                      {formatDateTime(h.at)}
                      {ledgerLabel(h.kind) && h.memo ? ` · ${h.memo}` : ''}
                    </span>
                  </span>
                  <span className={h.delta > 0 ? css.plus : css.minus}>
                    {h.delta > 0 ? '+' : ''}
                    {formatMoney(h.delta, d.currency)}
                  </span>
                </li>
              ))}
            </ul>
            {!all && d.history.length > HISTORY_PREVIEW && (
              <button type="button" className={css.more} onClick={() => setAll(true)}>
                显示全部 {d.history.length} 条
              </button>
            )}
          </>
        )}
      </QueryView>
    </Card>
  )
}

// ---------------------------------------------------------------------------
// 礼品卡：先查询卡面，再兑换；兑换幂等（一张卡一个键）
// ---------------------------------------------------------------------------
function GiftCardCard() {
  const toast = useToast()
  const preview = useGiftPreview()
  const redeem = useRedeemGift()
  const intentKey = useIntentKey()
  const mine = useMyGiftCards()
  const [code, setCode] = useState('')
  const [error, setError] = useState<string | null>(null)

  const normalized = normalizeGiftCode(code)
  const card = preview.data && preview.variables === normalized ? preview.data.card : null

  function lookup() {
    if (normalized.length < 8) return setError('请输入完整的卡密')
    setError(null)
    preview.mutate(normalized, { onError: (e) => setError(e.message) })
  }

  function doRedeem() {
    const body = { code: normalized }
    redeem.mutate(
      { code: normalized, key: intentKey(body) },
      {
        onSuccess: (r) => {
          intentKey.reset()
          toast(`兑换成功：${r.summary.join('，')}`)
          setCode('')
          preview.reset()
        },
        onError: (e) => {
          if (endsIntent(e)) intentKey.reset()
          setError(e.message)
        },
      },
    )
  }

  return (
    <Card title="兑换礼品卡" className={css.gift}>
      <div className={css.giftRow}>
        <Input
          mono
          aria-label="礼品卡卡密"
          placeholder="GC-XXXX-XXXX-XXXX"
          className={css.upper}
          fieldClassName={css.grow}
          value={code}
          onChange={(e) => {
            setCode(e.target.value)
            setError(null)
          }}
          onKeyDown={(e) => e.key === 'Enter' && lookup()}
        />
        <Button onClick={lookup} busy={preview.isPending} disabled={!normalized}>
          查询
        </Button>
      </div>
      {card && (
        <div className={css.giftCard}>
          <div className={css.giftInfo}>
            <div className={css.caption}>礼品卡内容</div>
            <div className={css.giftFace}>{giftFace(card)}</div>
            <div className={css.caption}>{giftNote(card)}</div>
          </div>
          <Button variant="primary" busy={redeem.isPending} onClick={doRedeem}>
            立即兑换
          </Button>
        </div>
      )}
      {error && (
        <div className={css.error} role="alert">
          {error}
        </div>
      )}
      <div className={css.mine}>
        <div className={css.caption}>我的礼品卡</div>
        <QueryView query={mine} rows={2} empty={<div className={css.hint}>兑换过的礼品卡会显示在这里。</div>}>
          {(rows) => (
            <ul className={css.list}>
              {rows.map((r, i) => (
                <li key={`${r.redeemed_at}-${i}`} className={css.mineRow}>
                  <span className={css.mineCode}>{r.code_hint ?? r.template_name}</span>
                  <span className={css.mineGain}>{redemptionGain(r)}</span>
                  <span className={css.mineAt}>{shortDate(r.redeemed_at)}</span>
                </li>
              ))}
            </ul>
          )}
        </QueryView>
      </div>
    </Card>
  )
}
