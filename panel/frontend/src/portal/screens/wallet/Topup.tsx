import { useState } from 'react'
import { formatMoney } from '../../../core/format'
import { Input } from '../../../ui'
import { Btn, cx, Num, Sheet } from '../../kit'
import { methodKey, usePaymentMethods } from '../common/catalog'
import { endsIntent, recallPayable, useIntentKey, usePlacedOrder } from '../common/intent'
import { useOrderPayable } from '../common/orders'
import { PaymentModal, PayMethodPicker, pickMethod, type PayState } from '../common/PayFlow'
import { useDebounced } from '../common/useDebounced'
import { useTopup } from './api'
import { parseTopupAmount, TOPUP_PRESETS } from './model'
import css from './Wallet.module.css'

const FEE_QUOTE_DEBOUNCE_MS = 300

export function TopupSheet({ open, onClose }: { open: boolean; onClose: () => void }) {
  const topup = useTopup()
  const intentKey = useIntentKey()
  const placed = usePlacedOrder<{ orderId: string; orderNo: string; amount: number }>()
  const payable = useOrderPayable()
  const [reopening, setReopening] = useState(false)
  const [preset, setPreset] = useState<number | 'other'>(100)
  const [custom, setCustom] = useState('')
  const [methodChoice, setMethodChoice] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [pay, setPay] = useState<PayState | null>(null)

  const amountText = preset === 'other' ? custom : String(preset)
  const parsed = parseTopupAmount(amountText)
  const settledAmount = useDebounced(amountText, FEE_QUOTE_DEBOUNCE_MS)
  const quotedAmount = parseTopupAmount(settledAmount)
  const methods = usePaymentMethods(!('error' in quotedAmount) ? quotedAmount.cents : undefined)
  const method = pickMethod(methods.data, methodChoice)

  async function recharge() {
    const p = parseTopupAmount(amountText)
    if ('error' in p) return setError(p.error)
    if (!method) return setError('暂无可用的支付方式')
    setError(null)
    const body = { amount: p.cents }
    setReopening(true)
    const again = await recallPayable(placed, body, (o) => payable(o.orderId)).finally(() => setReopening(false))
    if (again) return setPay({ phase: 'redirect', ...again, method })
    topup.mutate(
      { amount: p.cents, key: intentKey.keyFor(body) },
      {
        onSuccess: (order) => {
          intentKey.reset()
          const created = { orderId: order.order_id, orderNo: order.order_no, amount: order.amount }
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
    <>
      <Sheet open={open && pay === null} onClose={onClose} title="充值" dismissible={!topup.isPending}>
        <div className={css.topup}>
          <div className={css.amtShow} aria-live="polite">
            <Num className={css.amtCur}>¥</Num>
            <Num className={css.amtVal}>{'error' in parsed ? amountText || '0' : (parsed.cents / 100).toString()}</Num>
          </div>
          <div className={css.amts} role="radiogroup" aria-label="充值金额">
            {TOPUP_PRESETS.map((v) => (
              <button
                key={v}
                type="button"
                role="radio"
                aria-checked={preset === v}
                className={cx(css.am, preset === v && css.amOn)}
                onClick={() => {
                  setPreset(v)
                  setError(null)
                }}
              >
                ¥{v}
              </button>
            ))}
            <button
              type="button"
              role="radio"
              aria-checked={preset === 'other'}
              className={cx(css.am, preset === 'other' && css.amOn)}
              onClick={() => setPreset('other')}
            >
              其他
            </button>
          </div>
          {preset === 'other' && (
            <Input
              autoFocus
              mono
              inputMode="decimal"
              aria-label="其他金额（元）"
              placeholder="输入金额，如 88"
              value={custom}
              onChange={(e) => {
                setCustom(e.target.value.replace(/[^\d.]/g, ''))
                setError(null)
              }}
              error={error ?? undefined}
            />
          )}
          <p className={css.faint}>最少 ¥1，单次最多 ¥50,000</p>
          {error && preset !== 'other' && (
            <p className={css.error} role="alert">
              {error}
            </p>
          )}
          <PayMethodPicker methods={methods} selected={method} onSelect={(m) => setMethodChoice(methodKey(m))} />
          <Btn tone="hot" full busy={topup.isPending || reopening} disabled={!method} onClick={() => void recharge()}>
            {'error' in parsed ? '充值' : <>充值 <Num>{formatMoney(parsed.cents)}</Num></>}
          </Btn>
        </div>
      </Sheet>
      <PaymentModal
        state={pay}
        onClose={() => {
          setPay(null)
          onClose()
        }}
        onUnpayable={placed.forget}
      />
    </>
  )
}
