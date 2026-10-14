import { useMutation } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Card, Input, QueryView, useToast } from '../../../ui'
import { useFailure } from '../../actions'
import { paymentSettingsSchema, useInvalidateBilling, usePaymentSettings } from './api'
import css from './Billing.module.css'

/** 支付回调地址前缀（payment.notify_base_url）：留空用站点地址，生产只收 https */
export function CallbackBaseCard() {
  const settings = usePaymentSettings()
  return (
    <QueryView query={settings} rows={2} empty={null}>
      {(saved) => <CallbackBaseForm saved={saved} />}
    </QueryView>
  )
}

function CallbackBaseForm({ saved }: { saved: { notify_base_url: string; effective_base: string; public_base_url: string } }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateBilling()
  const [value, setValue] = useState(saved.notify_base_url)
  const [errors, setErrors] = useState<Record<string, string>>({})

  useEffect(() => {
    setValue(saved.notify_base_url)
  }, [saved.notify_base_url])

  const dirty = value.trim() !== saved.notify_base_url

  const save = useMutation({
    mutationFn: () => api.put('v1/payment-settings', paymentSettingsSchema, { body: { notify_base_url: value.trim() } }),
    onSuccess: () => {
      toast('回调地址已保存')
      void invalidate()
    },
    onError: (e) => fail(e, setErrors),
  })

  return (
    <Card title="回调地址">
      <div className={css.stack}>
        <p className={css.small}>
          现在生效：<span className={css.mono}>{saved.effective_base}/v1/webhooks/payments/&lt;渠道编码&gt;</span>
        </p>
        <Input
          label="回调地址前缀"
          mono
          placeholder={saved.public_base_url}
          hint={`留空用站点地址 ${saved.public_base_url}。只收 https，不带查询串；单独的域名要自己解析到面板并配好证书。`}
          value={value}
          onChange={(e) => {
            setValue(e.target.value)
            setErrors({})
          }}
          error={errors.notify_base_url}
          disabled={save.isPending}
        />
        <div className={css.toolbar}>
          <span className={css.spacer} />
          <Button size="sm" variant="primary" disabled={!dirty} busy={save.isPending} onClick={() => save.mutate()}>
            保存
          </Button>
        </div>
        <p className={css.small}>
          改了以后新发起的支付用新地址；之前发起、还没回调的支付仍回调到原来的地址，原地址在这段时间要继续能访问（没到账的由主动查单补记）。BTCPay 的 Webhook 地址要到 BTCPay 商店里同步改。
        </p>
      </div>
    </Card>
  )
}
