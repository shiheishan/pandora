import { useMutation } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { WITHDRAW_METHODS, isUSDTMethod, withdrawMethodLabel } from '../../../core/withdraw-methods'
import { useApi } from '../../../shell/runtime'
import { Button, Checkbox, Input, Switch, useToast } from '../../../ui'
import { buildLevelsConfig, levelsSum, levelsToForm, type LevelsForm } from './levelsModel'
import local from './Commission.module.css'
import type { FieldErrors } from './logic'
import css from './marketing.module.css'
import { useFailure, useInvalidateMarketing } from './queries'
import { okResponse, type CommissionOverview } from './schemas'

export function CommissionLevels({ overview }: { overview: CommissionOverview }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateMarketing()
  const [form, setForm] = useState<LevelsForm>(() => levelsToForm(overview))
  const [errors, setErrors] = useState<FieldErrors>({})

  const save = useMutation({
    mutationFn: (body: object) => api.post('v1/commission/config', okResponse, { body }),
    onSuccess: () => {
      toast('分销与提现设置已保存')
      void invalidate()
    },
    onError: (error) => fail(error, setErrors),
  })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    const built = buildLevelsConfig(form)
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

  return (
    <form className={local.form} onSubmit={submit} noValidate>
      <div>
        <Switch
          label="三级分销"
          checked={form.distributionOn}
          onChange={(e) => setForm({ ...form, distributionOn: e.target.checked })}
        />
        <div className={css.faint} style={{ marginTop: '4px' }}>
          关闭时直接邀请人拿全部佣金；开启后这一单的佣金按下面的比例分给往上三级邀请人，缺的级不分
        </div>
      </div>

      {form.distributionOn && (
        <div className={css.stack}>
          <div className={local.pair}>
            <Input
              label="第一级（%）"
              mono
              inputMode="numeric"
              value={form.l1}
              onChange={(e) => setForm({ ...form, l1: e.target.value })}
              error={errors.distribution_l1}
            />
            <Input
              label="第二级（%）"
              mono
              inputMode="numeric"
              value={form.l2}
              onChange={(e) => setForm({ ...form, l2: e.target.value })}
              error={errors.distribution_l2}
            />
          </div>
          <div>
            <Input
              label="第三级（%）"
              mono
              inputMode="numeric"
              value={form.l3}
              onChange={(e) => setForm({ ...form, l3: e.target.value })}
              error={errors.distribution_l3}
            />
          </div>
          <div className={css.faint}>合计 {sum}%（不超过 100）</div>
        </div>
      )}

      <div>
        <Switch
          label="开放提现"
          checked={form.withdrawOn}
          onChange={(e) => setForm({ ...form, withdrawOn: e.target.checked })}
        />
        {!form.withdrawOn && (
          <div className={css.faint} style={{ marginTop: '4px' }}>
            只关站点币种：站点币种的佣金解冻后自动转入余额（关闭前已可提现的部分由用户自己转）；其他币种的佣金仍按下面的方式提现
          </div>
        )}
      </div>

      {/* 关着提现时这里仍管其他币种的提现方式 */}
      <div>
        <div className={css.groupLabel}>{form.withdrawOn ? '提现方式' : '提现方式（其他币种）'}</div>
        <div className={css.checks}>
          {WITHDRAW_METHODS.map((method) => (
            <Checkbox
              key={method}
              label={withdrawMethodLabel(method)}
              checked={form.methods.includes(method)}
              onChange={() => toggleMethod(method)}
            />
          ))}
        </div>
        {errors.withdraw_methods && <div className={css.error} style={{ marginTop: '4px' }}>{errors.withdraw_methods}</div>}
        {hasUSDT && (
          <div className={css.faint} style={{ marginTop: '6px' }}>
            USDT 只记录打款，面板不发起链上转账；请在钱包里转账后回来填交易哈希
          </div>
        )}
      </div>

      <Button type="submit" variant="primary" block busy={save.isPending}>
        保存分销与提现设置
      </Button>
    </form>
  )
}
