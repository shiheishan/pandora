/**
 * [INPUT]: 依赖 react 的 useState / FormEvent，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../core/format 的 formatMoney，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./logic、./queries、./schemas，依赖 ./marketing.module.css 与 ./Gifts.module.css
 * [OUTPUT]: 对外提供 TemplateDrawer（新建 / 编辑礼品卡模板）
 * [POS]: admin/screens/marketing 礼品卡的模板编辑抽屉（契约「待补·前端」：设计点一下就建固定占位模板，后端必须有真实奖励才能保存）：类型、名称、说明、奖励（通用 / 套餐 + 价格 / 盲盒奖池）、领取条件、限制、主题色、状态。POST v1/gift-cards（R4：reauth），编辑时卡型锁定（改卡型后端 409）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { formatMoney } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, Checkbox, Drawer, Input, Segmented, Select, TextArea, useToast } from '../../../ui'
import local from './Gifts.module.css'
import { TEMPLATE_TYPES, buildTemplateRequest, emptyPrize, templateToForm, type FieldErrors, type PrizeForm, type TemplateForm } from './logic'
import css from './marketing.module.css'
import { useFailure, useInvalidateMarketing } from './queries'
import { templateSaved, type GiftTemplate, type Plan } from './schemas'

const INTERVAL: Readonly<Record<string, string>> = { day: '天', week: '周', month: '月', year: '年' }

export function TemplateDrawer({
  open,
  template,
  plans,
  onClose,
}: {
  open: boolean
  /** null = 新建 */
  template: GiftTemplate | null
  /** undefined = 读不到套餐目录：套餐卡与「限定套餐」不可用 */
  plans: readonly Plan[] | undefined
  onClose: () => void
}) {
  return (
    <Drawer open={open} onClose={onClose} title={template ? `编辑「${template.name}」` : '新建礼品卡模板'} width={520}>
      {open && <TemplateEditor key={template?.id ?? 'new'} template={template} plans={plans} onClose={onClose} />}
    </Drawer>
  )
}

function TemplateEditor({ template, plans, onClose }: { template: GiftTemplate | null; plans: readonly Plan[] | undefined; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateMarketing()
  const [form, setForm] = useState<TemplateForm>(() => templateToForm(template))
  const [errors, setErrors] = useState<FieldErrors>({})
  const set = <K extends keyof TemplateForm>(key: K, value: TemplateForm[K]) => setForm((f) => ({ ...f, [key]: value }))
  const setPrize = (i: number, patch: Partial<PrizeForm>) => set('pool', form.pool.map((p, j) => (j === i ? { ...p, ...patch } : p)))

  const save = useMutation({
    mutationFn: (body: object) => api.post('v1/gift-cards', templateSaved, { body }),
    onSuccess: ({ template: saved }) => {
      toast(template ? `已保存「${saved.name}」` : `已新建「${saved.name}」`)
      void invalidate()
      onClose()
    },
    onError: (error) => fail(error, setErrors),
  })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    const built = buildTemplateRequest(form)
    if (!built.ok) return setErrors(built.errors)
    setErrors({})
    save.mutate(built.body)
  }

  const livePlans = plans?.filter((p) => p.status === 'active') ?? []
  const prices = livePlans.find((p) => p.id === form.planId)?.prices.filter((p) => p.status === 'active') ?? []

  return (
    <form className={css.stack} onSubmit={submit} noValidate>
      <div>
        <div className={css.groupLabel}>卡型{template && '（已发出的码按原卡型生成，不能修改）'}</div>
        {template ? (
          <div>{TEMPLATE_TYPES.find(([v]) => v === form.type)?.[1]}</div>
        ) : (
          <Segmented<TemplateForm['type']>
            label="卡型"
            size="sm"
            value={form.type}
            onChange={(v) => set('type', v)}
            options={TEMPLATE_TYPES.map(([value, label]) => ({ value, label: value === 'plan' && !plans ? `${label}（需套餐权限）` : label }))}
          />
        )}
      </div>
      <Input label="名称" value={form.name} onChange={(e) => set('name', e.target.value)} error={errors.name} maxLength={120} data-autofocus="" />
      <TextArea label="说明（可选）" rows={2} value={form.description} onChange={(e) => set('description', e.target.value)} />

      <section className={local.section}>
        <div className={css.groupLabel}>奖励</div>
        {form.type === 'general' && (
          <div className={css.formGrid}>
            <Input label="余额（¥）" mono inputMode="decimal" value={form.balance} onChange={(e) => set('balance', e.target.value)} placeholder="不送" />
            <Input label="流量（GB）" mono inputMode="decimal" value={form.trafficGb} onChange={(e) => set('trafficGb', e.target.value)} placeholder="不送" />
            <Input label="延长到期（天）" mono inputMode="numeric" value={form.days} onChange={(e) => set('days', e.target.value)} placeholder="不延长" />
            <Checkbox label="重置本期流量" checked={form.resetQuota} onChange={(e) => set('resetQuota', e.target.checked)} />
          </div>
        )}
        {form.type === 'plan' &&
          (plans ? (
            <div className={css.formGrid}>
              <Select
                label="套餐"
                placeholder="选择套餐"
                value={form.planId}
                onChange={(e) => setForm((f) => ({ ...f, planId: e.target.value, priceId: '' }))}
                options={livePlans.map((p) => ({ value: p.id, label: p.name }))}
              />
              <Select
                label="价格"
                placeholder={form.planId ? '选择价格' : '先选套餐'}
                value={form.priceId}
                disabled={!form.planId}
                onChange={(e) => set('priceId', e.target.value)}
                options={prices.map((p) => ({ value: p.id, label: `${formatMoney(p.unit_amount, p.currency)} / ${p.interval_count > 1 ? p.interval_count : ''}${INTERVAL[p.billing_interval] ?? p.billing_interval}` }))}
              />
            </div>
          ) : (
            <div className={css.faint}>当前账号读不到套餐目录，不能编辑套餐卡的套餐与价格。</div>
          ))}
        {form.type === 'mystery' && (
          <div className={css.stack}>
            {form.pool.map((p, i) => (
              <div key={i} className={local.prize}>
                <Input label={`奖品 ${i + 1}`} value={p.label} onChange={(e) => setPrize(i, { label: e.target.value })} placeholder="中奖后用户看到的名字" />
                <Input label="权重" mono inputMode="numeric" value={p.weight} onChange={(e) => setPrize(i, { weight: e.target.value })} />
                <Input label="余额 ¥" mono inputMode="decimal" value={p.balance} onChange={(e) => setPrize(i, { balance: e.target.value })} />
                <Input label="流量 GB" mono inputMode="decimal" value={p.trafficGb} onChange={(e) => setPrize(i, { trafficGb: e.target.value })} />
                <Input label="天数" mono inputMode="numeric" value={p.days} onChange={(e) => setPrize(i, { days: e.target.value })} />
                <Button size="xs" variant="ghost" className={local.remove} aria-label={`删除奖品 ${i + 1}`} disabled={form.pool.length <= 2} onClick={() => set('pool', form.pool.filter((_, j) => j !== i))}>
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

      <section className={local.section}>
        <div className={css.groupLabel}>领取条件</div>
        <div className={css.checks}>
          <Checkbox label="仅新用户（从未付费）" checked={form.newUserOnly} onChange={(e) => set('newUserOnly', e.target.checked)} />
          <Checkbox label="仅付费用户" checked={form.paidUserOnly} onChange={(e) => set('paidUserOnly', e.target.checked)} />
          <Checkbox label="必须是被邀请注册的" checked={form.requireInvite} onChange={(e) => set('requireInvite', e.target.checked)} />
        </div>
        {plans && livePlans.length > 0 && (
          <>
            <div className={css.groupLabel}>限定持有这些套餐的用户（不选即不限）</div>
            <div className={css.checks}>
              {livePlans.map((p) => (
                <Checkbox
                  key={p.id}
                  label={p.name}
                  checked={form.allowedPlanIds.includes(p.id)}
                  onChange={(e) => set('allowedPlanIds', e.target.checked ? [...form.allowedPlanIds, p.id] : form.allowedPlanIds.filter((x) => x !== p.id))}
                />
              ))}
            </div>
          </>
        )}
        {errors.conditions && <div className={css.error}>{errors.conditions}</div>}
      </section>

      <section className={local.section}>
        <div className={css.formGrid}>
          <Input label="每人最多兑换（次）" mono inputMode="numeric" value={form.maxUsePerUser} onChange={(e) => set('maxUsePerUser', e.target.value)} placeholder="不限" />
          <Input label="两次兑换间隔（小时）" mono inputMode="numeric" value={form.cooldownHours} onChange={(e) => set('cooldownHours', e.target.value)} placeholder="不限" />
          <Input label="主题色" type="color" value={form.themeColor} onChange={(e) => set('themeColor', e.target.value)} className={local.color} />
          <Select
            label="状态"
            value={form.status}
            onChange={(e) => set('status', e.target.value as TemplateForm['status'])}
            error={errors.status}
            options={[
              { value: 'active', label: '可兑换' },
              { value: 'paused', label: '暂停兑换' },
              ...(template ? [{ value: 'archived', label: '归档（从列表移除，不能再生码）' }] : []),
            ]}
          />
        </div>
        {errors.limits && <div className={css.error}>{errors.limits}</div>}
      </section>

      <div className={css.formActions}>
        <Button onClick={onClose} disabled={save.isPending}>
          取消
        </Button>
        <Button type="submit" variant="primary" busy={save.isPending}>
          保存
        </Button>
      </div>
    </form>
  )
}
