import { useMutation } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { WITHDRAW_METHODS, isUSDTMethod, withdrawMethodLabel } from '../../../core/withdraw-methods'
import { useApi } from '../../../shell/runtime'
import { Button, Card, Checkbox, FilterTabs, Input, Switch, useToast } from '../../../ui'
import { CommissionOverrides } from './CommissionOverrides'
import {
  buildSettingsConfig,
  levelsSum,
  settingsToForm,
  type SettingsForm,
} from './levelsModel'
import local from './CommissionSettings.module.css'
import type { FieldErrors } from './logic'
import css from './marketing.module.css'
import { useFailure, useInvalidateMarketing } from './queries'
import { okResponse, type CommissionOverview } from './schemas'

const SCOPE_ITEMS = [
  { value: 'first_order' as const, label: '仅首单' },
  { value: 'every_order' as const, label: '每笔订单' },
]

export function CommissionSettings({ overview }: { overview: CommissionOverview }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateMarketing()

  const [form, setForm] = useState<SettingsForm>(() => settingsToForm(overview))
  const [errors, setErrors] = useState<FieldErrors>({})

  const save = useMutation({
    mutationFn: (body: object) => api.post('v1/commission/config', okResponse, { body }),
    onSuccess: () => {
      toast('佣金设置已保存')
      void invalidate()
    },
    onError: (error) => fail(error, setErrors),
  })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    const built = buildSettingsConfig(form)
    if (!built.ok) return setErrors(built.errors)
    setErrors({})
    save.mutate(built.body)
  }

  const toggleMethod = (method: string) => {
    setForm((prev) => {
      const exists = prev.methods.includes(method)
      const next = exists ? prev.methods.filter((m) => m !== method) : [...prev.methods, method]
      return { ...prev, methods: next }
    })
  }

  const sum = levelsSum(form)
  const hasUSDT = form.methods.some(isUSDTMethod)

  const cardFooter = (
    <div className={local.sbar}>
      <span className={css.faint}>只影响之后产生的佣金</span>
      <span className={local.spacer} />
      <Button
        type="submit"
        form="commission-settings-form"
        variant="primary"
        size="sm"
        busy={save.isPending}
      >
        保存设置
      </Button>
    </div>
  )

  return (
    <div>
      <form id="commission-settings-form" onSubmit={submit} noValidate>
        <Card title="返佣规则" footer={cardFooter} flush>
          {/* 段 1：返佣规则内容 */}
          <div className={local.cbody}>
            <div className={local.fg}>
              <Input
                label="返佣比例（%）"
                mono
                inputMode="numeric"
                value={form.rate}
                onChange={(e) => setForm({ ...form, rate: e.target.value })}
                error={errors.rate_percent}
              />
              <Input
                label="结算冻结期（天）"
                mono
                inputMode="numeric"
                value={form.freezeDays}
                onChange={(e) => setForm({ ...form, freezeDays: e.target.value })}
                error={errors.freeze_days}
              />
              <div className={local.fl}>
                <span>计佣范围</span>
                <div className={local.scopeTabs}>
                  <FilterTabs
                    label="计佣范围"
                    items={SCOPE_ITEMS}
                    value={form.scope}
                    onChange={(scope) => setForm({ ...form, scope })}
                  />
                </div>
              </div>
            </div>
          </div>

          {/* 段 2：三级分销 */}
          <div className={local.bhBt}>
            <h3 className={local.bhTitle}>三级分销</h3>
            <span className={local.spacer} />
            <Switch
              aria-label="三级分销"
              checked={form.distributionOn}
              onChange={(e) => setForm({ ...form, distributionOn: e.target.checked })}
            />
          </div>
          <div className={form.distributionOn ? local.cbody : local.cbodyOff}>
            <div className={local.fg}>
              <Input
                label="第一级（%）"
                mono
                inputMode="numeric"
                value={form.l1}
                onChange={(e) => setForm({ ...form, l1: e.target.value })}
                error={errors.distribution_l1}
                disabled={!form.distributionOn}
              />
              <Input
                label="第二级（%）"
                mono
                inputMode="numeric"
                value={form.l2}
                onChange={(e) => setForm({ ...form, l2: e.target.value })}
                error={errors.distribution_l2}
                disabled={!form.distributionOn}
              />
              <Input
                label="第三级（%）"
                mono
                inputMode="numeric"
                value={form.l3}
                onChange={(e) => setForm({ ...form, l3: e.target.value })}
                error={errors.distribution_l3}
                disabled={!form.distributionOn}
              />
            </div>
            <small className={local.hint}>合计 {sum}%（不超过 100）</small>
          </div>

          {/* 段 3：开放提现 */}
          <div className={local.bhBt}>
            <h3 className={local.bhTitle}>开放提现</h3>
            <span className={local.bhSub}>关闭后佣金解冻即自动转入余额</span>
            <span className={local.spacer} />
            <Switch
              aria-label="开放提现"
              checked={form.withdrawOn}
              onChange={(e) => setForm({ ...form, withdrawOn: e.target.checked })}
            />
          </div>
          <div className={form.withdrawOn ? local.cbody : local.cbodyOff}>
            <div className={local.fgSingle}>
              <Input
                label="最低提现（¥）"
                mono
                inputMode="decimal"
                value={form.minWithdraw}
                onChange={(e) => setForm({ ...form, minWithdraw: e.target.value })}
                error={errors.min_withdraw}
                disabled={!form.withdrawOn}
              />
            </div>
            <div className={local.methodsWrap}>
              <div className={css.groupLabel}>提现方式</div>
              <div className={local.chks}>
                {WITHDRAW_METHODS.map((method) => (
                  <Checkbox
                    key={method}
                    label={withdrawMethodLabel(method)}
                    checked={form.methods.includes(method)}
                    onChange={() => toggleMethod(method)}
                    disabled={!form.withdrawOn}
                  />
                ))}
              </div>
              {errors.withdraw_methods && <div className={css.error}>{errors.withdraw_methods}</div>}
              {hasUSDT && (
                <div className={local.hint}>
                  USDT 只记录打款，面板不发起链上转账
                </div>
              )}
            </div>
          </div>
        </Card>
      </form>

      {/* 单独佣金设置 */}
      <CommissionOverrides />
    </div>
  )
}
