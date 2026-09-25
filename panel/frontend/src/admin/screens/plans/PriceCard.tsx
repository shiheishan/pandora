/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/format 的 formatDateTime / formatMoney，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / Card / ConfirmModal / Input / Select / useToast，依赖 ../../actions 的 useCan / useIntentKey，依赖 ../users/api 的 useUserGroups，依赖 ./api 的 priceCreatedSchema / rowVersionSchema / useInvalidatePlans / PlanDetail / PriceRow，依赖 ./model 的 PERIOD_OPTIONS / CUSTOM_UNITS / periodLabel / emptyPriceForm / priceProblems / priceBody / PriceForm，依赖 ./failure 的 useCatalogFailure，依赖 ./Plans.module.css
 * [OUTPUT]: 对外提供 PriceCard
 * [POS]: 套餐详情的「价格」卡（后台-04）：在售价在前、已归档淡显在后，行上注明试用、用户组专属与时间窗；归档（ConfirmModal，不可逆）与底部「币种 + 金额 + 周期 + 新增价格」，周期多一个「自定义」，「高级」折叠里是试用天数、用户组专属价与生效时间窗（契约「后端有、设计缺」表）。写接口 catalog.publish + reauth + 幂等，503 走销售开关提示
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { formatDateTime, formatMoney } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, Card, ConfirmModal, Input, Select, useToast } from '../../../ui'
import { useCan, useIntentKey } from '../../actions'
import { useUserGroups } from '../users/api'
import { priceCreatedSchema, rowVersionSchema, useInvalidatePlans, type PlanDetail, type PriceRow } from './api'
import { useCatalogFailure } from './failure'
import { CUSTOM_UNITS, emptyPriceForm, PERIOD_OPTIONS, periodLabel, priceBody, priceProblems, type PriceForm } from './model'
import css from './Plans.module.css'

const CURRENCY_OPTIONS = [
  { value: 'CNY', label: 'CNY' },
  { value: 'USD', label: 'USD' },
]

export function PriceCard({ plan }: { plan: PlanDetail }) {
  const can = useCan()
  const writable = can('catalog.publish') && plan.status !== 'archived'
  const groups = useUserGroups(can('iam.user.read'))
  const groupName = (gid: string) => groups.data?.find((g) => g.id === gid)?.name ?? '用户组专属'
  const [archiving, setArchiving] = useState<PriceRow | null>(null)
  const rows = [...plan.prices.filter((p) => p.status === 'active'), ...plan.prices.filter((p) => p.status === 'archived')]

  return (
    <Card
      flush
      title={
        <>
          价格 <span className={css.cardSub}>归档后不再售卖，已购订阅不受影响</span>
        </>
      }
    >
      {rows.length === 0 && <p className={`${css.small} ${css.cardPad}`}>还没有价格。{writable ? '在下面新增一档，或用向导一次填好。' : ''}</p>}
      {rows.map((p) => {
        const extra = [
          p.trial_days ? `试用 ${p.trial_days} 天` : '',
          p.user_group_id ? groupName(p.user_group_id) : '',
          p.valid_from || p.valid_until ? `${p.valid_from ? formatDateTime(p.valid_from) : '即日'} 至 ${p.valid_until ? formatDateTime(p.valid_until) : '不限'}` : '',
        ].filter(Boolean)
        const off = p.status === 'archived'
        return (
          <div key={p.id} className={`${css.priceRow} ${off ? css.priceRowArchived : ''}`}>
            <span className={css.amount}>{formatMoney(p.unit_amount, p.currency)}</span>
            <span className={css.priceMeta}>
              <span>
                {periodLabel(p.billing_interval, p.interval_count)} · {p.currency}
              </span>
              {extra.length > 0 && <span className={css.small}>{extra.join(' · ')}</span>}
            </span>
            {off ? (
              <span className={css.small}>已归档</span>
            ) : (
              writable && (
                <Button size="xs" variant="ghost" onClick={() => setArchiving(p)}>
                  归档
                </Button>
              )
            )}
          </div>
        )
      })}
      {writable && <AddPrice plan={plan} groups={can('iam.user.read') ? (groups.data ?? []) : null} />}
      <ArchivePrice plan={plan} price={archiving} onClose={() => setArchiving(null)} />
    </Card>
  )
}

function AddPrice({ plan, groups }: { plan: PlanDetail; groups: ReadonlyArray<{ id: string; name: string }> | null }) {
  const api = useApi()
  const toast = useToast()
  const fail = useCatalogFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidatePlans()
  const [form, setForm] = useState<PriceForm>(emptyPriceForm)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const set = <K extends keyof PriceForm>(key: K, value: PriceForm[K]) => {
    setForm((f) => ({ ...f, [key]: value }))
    setErrors({})
  }

  const submit = async () => {
    const problems = priceProblems(form)
    if (Object.keys(problems).length) return setErrors(problems)
    const body = priceBody(form)
    setBusy(true)
    try {
      await api.post(`v1/plans/${encodeURIComponent(plan.id)}/prices`, priceCreatedSchema, { body, idempotencyKey: intent.keyFor([plan.id, body]) })
      intent.reset()
      toast('已新增价格')
      setForm(emptyPriceForm())
      void invalidate()
    } catch (e) {
      fail(e, { fields: setErrors, intent })
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className={css.addPrice}>
      <div className={css.addPriceRow}>
        <Select size="sm" aria-label="币种" options={CURRENCY_OPTIONS} value={form.currency} onChange={(e) => set('currency', e.target.value as PriceForm['currency'])} />
        <Input size="sm" mono aria-label="金额（元）" placeholder="金额" inputMode="decimal" value={form.amount} onChange={(e) => set('amount', e.target.value)} error={errors.unit_amount} />
        <Select
          size="sm"
          aria-label="周期"
          options={[...PERIOD_OPTIONS, { value: 'custom', label: '自定义…' }]}
          value={form.period}
          onChange={(e) => set('period', e.target.value)}
          error={errors.billing_interval}
        />
        <Button size="sm" variant="primary" busy={busy} onClick={() => void submit()}>
          新增价格
        </Button>
      </div>
      {form.period === 'custom' && (
        <div className={css.customPeriod}>
          <Input
            size="sm"
            mono
            aria-label="每几个单位"
            placeholder="数量"
            inputMode="numeric"
            value={form.customCount}
            onChange={(e) => set('customCount', e.target.value)}
            error={errors.interval_count}
          />
          <Select size="sm" aria-label="周期单位" options={CUSTOM_UNITS} value={form.customUnit} onChange={(e) => set('customUnit', e.target.value as PriceForm['customUnit'])} />
        </div>
      )}
      <details className={css.advanced}>
        <summary>高级：试用、用户组专属价、生效时间</summary>
        <div className={css.grid2}>
          <Input size="sm" mono label="试用天数" placeholder="0" inputMode="numeric" value={form.trialDays} onChange={(e) => set('trialDays', e.target.value)} error={errors.trial_days} />
          {groups ? (
            <Select
              size="sm"
              label="只卖给用户组"
              emptyOption="所有人（公开价）"
              options={groups.map((g) => ({ value: g.id, label: g.name }))}
              value={form.groupId}
              onChange={(e) => set('groupId', e.target.value)}
              error={errors.user_group_id}
            />
          ) : (
            <p className={css.small}>没有读取用户组的权限，只能新增公开价。</p>
          )}
          <Input size="sm" type="datetime-local" label="生效时间（选填）" value={form.from} onChange={(e) => set('from', e.target.value)} error={errors.valid_from} />
          <Input size="sm" type="datetime-local" label="失效时间（选填）" value={form.until} onChange={(e) => set('until', e.target.value)} error={errors.valid_until} />
        </div>
      </details>
    </div>
  )
}

/** POST v1/plans/{id}/prices/{priceID}/archive：不可逆（触发器只许 active → archived） */
function ArchivePrice({ plan, price, onClose }: { plan: PlanDetail; price: PriceRow | null; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useCatalogFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidatePlans()
  const label = price ? `${formatMoney(price.unit_amount, price.currency)} ${periodLabel(price.billing_interval, price.interval_count)}` : ''
  const confirm = async () => {
    if (!price) return
    const body = { expected_row_version: price.row_version }
    try {
      await api.post(`v1/plans/${encodeURIComponent(plan.id)}/prices/${encodeURIComponent(price.id)}/archive`, rowVersionSchema, { body, idempotencyKey: intent.keyFor([price.id, body]) })
      intent.reset()
      toast('价格已归档')
      onClose()
    } catch (e) {
      fail(e, { intent })
    } finally {
      void invalidate()
    }
  }
  return (
    <ConfirmModal
      open={price !== null}
      title="归档该价格？"
      body={`${label} 将停止售卖，已购订阅不受影响。归档不可恢复，要再卖需新增一档。`}
      confirmLabel="归档"
      tone="danger"
      onCancel={onClose}
      onConfirm={confirm}
    />
  )
}
