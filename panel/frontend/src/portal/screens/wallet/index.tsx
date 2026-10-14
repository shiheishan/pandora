import { useState } from 'react'
import { formatDateTime, formatMoney } from '../../../core/format'
import { href } from '../../../core/router'
import { Button, Card, Empty, Input, QueryView, Select, Skeleton } from '../../../ui'
import { useBalance } from '../../queries'
import type { PortalScreenProps } from '../index'
import { methodKey, usePaymentMethods } from '../common/catalog'
import { endsIntent, recallPayable, useIntentKey, usePlacedOrder } from '../common/intent'
import { useOrderPayable } from '../common/orders'
import { PaymentModal, type PayState } from '../common/PayFlow'
import { useTopup } from './api'
import { ledgerLabel, parseTopupAmount, TOPUP_PRESETS } from './model'
import { Redeem } from './Redeem'
import css from './Wallet.module.css'

export default function Wallet({ rest }: PortalScreenProps) {
  if (rest[0] === 'redeem') return <Redeem />
  return (
    <div className={css.grid}>
      <div className={css.balanceArea}>
        <BalanceCard />
      </div>
      <div className={css.giftArea}>
        <a className={css.listRow} href={href('/wallet/redeem')} id="btn-redeem">
          <span>
            <b>兑换卡</b>
            <small>套餐卡、加时长卡、流量重置卡</small>
          </span>
          <span aria-hidden="true">›</span>
        </a>
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
  const payable = useOrderPayable()
  const [reopening, setReopening] = useState(false)
  const [amount, setAmount] = useState('100')
  const [methodChoice, setMethodChoice] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [pay, setPay] = useState<PayState | null>(null)

  const list = methods.data ?? []
  const method = list.find((m) => methodKey(m) === methodChoice) ?? list[0] ?? null

  async function recharge() {
    const parsed = parseTopupAmount(amount)
    if ('error' in parsed) return setError(parsed.error)
    if (!method) return setError('暂无可用的支付方式')
    setError(null)
    const body = { amount: parsed.cents }
    // 刚建过同额的充值单、还能付：重开它的支付，不再建第二张；已取消、超时或付掉就忘掉重建
    setReopening(true)
    const again = await recallPayable(placed, body, (o) => payable(o.orderId)).finally(() => setReopening(false))
    if (again) return setPay({ phase: 'redirect', ...again, method })
    topup.mutate(
      { amount: parsed.cents, key: intentKey.keyFor(body) },
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
        <div className={css.hint}>买套餐、续费、买流量包都能用，结账时自动先用。不能提现。</div>
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
        <Button variant="primary" busy={topup.isPending || reopening} disabled={!method} onClick={() => void recharge()}>
          充值
        </Button>
      </div>
      <PaymentModal state={pay} onClose={() => setPay(null)} onUnpayable={placed.forget} />
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
    <Card flush title="余额记录" className={css.listCard}>
      <QueryView query={balance} rows={3} isEmpty={(d) => d.history.length === 0} empty={<Empty bare title="还没有余额变动" description="充值、下单用余额、兑换卡、换套餐退回与佣金转入都会记在这里。" />}>
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
