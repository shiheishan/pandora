import { useMutation } from '@tanstack/react-query'
import { useEffect, useState, type FormEvent } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Input, Modal, Select, Switch, useToast } from '../../../ui'
import { TEMPLATE_TYPES, buildTemplateRequest, emptyPrize, emptyTemplateForm, priceLabel, type PrizeForm, type TemplateForm } from './giftModel'
import local from './Gifts.module.css'
import type { FieldErrors } from './logic'
import css from './marketing.module.css'
import { useFailure, useInvalidateMarketing } from './queries'
import { templateSaved, type Plan } from './schemas'

export function TemplateModal({
  open,
  plans,
  onClose,
}: {
  open: boolean
  plans: readonly Plan[] | undefined
  onClose: () => void
}) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateMarketing()
  const [form, setForm] = useState<TemplateForm>(emptyTemplateForm)
  const [errors, setErrors] = useState<FieldErrors>({})

  useEffect(() => {
    if (open) {
      setForm(emptyTemplateForm())
      setErrors({})
    }
  }, [open])

  const set = <K extends keyof TemplateForm>(key: K, value: TemplateForm[K]) => setForm((f) => ({ ...f, [key]: value }))
  const setPrize = (i: number, patch: Partial<PrizeForm>) => set('pool', form.pool.map((p, j) => (j === i ? { ...p, ...patch } : p)))

  const save = useMutation({
    mutationFn: (body: object) => api.post('v1/gift-cards', templateSaved, { body }),
    onSuccess: () => {
      toast('模板已创建，可以生成卡码了')
      void invalidate()
      onClose()
    },
    onError: (error) => fail(error, setErrors),
  })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    const built = buildTemplateRequest(form, plans)
    if (!built.ok) return setErrors(built.errors)
    setErrors({})
    save.mutate(built.body)
  }

  const livePlans = plans?.filter((p) => p.status === 'active') ?? []
  const selectedPlan = livePlans.find((p) => p.id === form.planId)
  const activePrices = selectedPlan?.prices.filter((p) => p.status === 'active') ?? []
  const singleOneTimePrice = selectedPlan?.kind === 'one_time' ? activePrices[0] : undefined

  return (
    <Modal
      open={open}
      onClose={onClose}
      size={520}
      divided
      dismissible={!save.isPending}
      title="新建礼品卡模板"
      actions={
        <>
          <Button size="dialog" onClick={onClose} disabled={save.isPending}>
            取消
          </Button>
          <Button size="dialog" variant="primary" type="submit" form="new-template-form" busy={save.isPending}>
            创建模板
          </Button>
        </>
      }
    >
      <form id="new-template-form" className={css.stack} onSubmit={submit} noValidate>
        {/* 基本 */}
        <section className={local.formSection}>
          <div className={local.secTitle}>基本</div>
          <Input label="名称" value={form.name} onChange={(e) => set('name', e.target.value)} error={errors.name} maxLength={120} placeholder="如「50 元余额卡」" data-autofocus="" />
          <div className={local.fieldGroup}>
            <span className={local.fieldLabel}>类型</span>
            <div className={local.radioGroup} role="radiogroup" aria-label="类型">
              {TEMPLATE_TYPES.map(([val, label]) => (
                <button
                  key={val}
                  type="button"
                  role="radio"
                  aria-checked={val === form.type}
                  className={local.radioOption}
                  onClick={() => {
                    set('type', val)
                    setErrors((errs) => {
                      const next = { ...errs }
                      delete next.rewards
                      return next
                    })
                  }}
                >
                  <span className={val === form.type ? `${local.radioBox} ${local.radioBoxOn}` : local.radioBox} />
                  <span>{label}</span>
                </button>
              ))}
            </div>
          </div>
        </section>

        {/* 兑换后获得 */}
        <section className={local.formSection}>
          <div className={local.secTitle}>兑换后获得</div>
          {form.type === 'general' && (
            <div className={css.stack}>
              <div className={local.formGrid4}>
                <Input label="余额（元）" mono inputMode="decimal" value={form.balance} onChange={(e) => set('balance', e.target.value)} placeholder="0" />
                <Input label="流量（GB）" mono inputMode="decimal" value={form.trafficGb} onChange={(e) => set('trafficGb', e.target.value)} placeholder="0" />
                <Input label="延长到期（天）" mono inputMode="numeric" value={form.days} onChange={(e) => set('days', e.target.value)} placeholder="0" />
              </div>
              <Switch label="重置本期流量" checked={form.resetQuota} onChange={(e) => set('resetQuota', e.target.checked)} />
            </div>
          )}

          {form.type === 'plan' && (
            <div className={css.stack}>
              {!plans ? (
                <div className={css.faint}>读不到套餐目录，暂时不能建套餐卡</div>
              ) : (
                <>
                  <Select
                    label="套餐"
                    placeholder="选择套餐"
                    value={form.planId}
                    onChange={(e) => {
                      const newPlanId = e.target.value
                      const p = livePlans.find((x) => x.id === newPlanId)
                      const firstActivePriceId = p?.kind === 'one_time' ? (p.prices.find((pr) => pr.status === 'active')?.id ?? '') : ''
                      setForm((f) => ({ ...f, planId: newPlanId, priceId: firstActivePriceId }))
                    }}
                    options={livePlans.map((p) => ({ value: p.id, label: p.name }))}
                  />
                  {selectedPlan?.kind === 'subscription' && (
                    <Select
                      label="周期价"
                      placeholder="选择周期价"
                      value={form.priceId}
                      onChange={(e) => set('priceId', e.target.value)}
                      options={activePrices.map((p) => ({ value: p.id, label: priceLabel(p) }))}
                    />
                  )}
                  {selectedPlan?.kind === 'one_time' && (
                    <div className={css.faint}>
                      {singleOneTimePrice ? `一次性套餐只有一个价：${priceLabel(singleOneTimePrice)}` : '这个一次性套餐没有在售价格'}
                    </div>
                  )}
                </>
              )}
            </div>
          )}

          {form.type === 'mystery' && (
            <div className={css.stack}>
              <div className={css.faint}>至少 2 个、最多 50 个奖品，兑换时按权重抽一个</div>
              {form.pool.map((p, i) => (
                <div key={i} className={local.prizeRow}>
                  <Input label={`奖品 ${i + 1}`} value={p.label} onChange={(e) => setPrize(i, { label: e.target.value })} placeholder="奖品名称" />
                  <Input label="权重" mono inputMode="numeric" value={p.weight} onChange={(e) => setPrize(i, { weight: e.target.value })} />
                  <Input label="余额 ¥" mono inputMode="decimal" value={p.balance} onChange={(e) => setPrize(i, { balance: e.target.value })} />
                  <Input label="流量 GB" mono inputMode="decimal" value={p.trafficGb} onChange={(e) => setPrize(i, { trafficGb: e.target.value })} />
                  <Input label="天数" mono inputMode="numeric" value={p.days} onChange={(e) => setPrize(i, { days: e.target.value })} />
                  <Button
                    size="xs"
                    variant="ghost"
                    className={local.removeBtn}
                    aria-label={`删除奖品 ${i + 1}`}
                    disabled={form.pool.length <= 2}
                    onClick={() => set('pool', form.pool.filter((_, j) => j !== i))}
                  >
                    删除
                  </Button>
                </div>
              ))}
              <div>
                <Button size="sm" disabled={form.pool.length >= 50} onClick={() => set('pool', [...form.pool, emptyPrize()])}>
                  ＋ 添加奖品
                </Button>
              </div>
            </div>
          )}
          {errors.rewards && <div className={css.error}>{errors.rewards}</div>}
        </section>

        {/* 兑换条件 */}
        <section className={local.formSection}>
          <div className={local.secTitle}>兑换条件</div>
          <div className={local.condSwitches}>
            <Switch label="仅新用户" checked={form.newUserOnly} onChange={(e) => set('newUserOnly', e.target.checked)} />
            <Switch label="仅付费用户" checked={form.paidUserOnly} onChange={(e) => set('paidUserOnly', e.target.checked)} />
          </div>
          {errors.conditions && <div className={css.error}>{errors.conditions}</div>}
          <Input label="每人限兑次数" mono inputMode="numeric" value={form.maxUsePerUser} onChange={(e) => set('maxUsePerUser', e.target.value)} placeholder="不限" error={errors.limits} />
        </section>
      </form>
    </Modal>
  )
}
