/**
 * [INPUT]: 依赖 react 的 useState，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../core/api 的 isApiError，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / Card / Checkbox / Input / Select，依赖 ./logic 的表单构建，依赖 ./queries、./schemas，依赖 ./marketing.module.css
 * [OUTPUT]: 对外提供 CouponForm（新建单张 / 批量生成共用的内联表单）
 * [POS]: admin/screens/marketing 优惠券标签的内联表单（设计稿 cpForm）：设计只有码 / 数量 / 类型 / 数值 / 每码可用次数，按契约补齐活动名称（批量必填）、每人限用、门槛、封顶、有效期至、适用套餐。单张 POST v1/coupons（reauth），批量 POST v1/coupons/batch（reauth + 幂等 coupon_batch_generate）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { isApiError } from '../../../core/api'
import { useApi } from '../../../shell/runtime'
import { Button, Card, Checkbox, Input, Select } from '../../../ui'
import { COUPON_BATCH_MAX, buildCouponRequest, emptyCouponForm, type CouponForm as Form, type FieldErrors } from './logic'
import css from './marketing.module.css'
import { useFailure, useIntentKey, useInvalidateMarketing } from './queries'
import { couponBatchCreated, couponCreated, type Plan } from './schemas'

export interface BatchResult {
  name: string
  count: number
  codes: string[]
}

export function CouponForm({
  batch,
  plans,
  onCancel,
  onCreated,
}: {
  batch: boolean
  /** undefined = 没有 catalog.read 或目录读取失败，此时不能限定套餐 */
  plans: readonly Plan[] | undefined
  onCancel: () => void
  onCreated: (result: { code: string } | BatchResult) => void
}) {
  const api = useApi()
  const fail = useFailure()
  const invalidate = useInvalidateMarketing()
  const intent = useIntentKey()
  const [form, setForm] = useState<Form>(() => emptyCouponForm(batch))
  const [errors, setErrors] = useState<FieldErrors>({})
  const set = <K extends keyof Form>(key: K, value: Form[K]) => setForm((f) => ({ ...f, [key]: value }))

  const submit = useMutation({
    mutationFn: async (body: Record<string, unknown>) =>
      batch
        ? api.post('v1/coupons/batch', couponBatchCreated, { body, idempotencyKey: intent.keyFor(body) })
        : api.post('v1/coupons', couponCreated, { body }),
    onSuccess: (result) => {
      intent.reset()
      void invalidate()
      onCreated(result)
    },
    onError: (error) => {
      // 409「这个优惠码已经存在」落在码输入框下，而不是一闪而过的 Toast
      if (!batch && isApiError(error, 'conflict')) return setErrors({ code: error.message })
      fail(error, setErrors)
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
  const togglePlan = (id: string, on: boolean) => set('planIds', on ? [...form.planIds, id] : form.planIds.filter((x) => x !== id))

  return (
    <Card title={batch ? '批量生成优惠券' : '新建优惠券'}>
      <form className={css.stack} onSubmit={onSubmit} noValidate>
        <div className={css.formGrid}>
          {batch ? (
            <>
              <Input label="活动名称" value={form.name} onChange={(e) => set('name', e.target.value)} error={errors.name} fieldClassName={css.span2} placeholder="如「KOL 渠道 10 月」，靠它找回这一批" />
              <Input label="码前缀" mono value={form.prefix} onChange={(e) => set('prefix', e.target.value.toUpperCase())} error={errors.prefix} placeholder="可留空，如 VIP" maxLength={8} />
              <Input label={`数量（≤ ${COUPON_BATCH_MAX}）`} mono inputMode="numeric" value={form.count} onChange={(e) => set('count', e.target.value)} error={errors.count} />
            </>
          ) : (
            <>
              <Input label="优惠码" mono value={form.code} onChange={(e) => set('code', e.target.value.toUpperCase())} error={errors.code} placeholder="如 AUTUMN26" />
              <Input label="名称（可选）" value={form.name} onChange={(e) => set('name', e.target.value)} error={errors.name} placeholder="默认同优惠码" />
            </>
          )}
          <Select
            label="类型"
            value={form.kind}
            onChange={(e) => set('kind', e.target.value as Form['kind'])}
            error={errors.discount_type}
            options={[
              { value: 'percent', label: '折扣 %' },
              { value: 'fixed', label: '立减金额' },
            ]}
          />
          <Input
            label={form.kind === 'percent' ? '折扣（%）' : '立减金额'}
            mono
            inputMode="decimal"
            value={form.value}
            onChange={(e) => set('value', e.target.value)}
            error={errors.discount_value}
          />
          {form.kind === 'fixed' ? (
            <Select
              label="币种"
              value={form.currency}
              onChange={(e) => set('currency', e.target.value as Form['currency'])}
              options={[
                { value: 'CNY', label: '人民币 CNY' },
                { value: 'USD', label: '美元 USD' },
              ]}
            />
          ) : (
            <Input label="封顶优惠（¥，可选）" mono inputMode="decimal" value={form.maxDiscount} onChange={(e) => set('maxDiscount', e.target.value)} error={errors.max_discount} placeholder="不封顶" />
          )}
          <Input label="每码可用次数" mono inputMode="numeric" value={form.maxRedemptions} onChange={(e) => set('maxRedemptions', e.target.value)} error={errors.max_redemptions} placeholder="不限" />
          <Input label="每人限用" mono inputMode="numeric" value={form.perUser} onChange={(e) => set('perUser', e.target.value)} error={errors.max_redemptions_per_user} />
          <Input label="门槛金额（可选）" mono inputMode="decimal" value={form.minOrder} onChange={(e) => set('minOrder', e.target.value)} error={errors.min_order_amount} placeholder="无门槛" />
          <Input label="有效期至（可选）" type="datetime-local" value={form.validUntil} onChange={(e) => set('validUntil', e.target.value)} error={errors.valid_until} />
        </div>
        <div>
          <div className={css.groupLabel}>适用套餐（不选即全部套餐）</div>
          {plans === undefined ? (
            <div className={css.faint}>当前账号读不到套餐目录，只能创建适用全部套餐的券。</div>
          ) : (
            <div className={css.checks}>
              {activePlans.map((p) => (
                <Checkbox key={p.id} label={p.name} checked={form.planIds.includes(p.id)} onChange={(e) => togglePlan(p.id, e.target.checked)} />
              ))}
              {activePlans.length === 0 && <span className={css.faint}>还没有在售套餐。</span>}
            </div>
          )}
        </div>
        <div className={css.formActions}>
          <Button onClick={onCancel} disabled={submit.isPending}>
            取消
          </Button>
          <Button type="submit" variant="primary" busy={submit.isPending}>
            {batch ? '生成' : '创建'}
          </Button>
        </div>
      </form>
    </Card>
  )
}
