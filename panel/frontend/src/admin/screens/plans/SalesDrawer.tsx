/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / Checkbox / Drawer / Input / Select / Switch / useToast，依赖 ../../actions 的 useCan / useIntentKey，依赖 ../users/api 的 useUserGroups，依赖 ./api 的 rowVersionSchema / useInvalidatePlans / PlanDetail / VISIBILITIES，依赖 ./model 的 SalesForm / salesForm / salesProblems / salesBody / VISIBILITY_LABELS，依赖 ./failure 的 useCatalogFailure，依赖 ./Highlights 的 HighlightsField，依赖 ./Plans.module.css
 * [OUTPUT]: 对外提供 SalesDrawer，以及向导也用的 GroupPicker
 * [POS]: 套餐详情「销售设置」抽屉（契约后台-04 PUT v1/plans/{id} 的待补·前端入口）：卖点与「标为推荐」（R100）、可见范围与可见用户组、上架时间窗（向导接口不收这两个字段）、三个购买开关、每人限购、库存（只读显示已预留）、排序；整体覆盖，名称 / 代码 / 说明原样回填，卖点与推荐也每次带上当前值。catalog.publish + reauth + 幂等；在售套餐把开关从关改回开受销售开关控制（503）。D-C-1 已决（5.A.2）：归档不可逆，「暂停售卖」就是在这里关新购或隐藏
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Checkbox, Drawer, Input, Select, Switch, useToast } from '../../../ui'
import { useCan, useIntentKey } from '../../actions'
import { useUserGroups } from '../users/api'
import { rowVersionSchema, useInvalidatePlans, VISIBILITIES, type PlanDetail } from './api'
import { useCatalogFailure } from './failure'
import { HighlightsField } from './Highlights'
import { salesBody, salesForm, salesProblems, VISIBILITY_LABELS, type SalesForm } from './model'
import css from './Plans.module.css'

export const VISIBILITY_OPTIONS = VISIBILITIES.map((v) => ({
  value: v,
  label: VISIBILITY_LABELS[v],
}))

export function SalesDrawer({ plan, open, onClose }: { plan: PlanDetail; open: boolean; onClose: () => void }) {
  // 每次打开都从最新详情取初值
  return <SalesBody key={open ? `open:${plan.row_version}` : 'closed'} plan={plan} open={open} onClose={onClose} />
}

function SalesBody({ plan, open, onClose }: { plan: PlanDetail; open: boolean; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useCatalogFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidatePlans()
  const [form, setForm] = useState<SalesForm>(() => salesForm(plan))
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const set = <K extends keyof SalesForm>(key: K, value: SalesForm[K]) => {
    setForm((f) => ({ ...f, [key]: value }))
    setErrors({})
  }

  const save = async () => {
    const problems = salesProblems(form, plan.stock_reserved)
    if (Object.keys(problems).length) return setErrors(problems)
    const body = salesBody(form, plan)
    setBusy(true)
    try {
      await api.put(`v1/plans/${encodeURIComponent(plan.id)}`, rowVersionSchema, { body, idempotencyKey: intent.keyFor([plan.id, body]) })
      intent.reset()
      toast('销售设置已保存')
      onClose()
    } catch (e) {
      fail(e, { fields: setErrors, intent })
    } finally {
      setBusy(false)
      void invalidate()
    }
  }

  return (
    <Drawer
      open={open}
      onClose={onClose}
      dismissible={!busy}
      title="销售设置"
      subtitle={plan.name}
      actions={
        <>
          <Button onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button variant="primary" busy={busy} onClick={() => void save()}>
            保存
          </Button>
        </>
      }
    >
      <div className={css.stack}>
        <HighlightsField
          highlights={form.highlights}
          recommended={form.recommended}
          onHighlights={(list) => set('highlights', list)}
          onRecommended={(on) => set('recommended', on)}
          errors={errors}
        />
        <Select label="可见范围" options={VISIBILITY_OPTIONS} value={form.visibility} onChange={(e) => set('visibility', e.target.value as SalesForm['visibility'])} error={errors.visibility} />
        {form.visibility === 'group' && <GroupPicker picked={form.groupIds} onChange={(ids) => set('groupIds', ids)} error={errors.visible_group_ids} />}
        <div className={css.grid2}>
          <Input type="datetime-local" label="上架时间（选填）" value={form.from} onChange={(e) => set('from', e.target.value)} error={errors.visible_from} />
          <Input type="datetime-local" label="下架时间（选填）" value={form.until} onChange={(e) => set('until', e.target.value)} error={errors.visible_until} />
        </div>
        <div className={css.stack}>
          <Switch label="允许新购" checked={form.allowNew} onChange={(e) => set('allowNew', e.target.checked)} />
          <Switch label="允许续费" checked={form.allowRenewal} onChange={(e) => set('allowRenewal', e.target.checked)} />
          <Switch label="允许变更到这个套餐（升级与降级）" checked={form.allowUpgrade} onChange={(e) => set('allowUpgrade', e.target.checked)} />
        </div>
        <div className={css.grid2}>
          <Input
            mono
            label="每人限购"
            placeholder="不限"
            inputMode="numeric"
            value={form.purchaseLimit}
            onChange={(e) => set('purchaseLimit', e.target.value)}
            error={errors.purchase_limit_per_user}
          />
          <Input
            mono
            label="库存总量"
            placeholder="不限"
            inputMode="numeric"
            hint={`已预留 ${plan.stock_reserved}`}
            value={form.stockTotal}
            onChange={(e) => set('stockTotal', e.target.value)}
            error={errors.stock_total}
          />
          <Input mono label="排序（越小越靠前）" inputMode="numeric" value={form.sortOrder} onChange={(e) => set('sortOrder', e.target.value)} error={errors.sort_order} />
        </div>
        <p className={css.small}>关掉「允许新购」或改成隐藏，就是暂停售卖；已有订阅不受影响。在售套餐把开关重新打开需要销售开关已开启。</p>
      </div>
    </Drawer>
  )
}

/** 可见用户组多选：要 iam.user.read 才能列出名字，没有就说明原因 */
export function GroupPicker({ picked, onChange, error }: { picked: readonly string[]; onChange: (ids: string[]) => void; error?: string }) {
  const can = useCan()
  const readable = can('iam.user.read')
  const groups = useUserGroups(readable)
  const toggle = (id: string) => onChange(picked.includes(id) ? picked.filter((x) => x !== id) : [...picked, id])
  return (
    <fieldset className={`${css.stack} ${css.fieldset}`}>
      <legend className={css.small}>可见的用户组</legend>
      {!readable ? (
        <p className={css.small}>没有读取用户组的权限（iam.user.read），看不到组名；已选 {picked.length} 个。</p>
      ) : groups.data?.length === 0 ? (
        <p className={css.small}>还没有用户组，先到「用户 · 用户组」建一个。</p>
      ) : (
        <div className={css.toggles}>
          {groups.data?.map((g) => (
            <Checkbox key={g.id} label={g.name} checked={picked.includes(g.id)} onChange={() => toggle(g.id)} />
          ))}
        </div>
      )}
      {error && (
        <p className={css.fieldError} role="alert">
          {error}
        </p>
      )}
    </fieldset>
  )
}
