import { useMutation } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { isApiError } from '../../../core/api'
import { useApi } from '../../../shell/runtime'
import { Button, Input, Modal, Segmented, Switch } from '../../../ui'
import { COUPON_BATCH_MAX, buildCouponRequest, emptyCouponForm, type CouponForm as Form, type FieldErrors } from './logic'
import local from './Coupons.module.css'
import css from './marketing.module.css'
import { useFailure, useIntentKey, useInvalidateMarketing } from './queries'
import { couponBatchCreated, couponCreated, type Plan } from './schemas'

export interface BatchResult {
  name: string
  count: number
  codes: string[]
}

export function CouponFormModal({
  open,
  plans,
  onClose,
  onCreated,
}: {
  open: 'one' | 'batch' | null
  /** undefined = 套餐目录还没取到（加载中或失败），此时只能创建全部套餐的券 */
  plans: readonly Plan[] | undefined
  onClose: () => void
  onCreated: (result: { code: string } | BatchResult) => void
}) {
  const [submitting, setSubmitting] = useState(false)
  return (
    <Modal
      open={open !== null}
      onClose={onClose}
      size={520}
      divided
      dismissible={!submitting}
      title={open === 'batch' ? '批量生成优惠券' : '新建优惠券'}
      subtitle={open === 'batch' ? '一次生成一批随机码，生成后下载 CSV' : undefined}
      actions={
        open !== null ? (
          <>
            <Button size="dialog" onClick={onClose} disabled={submitting}>
              取消
            </Button>
            <Button size="dialog" variant="primary" type="submit" form="coupon-form-modal" busy={submitting}>
              {open === 'batch' ? '生成' : '创建'}
            </Button>
          </>
        ) : undefined
      }
    >
      {open !== null && (
        <CouponFormInner
          key={open}
          open={open}
          plans={plans}
          onClose={onClose}
          onCreated={onCreated}
          onSubmittingChange={setSubmitting}
        />
      )}
    </Modal>
  )
}

function CouponFormInner({
  open,
  plans,
  onClose,
  onCreated,
  onSubmittingChange,
}: {
  open: 'one' | 'batch'
  plans: readonly Plan[] | undefined
  onClose: () => void
  onCreated: (result: { code: string } | BatchResult) => void
  onSubmittingChange: (busy: boolean) => void
}) {
  const batch = open === 'batch'
  const api = useApi()
  const fail = useFailure()
  const invalidate = useInvalidateMarketing()
  const intent = useIntentKey()

  const [form, setForm] = useState<Form>(() => emptyCouponForm(batch))
  const [errors, setErrors] = useState<FieldErrors>({})
  const set = <K extends keyof Form>(key: K, value: Form[K]) => {
    setForm((f) => ({ ...f, [key]: value }))
    setErrors((e) => {
      if (!e[key]) return e
      const copy = { ...e }
      delete copy[key]
      return copy
    })
  }

  const submit = useMutation({
    mutationFn: async (body: Record<string, unknown>) => {
      onSubmittingChange(true)
      return batch
        ? api.post('v1/coupons/batch', couponBatchCreated, { body, idempotencyKey: intent.keyFor(body) })
        : api.post('v1/coupons', couponCreated, { body })
    },
    onSettled: () => {
      onSubmittingChange(false)
    },
    onSuccess: (result) => {
      intent.reset()
      void invalidate()
      onCreated(result)
    },
    onError: (error) => {
      // 409「这个优惠码已经存在」落在码输入框下，而不是一闪而过的 Toast
      if (!batch && isApiError(error, 'conflict')) return setErrors({ code: error.message })
      fail(error, { fields: setErrors, intent })
    },
  })

  const onSubmit = (event: FormEvent) => {
    event.preventDefault()
    const built = buildCouponRequest(form, batch)
    if (!built.ok) return setErrors(built.errors)
    setErrors({})
    submit.mutate(built.body)
  }

  const activePlans = plans?.filter((p) => p.status !== 'archived') ?? []
  const togglePlan = (id: string, on: boolean) =>
    set('planIds', on ? [...form.planIds, id] : form.planIds.filter((x) => x !== id))

  const formId = 'coupon-form-modal'

  return (
    <>
      <form id={formId} className={css.stack} onSubmit={onSubmit} noValidate>
        <div className={local.section}>券码</div>
        {batch ? (
          <>
            <Input
              label="活动名称"
              value={form.name}
              onChange={(e) => set('name', e.target.value)}
              error={errors.name}
              placeholder="如「KOL 渠道 10 月」，靠它找回这一批"
            />
            <div className={css.formGrid}>
              <Input
                label="码前缀"
                mono
                value={form.prefix}
                onChange={(e) => set('prefix', e.target.value.toUpperCase())}
                error={errors.prefix}
                placeholder="可留空，如 VIP"
                maxLength={8}
              />
              <Input
                label="数量（1–500）"
                mono
                inputMode="numeric"
                value={form.count}
                onChange={(e) => set('count', e.target.value)}
                error={errors.count}
                placeholder={`1–${COUPON_BATCH_MAX}`}
              />
            </div>
          </>
        ) : (
          <div className={css.formGrid}>
            <Input
              label="优惠码"
              mono
              value={form.code}
              onChange={(e) => set('code', e.target.value.toUpperCase())}
              error={errors.code}
              placeholder="如 AUTUMN26"
            />
            <Input
              label="名称（可选）"
              value={form.name}
              onChange={(e) => set('name', e.target.value)}
              error={errors.name}
              placeholder="默认同优惠码"
            />
          </div>
        )}

        <div className={local.section}>优惠</div>
        <div className={local.fieldCol}>
          <span className={css.groupLabel}>类型</span>
          <Segmented<'percent' | 'fixed'>
            label="类型"
            size="sm"
            value={form.kind}
            onChange={(val) => set('kind', val)}
            options={[
              { value: 'percent', label: '折扣 %' },
              { value: 'fixed', label: '立减金额' },
            ]}
          />
        </div>

        <div className={css.formGrid}>
          <Input
            label={form.kind === 'percent' ? '折扣（%）' : '立减金额（¥）'}
            mono
            inputMode="decimal"
            value={form.value}
            onChange={(e) => set('value', e.target.value)}
            error={errors.discount_value}
          />
          {form.kind === 'percent' && (
            <Input
              label="封顶优惠（¥，可选）"
              mono
              inputMode="decimal"
              value={form.maxDiscount}
              onChange={(e) => set('maxDiscount', e.target.value)}
              error={errors.max_discount}
              placeholder="不封顶"
            />
          )}
        </div>

        <div className={local.section}>限制</div>
        <div className={css.formGrid}>
          <Input
            label="每码可用次数"
            mono
            inputMode="numeric"
            value={form.maxRedemptions}
            onChange={(e) => set('maxRedemptions', e.target.value)}
            error={errors.max_redemptions}
            placeholder="不限"
          />
          <Input
            label="每人限用"
            mono
            inputMode="numeric"
            value={form.perUser}
            onChange={(e) => set('perUser', e.target.value)}
            error={errors.max_redemptions_per_user}
          />
        </div>
        <Input
          label="门槛金额（可选）"
          mono
          inputMode="decimal"
          value={form.minOrder}
          onChange={(e) => set('minOrder', e.target.value)}
          error={errors.min_order_amount}
          placeholder="无门槛"
        />

        <div className={local.section}>有效期</div>
        <div className={css.formGrid}>
          <Input
            label="开始时间"
            type="datetime-local"
            value={form.validFrom}
            onChange={(e) => set('validFrom', e.target.value)}
            error={errors.valid_from}
            hint="留空为创建后立即生效"
          />
          <Input
            label="结束时间"
            type="datetime-local"
            value={form.validUntil}
            onChange={(e) => set('validUntil', e.target.value)}
            error={errors.valid_until}
            hint="留空为长期有效"
          />
        </div>

        <div className={local.section}>适用套餐（不选即全部套餐）</div>
        {plans === undefined ? (
          <div className={local.faint}>读不到套餐目录，只能创建适用全部套餐的券。</div>
        ) : (
          <div className={local.plansGrid}>
            {activePlans.map((p) => (
              <Switch
                key={p.id}
                label={p.name}
                checked={form.planIds.includes(p.id)}
                onChange={(e) => togglePlan(p.id, e.target.checked)}
              />
            ))}
            {activePlans.length === 0 && <span className={local.faint}>还没有在售套餐。</span>}
          </div>
        )}
      </form>
    </>
  )
}
