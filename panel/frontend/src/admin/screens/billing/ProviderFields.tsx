import { Input } from '../../../ui'
import css from './Billing.module.css'
import type { ProviderForm } from './model'

export function FeeFields({
  form,
  errors,
  set,
}: {
  form: ProviderForm
  errors: Record<string, string>
  set: <K extends keyof ProviderForm>(key: K, value: ProviderForm[K]) => void
}) {
  return (
    <>
      <div className={css.grid2}>
        <Input
          label="固定手续费（元）"
          inputMode="decimal"
          mono
          value={form.fee_fixed}
          onChange={(e) => set('fee_fixed', e.target.value)}
          error={errors.fee_fixed}
        />
        <Input
          label="比例手续费（%）"
          inputMode="decimal"
          mono
          value={form.fee_percent}
          onChange={(e) => set('fee_percent', e.target.value)}
          error={errors.fee_percent}
        />
      </div>
      <p className={css.small}>向用户加收，结账页按渠道显示手续费与合计；用余额付不收。两项都填 0 = 不收。</p>
    </>
  )
}

export function BTCPayNote({ notifyUrl }: { notifyUrl: string }) {
  return (
    <div className={css.stack}>
      <p className={css.small}>
        在 BTCPay 商店「Settings › Webhooks」里新建 Webhook：地址填下面的回调地址，事件勾选 Invoice settled / expired / invalid，密钥填与这里相同的 Webhook
        密钥。只有「已结算」且不是手动标记的发票才自动入账。
      </p>
      {notifyUrl ? <span className={css.mono}>{notifyUrl}</span> : <span className={css.small}>建好后在渠道卡片上能看到回调地址。</span>}
    </div>
  )
}

export function CoinPaymentsNote() {
  return (
    <p className={css.small}>
      回调地址随每张发票自动下发，不用在 CoinPayments 后台配。请在商户后台关闭「接受部分付款」，少付容差设为 0：少付的发票面板不会自动入账。
    </p>
  )
}
