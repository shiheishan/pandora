import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Input, Modal, useToast } from '../../../ui'
import { useFailure } from '../../actions'
import { purchaseLimitErrors, purchaseLimitsBody, purchaseLimitsFormFrom, type PurchaseLimitsForm } from './model'
import billingCss from './Billing.module.css'
import css from './Orders.module.css'
import { purchaseLimitsSchema, type PurchaseLimits } from './schemas'

// 放在 billing 前缀下：useInvalidateBilling 整段失效时一并刷新
const PURCHASE_LIMITS_KEY = ['admin', 'billing', 'purchase-limits'] as const

export function PurchaseLimitsDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  return open ? <PurchaseLimitsForm onClose={onClose} /> : null
}

function PurchaseLimitsForm({ onClose }: { onClose: () => void }) {
  const api = useApi()
  const client = useQueryClient()
  const toast = useToast()
  const fail = useFailure()
  const q = useQuery({
    queryKey: PURCHASE_LIMITS_KEY,
    queryFn: ({ signal }) => api.get('v1/settings/purchase', purchaseLimitsSchema, { signal }),
  })
  // 存输入框原文：清空时不会悄悄变成 0（= 不限），保存前校验再转数字
  const [draft, setDraft] = useState<PurchaseLimitsForm | null>(null)
  const [errors, setErrors] = useState<Record<string, string>>({})

  const save = useMutation({
    mutationFn: (body: PurchaseLimits) => api.post('v1/settings/purchase', purchaseLimitsSchema, { body }),
    onSuccess: () => {
      toast('购买限制已保存')
      void client.invalidateQueries({ queryKey: PURCHASE_LIMITS_KEY })
      onClose()
    },
    onError: (e) => fail(e, (f) => setErrors(f)),
  })

  const form = draft ?? (q.data ? purchaseLimitsFormFrom(q.data) : null)
  const submit = () => {
    if (!form) return
    const local = purchaseLimitErrors(form)
    if (Object.keys(local).length) return setErrors(local)
    setErrors({})
    save.mutate(purchaseLimitsBody(form))
  }

  return (
    <Modal
      open
      onClose={onClose}
      dismissible={!save.isPending}
      title="购买限制"
      actions={
        <>
          <Button size="dialog" onClick={onClose} disabled={save.isPending}>取消</Button>
          <Button size="dialog" variant="primary" busy={save.isPending} disabled={!form} onClick={submit}>保存</Button>
        </>
      }
    >
      {!form ? (
        <p>加载中…</p>
      ) : (
        <div className={billingCss.dialogBody}>
          <Input
            label="每人同时未支付的订单最多几笔"
            hint="到了上限用户要先付款或等旧单过期才能再下单；已过付款期限的单不算。0 表示不限"
            inputMode="numeric"
            value={form.max_unpaid_orders}
            error={errors.max_unpaid_orders}
            disabled={save.isPending}
            onChange={(e) => {
              setDraft({ ...form, max_unpaid_orders: e.target.value })
              if (errors.max_unpaid_orders) setErrors((prev) => ({ ...prev, max_unpaid_orders: '' }))
            }}
          />
          <Input
            label="每人最多几份订阅"
            hint="生效中的、过期但还能续回的订阅，加上还没付款的新开订单；只在新开一份时检查，续费、叠加、换套餐、流量重置不受影响。0 表示不限"
            inputMode="numeric"
            value={form.max_subscriptions}
            error={errors.max_subscriptions}
            disabled={save.isPending}
            onChange={(e) => {
              setDraft({ ...form, max_subscriptions: e.target.value })
              if (errors.max_subscriptions) setErrors((prev) => ({ ...prev, max_subscriptions: '' }))
            }}
          />
          <p className={css.small}>只管用户在用户端自己下单；后台人工开单、礼品卡兑换不受限。改了立即生效。</p>
        </div>
      )}
    </Modal>
  )
}
