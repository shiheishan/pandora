import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Card, Input, QueryView, useToast } from '../../../ui'
import { useIntentKey } from '../../actions'
import { planResetPricesSchema, useInvalidatePlans, usePlanResetPrices, type PlanDetail, type PlanResetPrices } from './api'
import { useCatalogFailure } from './failure'
import { parseAmount } from './model'
import css from './Plans.module.css'

export function ResetPriceCard({ plan }: { plan: PlanDetail }) {
  const query = usePlanResetPrices(plan.id)

  return (
    <Card
      flush
      title={
        <>
          流量重置价格 <span className={css.cardSub}>用户在门户花钱把这一份当期已用流量清零；不填就不卖</span>
        </>
      }
    >
      <QueryView query={query} rows={1}>
        {(data) => <ResetPriceEditor key={data.reset_prices.map((p) => `${p.currency}:${p.unit_amount}`).join(',')} plan={plan} data={data} />}
      </QueryView>
    </Card>
  )
}

function amountToYuan(cents: number | undefined): string {
  if (cents === undefined) return ''
  return (cents / 100).toFixed(2).replace(/\.00$/, '').replace(/(\.[1-9])0$/, '$1')
}

function ResetPriceEditor({ plan, data }: { plan: PlanDetail; data: PlanResetPrices }) {
  const api = useApi()
  const toast = useToast()
  const fail = useCatalogFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidatePlans()

  const initialCny = data.reset_prices.find((p) => p.currency === 'CNY')?.unit_amount
  const initialUsd = data.reset_prices.find((p) => p.currency === 'USD')?.unit_amount

  const [cnyText, setCnyText] = useState(() => amountToYuan(initialCny))
  const [usdText, setUsdText] = useState(() => amountToYuan(initialUsd))
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)

  const writable = plan.status !== 'archived'

  const currentCnyAmount = cnyText.trim() === '' ? null : parseAmount(cnyText)
  const currentUsdAmount = usdText.trim() === '' ? null : parseAmount(usdText)

  const dirty =
    (currentCnyAmount ?? null) !== (initialCny ?? null) ||
    (currentUsdAmount ?? null) !== (initialUsd ?? null) ||
    (cnyText.trim() === '' && initialCny !== undefined) ||
    (usdText.trim() === '' && initialUsd !== undefined)

  const save = async () => {
    const nextErrors: Record<string, string> = {}
    const items: Array<{ currency: 'CNY' | 'USD'; unit_amount: number }> = []

    if (cnyText.trim() !== '') {
      const val = parseAmount(cnyText)
      if (val === null || val <= 0) {
        nextErrors.cny = '重置价格须大于 0，最多两位小数'
      } else {
        items.push({ currency: 'CNY', unit_amount: val })
      }
    }

    if (usdText.trim() !== '') {
      const val = parseAmount(usdText)
      if (val === null || val <= 0) {
        nextErrors.usd = '重置价格须大于 0，最多两位小数'
      } else {
        items.push({ currency: 'USD', unit_amount: val })
      }
    }

    if (Object.keys(nextErrors).length > 0) {
      setErrors(nextErrors)
      return
    }

    // 保证按币种升序 CNY, USD
    items.sort((a, b) => a.currency.localeCompare(b.currency))

    setBusy(true)
    const body = { reset_prices: items }
    try {
      await api.put(`v1/plans/${encodeURIComponent(plan.id)}/reset-prices`, planResetPricesSchema, {
        body,
        idempotencyKey: intent.keyFor([plan.id, body]),
      })
      intent.reset()
      toast(items.length > 0 ? '流量重置价格已保存' : '已停止出售流量重置')
    } catch (e) {
      fail(e, {
        fields: (f) => {
          const errs: Record<string, string> = {}
          for (const [k, v] of Object.entries(f)) {
            if (k.startsWith('reset_prices.0.')) {
              if (items[0]?.currency === 'CNY') errs.cny = v
              else if (items[0]?.currency === 'USD') errs.usd = v
            } else if (k.startsWith('reset_prices.1.')) {
              if (items[1]?.currency === 'USD') errs.usd = v
              else if (items[1]?.currency === 'CNY') errs.cny = v
            } else {
              errs[k] = v
            }
          }
          setErrors(errs)
        },
        intent,
      })
    } finally {
      setBusy(false)
      void invalidate()
    }
  }

  return (
    <>
      <div className={css.cardPad}>
        {errors.reset_prices && (
          <p className={css.note} style={{ marginBottom: 'var(--space-12)' }}>
            {errors.reset_prices}
          </p>
        )}
        <div className={css.grid2}>
          <Input
            size="sm"
            mono
            label="CNY 价格（元）"
            placeholder="留空不卖"
            inputMode="decimal"
            value={cnyText}
            disabled={!writable}
            onChange={(e) => {
              setCnyText(e.target.value)
              setErrors({})
            }}
            error={errors.cny}
          />
          <Input
            size="sm"
            mono
            label="USD 价格（元）"
            placeholder="留空不卖"
            inputMode="decimal"
            value={usdText}
            disabled={!writable}
            onChange={(e) => {
              setUsdText(e.target.value)
              setErrors({})
            }}
            error={errors.usd}
          />
        </div>
      </div>
      {writable && (
        <div className={css.cardFoot}>
          <span className={css.small}>整组替换；留空表示不在该币种下出售流量重置</span>
          <span className={css.spacer} />
          <Button size="sm" variant="outline" busy={busy} disabled={!dirty} onClick={() => void save()}>
            保存重置价格
          </Button>
        </div>
      )}
    </>
  )
}
