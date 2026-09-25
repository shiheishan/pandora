/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / Input / Modal / Select / Switch / TextArea / useToast，依赖 ../../actions 的 useCan / useIntentKey，依赖 ./api 的 usePlanPools / usePoolOptions / planCreatedSchema / planUpdatedSchema / useInvalidatePlans / PlanDetail，依赖 ./model 的向导表单、校验与提交体，依赖 ./failure 的 useCatalogFailure，依赖 ./PoolCard 的 PoolChips，依赖 ./SalesDrawer 的 GroupPicker / VISIBILITY_OPTIONS，依赖 ./Plans.module.css
 * [OUTPUT]: 对外提供 Wizard
 * [POS]: 套餐向导（后台-04「新建套餐」「用向导编辑」，720 宽弹窗，左步骤栏右表单）：基本资料（+ 可见范围与排序）、用量与设备（新建时 + 流量重置）、销售价格（每档币种 / 周期 / 试用）、可用线路、确认（+ 购买限制；新建时「保存后立即发布上架」）。新建 POST v1/plans/complete，编辑 PUT v1/plans/{id}/complete（没改的额度、价格、线路发 null）；都是 catalog.publish + reauth + 幂等（5.A D-C-2）。每步「下一步」只校验本步，提交时前端或后端的 fields 跳到出错的那一步。D-C-5 未决前向导不放限速（后端必定 422）；编辑向导的三处后端限制（设备数改不回不限、新版本的高级设置回到默认、会清掉上架时间窗）如实提示
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Input, Modal, Select, Switch, TextArea, useToast } from '../../../ui'
import { useCan, useIntentKey } from '../../actions'
import { planCreatedSchema, planUpdatedSchema, useInvalidatePlans, usePlanPools, usePoolOptions, type PlanDetail } from './api'
import { useCatalogFailure } from './failure'
import {
  createBody,
  emptyWizard,
  newPriceRow,
  PERIOD_OPTIONS,
  periodLabel,
  parsePeriod,
  RESET_LABELS,
  removedCurrencies,
  stepOfField,
  updateBody,
  VISIBILITY_LABELS,
  WIZARD_STEPS,
  wizardFromPlan,
  wizardProblems,
  type WizardForm,
  type WizardPrice,
} from './model'
import css from './Plans.module.css'
import { PoolChips } from './PoolCard'
import { GroupPicker, VISIBILITY_OPTIONS } from './SalesDrawer'

const RESET_OPTIONS = Object.entries(RESET_LABELS).map(([value, label]) => ({ value, label }))
const CURRENCY_OPTIONS = [
  { value: 'CNY', label: 'CNY' },
  { value: 'USD', label: 'USD' },
]

export function Wizard({ plan, onClose, onSaved }: { plan: PlanDetail | null; onClose: () => void; onSaved: (planId: string) => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useCatalogFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidatePlans()
  const mode = plan ? 'edit' : 'new'
  const [original] = useState<WizardForm>(() => (plan ? wizardFromPlan(plan) : emptyWizard()))
  const [form, setForm] = useState<WizardForm>(original)
  const [step, setStep] = useState(1)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)

  const set = <K extends keyof WizardForm>(key: K, value: WizardForm[K]) => {
    setForm((f) => ({ ...f, [key]: value }))
    setErrors({})
  }
  const problems = () => wizardProblems(form, mode, original)
  const badSteps = new Set(Object.keys(errors).map(stepOfField))
  // 从步骤栏直接跳到后面时，跳过的步不算完成：只有本步字段都过了预检才打 ✓
  const pending = new Set(Object.keys(problems()).map(stepOfField))
  const done = (n: number) => n < step && !pending.has(n)

  /** 把 fields 标上并跳到最早出错的一步；认不出步的 fields 用 Toast 说 */
  const mark = (fields: Record<string, string>) => {
    setErrors(fields)
    const steps = Object.keys(fields)
      .map(stepOfField)
      .filter((s): s is number => s !== null)
    if (steps.length) setStep(Math.min(...steps))
    else toast(Object.values(fields).join('；'), 'danger')
  }

  const next = () => {
    const mine = Object.fromEntries(Object.entries(problems()).filter(([k]) => stepOfField(k) === step))
    if (Object.keys(mine).length) return setErrors(mine)
    if (step < WIZARD_STEPS.length) return setStep(step + 1)
    void submit()
  }

  const submit = async () => {
    const all = problems()
    if (Object.keys(all).length) return mark(all)
    setBusy(true)
    try {
      if (!plan) {
        const body = createBody(form)
        const r = await api.post('v1/plans/complete', planCreatedSchema, { body, idempotencyKey: intent.keyFor(body) })
        intent.reset()
        toast(r.published ? '套餐已创建并上架' : '已保存为草稿，发布后门户可见')
        onSaved(r.plan.id)
      } else {
        const body = updateBody(form, original, plan.row_version)
        const r = await api.put(`v1/plans/${encodeURIComponent(plan.id)}/complete`, planUpdatedSchema, { body, idempotencyKey: intent.keyFor([plan.id, body]) })
        intent.reset()
        toast(r.changed.join('；') || '套餐已保存')
        onSaved(plan.id)
      }
    } catch (e) {
      fail(e, { fields: mark, intent })
    } finally {
      setBusy(false)
      void invalidate()
    }
  }

  const last = step === WIZARD_STEPS.length
  return (
    <Modal
      open
      size="lg"
      onClose={onClose}
      dismissible={!busy}
      title={plan ? `编辑「${plan.name}」` : '新建套餐'}
      actions={
        <>
          <Button size="dialog" onClick={onClose} disabled={busy}>
            取消
          </Button>
          {step > 1 && (
            <Button size="dialog" onClick={() => setStep(step - 1)} disabled={busy}>
              上一步
            </Button>
          )}
          <Button size="dialog" variant="primary" busy={busy} onClick={next}>
            {!last ? '下一步' : plan ? '保存' : form.publish ? '保存并发布' : '保存草稿'}
          </Button>
        </>
      }
    >
      <div className={css.wizard}>
        <nav className={css.steps} aria-label="向导步骤">
          {WIZARD_STEPS.map((t, i) => {
            const n = i + 1
            return (
              <button
                key={t}
                type="button"
                className={`${css.step} ${done(n) ? css.stepDone : ''} ${badSteps.has(n) ? css.stepBad : ''}`}
                aria-current={n === step ? 'step' : undefined}
                onClick={() => setStep(n)}
              >
                <span className={css.stepMark} aria-hidden="true">
                  {badSteps.has(n) ? '!' : done(n) ? '✓' : n}
                </span>
                {t}
              </button>
            )
          })}
        </nav>
        <div className={css.stack}>
          <h3 className={css.stepTitle}>{WIZARD_STEPS[step - 1]}</h3>
          {step === 1 && <StepBasics form={form} set={set} errors={errors} />}
          {step === 2 && <StepQuota form={form} set={set} errors={errors} mode={mode} />}
          {step === 3 && <StepPrices form={form} set={set} errors={errors} original={plan ? original : null} />}
          {step === 4 && <StepPools form={form} set={set} errors={errors} plan={plan} />}
          {step === 5 && <StepConfirm form={form} set={set} errors={errors} plan={plan} />}
        </div>
      </div>
    </Modal>
  )
}

type StepProps = {
  form: WizardForm
  set: <K extends keyof WizardForm>(key: K, value: WizardForm[K]) => void
  errors: Record<string, string>
}

function StepBasics({ form, set, errors }: StepProps) {
  return (
    <>
      <div className={css.grid2}>
        <Input label="套餐名称" value={form.name} onChange={(e) => set('name', e.target.value)} error={errors.name} data-autofocus />
        <Input
          label="套餐代码"
          mono
          placeholder="pro-monthly"
          value={form.code}
          onChange={(e) => set('code', e.target.value)}
          error={errors.code}
          hint="小写字母、数字、- 与 _，门户与订单里用它识别套餐"
        />
      </div>
      <TextArea label="套餐说明" rows={3} value={form.description} onChange={(e) => set('description', e.target.value)} error={errors.description} />
      <details className={css.advanced} open={form.visibility !== 'public' || Boolean(errors.visibility || errors.visible_group_ids || errors.sort_order)}>
        <summary>可见范围与排序</summary>
        <div className={css.stack}>
          <div className={css.grid2}>
            <Select label="可见范围" options={VISIBILITY_OPTIONS} value={form.visibility} onChange={(e) => set('visibility', e.target.value as WizardForm['visibility'])} error={errors.visibility} />
            <Input label="排序（越小越靠前）" mono inputMode="numeric" value={form.sortOrder} onChange={(e) => set('sortOrder', e.target.value)} error={errors.sort_order} />
          </div>
          {form.visibility === 'group' && <GroupPicker picked={form.groupIds} onChange={(ids) => set('groupIds', ids)} error={errors.visible_group_ids} />}
        </div>
      </details>
    </>
  )
}

function StepQuota({ form, set, errors, mode }: StepProps & { mode: 'new' | 'edit' }) {
  return (
    <>
      <div className={css.grid2}>
        <Input label="每周期流量（GB）" mono placeholder="留空为不限量" inputMode="numeric" value={form.gb} onChange={(e) => set('gb', e.target.value)} error={errors.traffic_gb} />
        <Input label="同时在线设备上限" mono placeholder="留空为不限" inputMode="numeric" value={form.devices} onChange={(e) => set('devices', e.target.value)} error={errors.max_devices} />
      </div>
      {mode === 'new' ? (
        <div className={css.grid2}>
          <Select label="流量重置" options={RESET_OPTIONS} value={form.strategy} onChange={(e) => set('strategy', e.target.value as WizardForm['strategy'])} error={errors.quota_reset_strategy} />
          {form.strategy === 'fixed_day' && (
            <Input label="每月几号（1–28）" mono inputMode="numeric" value={form.resetDay} onChange={(e) => set('resetDay', e.target.value)} error={errors.quota_reset_day} />
          )}
        </div>
      ) : (
        <p className={css.note}>
          额度变了会开一个新版本并立即发布：新购按新额度，已买的用户仍按原额度。新版本的宽限期、权益等高级设置会回到默认值，要保留它们请改用详情里的「新建版本」。流量重置方式在版本的「高级」里改。
        </p>
      )}
      <p className={css.small}>限速在版本里设置（需要选「用完限速」策略）。</p>
    </>
  )
}

function StepPrices({ form, set, errors, original }: StepProps & { original: WizardForm | null }) {
  const update = (i: number, patch: Partial<WizardPrice>) =>
    set(
      'prices',
      form.prices.map((p, j) => (j === i ? { ...p, ...patch } : p)),
    )
  const gone = original ? removedCurrencies(form, original) : []
  return (
    <>
      {form.prices.map((p, i) => {
        // 编辑时回填的价格可能不在五档预设里：把它自己的周期也放进下拉
        const own = PERIOD_OPTIONS.some((o) => o.value === p.period) ? [] : [{ value: p.period, label: periodLabel(parsePeriod(p.period).billing_interval, parsePeriod(p.period).interval_count) }]
        return (
          <div key={p.key} className={css.wizardPrice}>
            <Input
              mono
              aria-label={`第 ${i + 1} 档价格（元）`}
              placeholder="价格（元）"
              inputMode="decimal"
              value={p.amount}
              onChange={(e) => update(i, { amount: e.target.value })}
              error={errors[`prices.${i}.unit_amount`]}
            />
            <Select
              aria-label={`第 ${i + 1} 档币种`}
              options={CURRENCY_OPTIONS}
              value={p.currency}
              onChange={(e) => update(i, { currency: e.target.value as WizardPrice['currency'] })}
              error={errors[`prices.${i}.currency`]}
            />
            <Select
              aria-label={`第 ${i + 1} 档周期`}
              options={[...PERIOD_OPTIONS, ...own]}
              value={p.period}
              onChange={(e) => update(i, { period: e.target.value })}
              error={errors[`prices.${i}.billing_interval`] ?? errors[`prices.${i}.interval_count`]}
            />
            <Input
              mono
              aria-label={`第 ${i + 1} 档试用天数`}
              placeholder="试用天数"
              inputMode="numeric"
              value={p.trialDays}
              onChange={(e) => update(i, { trialDays: e.target.value })}
              error={errors[`prices.${i}.trial_days`]}
            />
            <Button
              variant="ghost"
              onClick={() =>
                set(
                  'prices',
                  form.prices.filter((_, j) => j !== i),
                )
              }
            >
              移除
            </Button>
          </div>
        )
      })}
      <Button size="sm" variant="outline" className={css.alignStart} onClick={() => set('prices', [...form.prices, newPriceRow(form.prices.length ? 'month:3' : 'month:1')])}>
        ＋ 添加周期
      </Button>
      {errors.prices && (
        <p className={css.fieldError} role="alert">
          {errors.prices}
        </p>
      )}
      {original && <p className={css.small}>这里只管在售的公开价；用户组专属价与带时间窗的价格在详情「价格」卡里管，保存不会动它们。</p>}
      {gone.length > 0 && <p className={css.note}>{gone.join('、')} 的价格全部移除后不会被归档（只同步清单里出现的币种），要停售请到详情「价格」卡逐条归档。</p>}
    </>
  )
}

function StepPools({ form, set, errors, plan }: StepProps & { plan: PlanDetail | null }) {
  const can = useCan()
  // 新建时没有套餐 id，候选取节点池列表（node.read）；编辑时取本套餐的绑定候选（catalog.read）
  const options = usePoolOptions(!plan && can('node.read'))
  const planPools = usePlanPools(plan?.id ?? null)
  const pools = plan ? planPools.data?.pools : options.data
  const loading = plan ? planPools.isPending : can('node.read') && options.isPending
  return (
    <>
      {!plan && !can('node.read') ? (
        <p className={css.note}>没有读取节点池的权限（node.read），这里列不出线路。可以关掉第 5 步的「立即发布」先存草稿，建好后在详情「线路 · 节点池」里绑定。</p>
      ) : loading ? (
        <p className={css.small}>正在读取节点池…</p>
      ) : !pools?.length ? (
        <p className={css.small}>还没有可用的节点池，先到「节点与服务器 · 节点池」建一个。</p>
      ) : (
        <PoolChips inline pools={pools} picked={form.poolIds} onChange={(ids) => set('poolIds', ids)} />
      )}
      {errors.pool_ids && (
        <p className={css.fieldError} role="alert">
          {errors.pool_ids}
        </p>
      )}
      {plan && <p className={css.small}>线路变了会开一个新版本并立即发布：新购立即用新线路，已买的用户续费时切换。</p>}
    </>
  )
}

function StepConfirm({ form, set, errors, plan }: StepProps & { plan: PlanDetail | null }) {
  const planPools = usePlanPools(plan?.id ?? null)
  const options = usePoolOptions(false)
  const poolNames = (plan ? planPools.data?.pools : options.data)?.filter((p) => form.poolIds.includes(p.id)).map((p) => p.name) ?? []
  const prices = form.prices
    .filter((p) => p.amount.trim())
    .map((p) => `${p.currency === 'USD' ? '$' : '¥'}${p.amount} ${periodLabel(parsePeriod(p.period).billing_interval, parsePeriod(p.period).interval_count)}`)
  const rows: Array<[string, string]> = [
    ['名称', `${form.name || '—'} · ${form.code || '—'}`],
    ['流量', form.gb.trim() ? `${form.gb} GB / 周期` : '不限量'],
    ['设备', form.devices.trim() || '不限'],
    ['价格', prices.join('，') || '—'],
    ['线路', poolNames.join('、') || (form.poolIds.length ? `${form.poolIds.length} 个节点池` : '—')],
    ['可见范围', VISIBILITY_LABELS[form.visibility]],
  ]
  return (
    <>
      <dl className={css.summary}>
        {rows.map(([k, v]) => (
          <div key={k} className={css.summaryRow}>
            <dt>{k}</dt>
            <dd>{v}</dd>
          </div>
        ))}
      </dl>
      <details className={css.advanced} open={Boolean(errors.purchase_limit_per_user || errors.stock_total)}>
        <summary>购买限制</summary>
        <div className={css.stack}>
          <div className={css.toggles}>
            <Switch label="允许新购" checked={form.allowNew} onChange={(e) => set('allowNew', e.target.checked)} />
            <Switch label="允许续费" checked={form.allowRenewal} onChange={(e) => set('allowRenewal', e.target.checked)} />
            <Switch label="允许变更到这个套餐" checked={form.allowUpgrade} onChange={(e) => set('allowUpgrade', e.target.checked)} />
          </div>
          <div className={css.grid2}>
            <Input
              label="每人限购"
              mono
              placeholder="不限"
              inputMode="numeric"
              value={form.purchaseLimit}
              onChange={(e) => set('purchaseLimit', e.target.value)}
              error={errors.purchase_limit_per_user}
            />
            <Input label="库存总量" mono placeholder="不限" inputMode="numeric" value={form.stockTotal} onChange={(e) => set('stockTotal', e.target.value)} error={errors.stock_total} />
          </div>
        </div>
      </details>
      {plan ? (
        (plan.visible_from || plan.visible_until) && <p className={css.note}>保存会清掉这个套餐的上架时间窗（编辑向导不带这两个字段），保存后到「销售设置」重新填。</p>
      ) : (
        <Switch label="保存后立即发布上架" checked={form.publish} onChange={(e) => set('publish', e.target.checked)} />
      )}
    </>
  )
}
