/**
 * [INPUT]: 依赖 react 的 useEffect / useState，依赖 ../../../core/format 的 formatMoney，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / Input / Modal / Select / useToast，依赖 ../../actions 的 useCan / useIntentKey，依赖 ../plans/api 的 usePlans，依赖 ../users/api 的 useUser / useInvalidateUsers，依赖 ./api 的 manualCreatedSchema / useUserPick / useInvalidateBilling，依赖 ./model，依赖 ./failure 的 useBillingFailure，依赖 ./Billing.module.css
 * [OUTPUT]: 对外提供 ManualOrder
 * [POS]: 「人工开单」弹窗（后台-05；POST v1/orders/manual，billing.order.write + reauth + 幂等 order_create，R64 / R74）：用户改成可搜索选择器（GET v1/users?q=，从用户抽屉「为其开单」进来时按 ?new=<用户 id> 预先选好）、套餐与周期取套餐页同一条 GET v1/plans 的在售价格、结算方式（待用户支付 / 线下已收款 + 凭证号 / 赠送；从余额扣除按 D-C-3（已决，5.A.2）不提供）、开单原因必填；成功后关框并打开新订单的抽屉
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useEffect, useState } from 'react'
import { formatMoney } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, Input, Modal, Select, useToast } from '../../../ui'
import { useCan, useIntentKey } from '../../actions'
import { usePlans } from '../plans/api'
import { useInvalidateUsers, useUser } from '../users/api'
import { manualCreatedSchema, useInvalidateBilling, useUserPick } from './api'
import css from './Billing.module.css'
import { useBillingFailure } from './failure'
import { emptyManual, manualBody, manualProblems, priceChoices, SETTLEMENTS, type ManualForm, type Settlement } from './model'

/** onClose(新订单 id)：取消时不带参数 */
export function ManualOrder({ open, userId, onClose }: { open: boolean; userId: string | null; onClose: (createdId?: string) => void }) {
  // 每次打开都是一张新表单（也是一次新的开单意图）
  return open ? <ManualForm userId={userId} onClose={onClose} /> : null
}

function ManualForm({ userId, onClose }: { userId: string | null; onClose: (createdId?: string) => void }) {
  const api = useApi()
  const toast = useToast()
  const can = useCan()
  const fail = useBillingFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateBilling()
  const invalidateUsers = useInvalidateUsers()
  const canUsers = can('iam.user.read')
  const canPlans = can('catalog.read')
  const prefill = useUser(canUsers ? userId : null)
  const plans = usePlans()
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
  const set = <K extends keyof ManualForm>(key: K, value: ManualForm[K]) => {
    setForm((f) => ({ ...f, [key]: value }))
    setErrors({})
  }

  const submit = async () => {
    const problems = manualProblems(form)
    if (Object.keys(problems).length) return setErrors(problems)
    const body = manualBody(form)
    setBusy(true)
    try {
      const r = await api.post('v1/orders/manual', manualCreatedSchema, { body, idempotencyKey: intent.keyFor(body) })
      intent.reset()
      toast(
        form.settlement === 'pending'
          ? `订单 ${r.order_no} 已创建，等待用户在 30 分钟内支付`
          : form.settlement === 'offline'
            ? `订单 ${r.order_no} 已按线下收款入账，订阅已开通`
            : `订单 ${r.order_no} 已赠送开通`,
      )
      onClose(r.order_id)
    } catch (e) {
      fail(e, { fields: setErrors, intent })
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
          <Button size="dialog" variant="primary" busy={busy} onClick={() => void submit()}>
            {form.settlement === 'offline' ? '入账并开通' : form.settlement === 'grant' ? '赠送开通' : '创建订单'}
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
            error={errors.price_id || errors.plan_id}
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
        <p className={css.small}>
          {settlement.hint}
          {chosen && form.settlement !== 'grant' && `；应付 ${formatMoney(chosen.amount, chosen.currency)}`}。「从余额扣除」暂不提供，需要时先到用户详情调账，再用赠送开单。
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
