import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Card, Input, QueryView, useToast } from '../../../ui'
import { KvList, KvRow } from '../../kit'
import { useFailure } from '../../actions'
import { paymentSettingsSchema, useInvalidateBilling, usePaymentSettings } from './api'
import css from './Billing.module.css'

/** 支付回调地址前缀（payment.notify_base_url）：留空用站点地址，生产只收 https */
export function CallbackBaseCard() {
  const settings = usePaymentSettings()
  // 保存后的值变了就按 key 重建表单，输入框回到新值（不在 effect 里 setState）
  return (
    <QueryView query={settings} rows={2} empty={null}>
      {(saved) => <CallbackBaseForm key={saved.notify_base_url} saved={saved} />}
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
    <Card
      title="回调地址"
      subtitle="支付渠道把结果通知到这里"
      footer={
        <>
          <span className={css.small}>
            改了以后新发起的支付用新地址；之前发起、还没回调的支付仍回调到原来的地址，原地址在这段时间要继续能访问（没到账的由主动查单补记）。BTCPay 的 Webhook 地址要到 BTCPay 商店里同步改。
          </span>
          <Button size="sm" disabled={!dirty} busy={save.isPending} onClick={() => save.mutate()}>
            保存
          </Button>
        </>
      }
    >
      <div className={css.callbackBody}>
        <KvList>
          <KvRow label="现在生效">
            <span className={css.mono}>{saved.effective_base}/v1/webhooks/payments/&lt;渠道编码&gt;</span>
          </KvRow>
        </KvList>
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
      </div>
    </Card>
  )
}
