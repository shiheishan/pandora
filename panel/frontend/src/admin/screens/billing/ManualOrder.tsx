import { useEffect, useState } from 'react'
import { isApiError } from '../../../core/api'
import { useApi } from '../../../shell/runtime'
import { Button, Input, Modal, Select, useToast } from '../../../ui'
import { endsIntent, useCan, useFailure, useIntentKey } from '../../actions'
import { usePlans } from '../plans/api'
import { useInvalidateUsers, useUser } from '../users/api'
import { manualCreatedSchema, useInvalidateBilling, useManualPreview, useProviders, useUserPick } from './api'
import css from './Billing.module.css'
import {
  belowMinimumText,
  emptyManual,
  manualBelowMinimum,
  manualBody,
  manualCreatedToast,
  manualFailureFields,
  manualProblems,
  previewParams,
  priceChoices,
  SETTLEMENTS,
  siteMinPayment,
  type ManualForm,
  type Settlement,
} from './model'
import { choiceOf, previewFailure, priceNote, selectedKey, submitGate } from './placement'
import { PlacementError, PlacementPicker } from './PlacementPicker'

/**
 * onClose(新订单 id)：取消时不带参数。
 * entrySubscriptionId：从用户抽屉某一份订阅的「给这份开单」进来时，那份订阅的 id，只用来预选落点
 */
export function ManualOrder({
  open,
  userId,
  entrySubscriptionId = null,
  onClose,
}: {
  open: boolean
  userId: string | null
  entrySubscriptionId?: string | null
  onClose: (createdId?: string) => void
}) {
  // 每次打开都是一张新表单（也是一次新的开单意图）
  return open ? <ManualForm userId={userId} entrySubscriptionId={entrySubscriptionId} onClose={onClose} /> : null
}

function ManualForm({ userId, entrySubscriptionId, onClose }: { userId: string | null; entrySubscriptionId: string | null; onClose: (createdId?: string) => void }) {
  const api = useApi()
  const toast = useToast()
  const can = useCan()
  const fail = useFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateBilling()
  const invalidateUsers = useInvalidateUsers()
  const canUsers = can('iam.user.read')
  const canPlans = can('catalog.read')
  const prefill = useUser(canUsers ? userId : null)
  const plans = usePlans()
  // 站点最低付款额从渠道列表算（读渠道要 billing.payment.read）；读不到就不在提交前拦，交给服务端的 422
  const providers = useProviders(can('billing.payment.read'))
  const minPay = providers.data ? siteMinPayment(providers.data) : null
  const [form, setForm] = useState<ManualForm>(() => emptyManual())
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)

  // 从用户抽屉进来：详情回来后选中这个人（只在还没选人时）
  const [prefilled, setPrefilled] = useState(false)
  if (!prefilled && prefill.data && form.user === null) {
    setPrefilled(true)
    setForm((f) => ({ ...f, user: { id: prefill.data.id, email: prefill.data.email } }))
  }

  const choices = canPlans ? priceChoices(plans.data ?? []) : []
  const chosen = choices.find((c) => c.value === form.choice)
  // 管理员自己点的落点；换了用户、套餐或价格，选项就变了，点过的作废
  const [picked, setPicked] = useState('')
  const set = <K extends keyof ManualForm>(key: K, value: ManualForm[K]) => {
    setForm((f) => ({ ...f, [key]: value }))
    if (key === 'user' || key === 'choice') setPicked('')
    setErrors({})
  }

  // 入口订阅只对入口那位用户有效：中途换了人就不再预选；服务端说它不对（422 entry_subscription_id）时也不再带
  const [entryRejected, setEntryRejected] = useState<string | null>(null)
  const entry = entryRejected === null && form.user !== null && form.user.id === userId ? entrySubscriptionId : null
  const params = previewParams(form, entry)
  const preview = useManualPreview(params)
  const failure = previewFailure(preview.error ? (isApiError(preview.error) ? preview.error : { status: 0, fields: {}, message: preview.error.message }) : null)
  if (failure.entryRejected !== null && entryRejected === null) setEntryRejected(failure.entryRejected)
  const selected = selectedKey(preview.data, picked)
  const option = preview.data?.options.find((o) => o.key === selected)
  const target = option ? choiceOf(option) : null
  const below = manualBelowMinimum(form.settlement, chosen, option, minPay)
  const gate = submitGate({
    hasInputs: params !== null,
    loading: params !== null && preview.isPending,
    failed: preview.isError,
    failedLabel: failure.fields.price_id ? '先换一个价格' : undefined,
    selected,
    blocked: below ? '低于最低付款额' : null,
  })

  const submit = async () => {
    const problems = manualProblems(form, target)
    if (Object.keys(problems).length) return setErrors(problems)
    const body = manualBody(form, target)
    setBusy(true)
    try {
      const r = await api.post('v1/orders/manual', manualCreatedSchema, { body, idempotencyKey: intent.keyFor(body) })
      intent.reset()
      toast(manualCreatedToast(r, form.settlement, target?.kind))
      onClose(r.order_id)
    } catch (e) {
      // 低于最低付款额的 422 不带 fields：认出来落到结算方式上（下一步就是换结算方式），不只弹 Toast
      const own = isApiError(e) ? manualFailureFields(e) : null
      if (own) {
        if (endsIntent(e)) intent.reset()
        setErrors(own)
      } else fail(e, { fields: setErrors, intent })
      // 订阅状态在预览之后变了（落点已失效）或没选落点：重新取一遍选项，点过的作废
      if (isApiError(e) && (e.status === 409 || e.fields.target !== undefined)) {
        setPicked('')
        void preview.refetch()
      }
    } finally {
      setBusy(false)
      void invalidate()
      void invalidateUsers()
    }
  }

  const settlement = SETTLEMENTS.find((s) => s.value === form.settlement)!
  return (
    <Modal
      open
      size="md"
      onClose={() => onClose()}
      dismissible={!busy}
      title="人工开单"
      actions={
        <>
          <Button size="dialog" onClick={() => onClose()} disabled={busy}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={busy} disabled={!gate.ok} onClick={() => void submit()}>
            {!gate.ok ? gate.label : form.settlement === 'offline' ? '入账并开通' : form.settlement === 'grant' ? '赠送开通' : '创建订单'}
          </Button>
        </>
      }
    >
      <div className={css.dialogBody}>
        <UserPicker
          value={form.user}
          enabled={canUsers}
          loading={canUsers && userId !== null && prefill.isPending}
          onChange={(user) => set('user', user)}
          error={errors.user_id}
        />
        <div className={css.grid2}>
          <Select
            label="套餐与周期"
            placeholder={!canPlans ? '需要套餐读取权限' : plans.isPending ? '读取中…' : choices.length ? '选择在售价格' : '没有在售的价格'}
            options={choices.map(({ value, label }) => ({ value, label }))}
            value={form.choice}
            onChange={(e) => set('choice', e.target.value)}
            error={errors.price_id || errors.plan_id || failure.fields.price_id}
            disabled={!canPlans}
          />
          <Select
            label="结算方式"
            options={SETTLEMENTS.map(({ value, label }) => ({ value, label }))}
            value={form.settlement}
            onChange={(e) => set('settlement', e.target.value as Settlement)}
            error={errors.settlement}
          />
        </div>
        {params !== null && failure.general !== null && <PlacementError message={failure.general} onRetry={() => void preview.refetch()} />}
        {preview.data && chosen && (
          <PlacementPicker
            options={preview.data.options}
            selected={selected}
            targetPlan={chosen.planName}
            settlement={form.settlement}
            entrySubscriptionId={entry}
            onPick={(key) => {
              setPicked(key)
              setErrors({})
            }}
          />
        )}
        {entryRejected !== null && entrySubscriptionId !== null && form.user?.id === userId && (
          <span className={`${css.small} ${css.tone_warn}`}>没能预选「给这份开单」点的那份（{entryRejected}），请自己选这单落到哪一份。</span>
        )}
        {errors.target && (
          <span role="alert" className={`${css.small} ${css.tone_danger}`}>
            {errors.target}
          </span>
        )}
        {below && (
          <div className={css.minWarn} role="alert">
            <span>{belowMinimumText(below)}</span>
            <span className={css.minWarnActions}>
              <Button size="xs" onClick={() => set('settlement', 'grant')}>
                改用赠送
              </Button>
              <Button size="xs" onClick={() => set('settlement', 'offline')}>
                改用线下已收款
              </Button>
            </span>
          </div>
        )}
        <p className={css.small}>
          {settlement.hint}
          {chosen && form.settlement !== 'grant' && `；${priceNote(chosen.amount, chosen.currency, option)}`}。「从余额扣除」暂不提供，需要时先到用户详情调账，再用赠送开单。
        </p>
        {form.settlement === 'offline' && (
          <Input label="凭证号" mono placeholder="银行流水号、收据编号等" value={form.reference} onChange={(e) => set('reference', e.target.value)} error={errors.reference} />
        )}
        <Input label="开单原因（写入审计）" placeholder="至少 5 个字，日后对账的依据" value={form.reason} onChange={(e) => set('reason', e.target.value)} error={errors.reason} />
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// 用户选择器：输入邮箱或用户 ID 搜索（防抖 300ms），点一条选中；选中后显示邮箱与「更换」
// ---------------------------------------------------------------------------
function UserPicker({
  value,
  enabled,
  loading,
  onChange,
  error,
}: {
  value: ManualForm['user']
  enabled: boolean
  loading: boolean
  onChange: (user: ManualForm['user']) => void
  error?: string
}) {
  const [text, setText] = useState('')
  const [term, setTerm] = useState('')
  useEffect(() => {
    const timer = setTimeout(() => setTerm(text), 300)
    return () => clearTimeout(timer)
  }, [text])
  const pick = useUserPick(term, enabled && value === null)
  const found = pick.data ?? []

  if (value)
    return (
      <div className={css.stack}>
        <span className={css.fieldLabel}>用户</span>
        <div className={css.picked}>
          <span className={`${css.ellipsis} ${css.spacer}`}>{value.email}</span>
          <Button size="xs" variant="ghost" onClick={() => onChange(null)}>
            更换
          </Button>
        </div>
      </div>
    )
  return (
    <div className={css.stack}>
      <Input
        label="用户"
        type="search"
        placeholder={enabled ? (loading ? '读取用户中…' : '输入邮箱或用户 ID 搜索') : '需要用户读取权限才能选择用户'}
        value={text}
        onChange={(e) => setText(e.target.value)}
        disabled={!enabled}
        error={error}
        data-autofocus=""
      />
      {enabled && term.trim() !== '' && (
        <ul className={css.pickList} aria-label="匹配的用户">
          {pick.isPending ? (
            <li className={css.pickEmpty}>搜索中…</li>
          ) : pick.isError ? (
            <li className={css.pickEmpty}>搜索失败：{pick.error.message}</li>
          ) : found.length === 0 ? (
            <li className={css.pickEmpty}>没有匹配的用户</li>
          ) : (
            found.map((u) => (
              <li key={u.id}>
                <button type="button" className={css.pickItem} onClick={() => onChange({ id: u.id, email: u.email })}>
                  <span className={css.ellipsis}>{u.email}</span>
                  <span className={`${css.mono} ${css.small}`}>#{u.id.slice(0, 8)}</span>
                </button>
              </li>
            ))
          )}
        </ul>
      )}
    </div>
  )
}
