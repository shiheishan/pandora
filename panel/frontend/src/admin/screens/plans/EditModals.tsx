import { useCallback, useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Checkbox, Input, Modal, Select, Switch, TextArea, useToast } from '../../../ui'
import { useIntentKey } from '../../actions'
import { useUserGroups } from '../users/api'
import { assignableGroups, groupOptionLabel } from '../users/model'
import { planResponseSchema, useApplyPlan, useInvalidatePlans, VISIBILITIES, type PlanDetail, type Visibility } from './api'
import css from './Detail.module.css'
import { useCatalogFailure } from './failure'
import {
  basicBody,
  basicForm,
  basicProblems,
  onceBody,
  onceForm,
  onceProblems,
  resetBody,
  resetForm,
  resetProblems,
  salesBody,
  salesForm,
  salesProblems,
  usageBody,
  usageForm,
  usageProblems,
  VISIBILITY_LABELS,
  type BasicForm,
  type OnceForm,
  type ResetForm,
  type SalesForm,
  type UsageForm,
} from './model'

export function usePlanWrite(): {
  busy: boolean
  /** 成功回最新详情（已写回缓存并失效列表）；失败已经处理（标表单或 Toast），回 null */
  run: (method: 'put' | 'post', path: string, body: object, opts?: { onFields?: (f: Record<string, string>) => void }) => Promise<PlanDetail | null>
} {
  const api = useApi()
  const intent = useIntentKey()
  const fail = useCatalogFailure()
  const applyPlan = useApplyPlan()
  const invalidate = useInvalidatePlans()
  const [busy, setBusy] = useState(false)

  const run = useCallback(
    async (
      method: 'put' | 'post',
      path: string,
      body: object,
      opts?: { onFields?: (f: Record<string, string>) => void },
    ): Promise<PlanDetail | null> => {
      setBusy(true)
      const idempotencyKey = intent.keyFor([path, body])
      try {
        const res = await api[method](path, planResponseSchema, { body, idempotencyKey })
        intent.reset()
        await applyPlan(res.plan)
        return res.plan
      } catch (e) {
        fail(e, { fields: opts?.onFields, intent })
        void invalidate()
        return null
      } finally {
        setBusy(false)
      }
    },
    [api, intent, fail, applyPlan, invalidate],
  )

  return { busy, run }
}

const VISIBILITY_OPTIONS = VISIBILITIES.map((v) => ({
  value: v,
  label: VISIBILITY_LABELS[v],
}))

export function GroupPicker({ picked, onChange, error }: { picked: readonly string[]; onChange: (ids: string[]) => void; error?: string }) {
  const groups = useUserGroups()
  const toggle = (id: string) => onChange(picked.includes(id) ? picked.filter((x) => x !== id) : [...picked, id])
  const [initial] = useState(() => [...picked])
  const shown = assignableGroups(groups.data ?? [], [...initial, ...picked])
  return (
    <fieldset className={`${css.stack} ${css.fieldset}`}>
      <legend className={css.legend}>可见的用户组</legend>
      {groups.data && shown.length === 0 ? (
        <p className={css.mutedSmall}>还没有可选的用户组，先到「用户 · 用户组」建一个或启用。</p>
      ) : (
        <div className={css.toggles}>
          {shown.map((g) => (
            <Checkbox key={g.id} label={groupOptionLabel(g)} checked={picked.includes(g.id)} onChange={() => toggle(g.id)} />
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

export function BasicModal(props: { plan: PlanDetail; open: boolean; onClose: () => void }) {
  return <BasicModalBody key={props.open ? `open:${props.plan.row_version}` : 'closed'} {...props} />
}

function BasicModalBody({ plan, open, onClose }: { plan: PlanDetail; open: boolean; onClose: () => void }) {
  const toast = useToast()
  const { busy, run } = usePlanWrite()
  const [form, setForm] = useState<BasicForm>(() => basicForm(plan))
  const [errors, setErrors] = useState<Record<string, string>>({})

  const save = async () => {
    const problems = basicProblems(form)
    if (Object.keys(problems).length > 0) {
      setErrors(problems)
      return
    }
    const body = basicBody(form, plan.row_version)
    const res = await run('put', `v1/plans/${encodeURIComponent(plan.id)}/basic`, body, { onFields: setErrors })
    if (res) {
      toast('已保存')
      onClose()
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      size={520}
      title="编辑基本资料"
      dismissible={!busy}
      actions={
        <>
          <Button size="dialog" onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={busy} onClick={() => void save()}>
            保存
          </Button>
        </>
      }
    >
      <div className={css.stack}>
        <Input
          label="名称"
          value={form.name}
          onChange={(e) => {
            setForm((f) => ({ ...f, name: e.target.value }))
            setErrors({})
          }}
          hint={`套餐代码 ${plan.code} 由系统生成，改名不影响`}
          error={errors.name}
        />
        <TextArea
          label="说明"
          rows={3}
          value={form.description}
          onChange={(e) => {
            setForm((f) => ({ ...f, description: e.target.value }))
            setErrors({})
          }}
          error={errors.description}
        />
      </div>
    </Modal>
  )
}

export function UsageModal(props: { plan: PlanDetail; open: boolean; onClose: () => void }) {
  return <UsageModalBody key={props.open ? `open:${props.plan.row_version}` : 'closed'} {...props} />
}

function UsageModalBody({ plan, open, onClose }: { plan: PlanDetail; open: boolean; onClose: () => void }) {
  const toast = useToast()
  const { busy, run } = usePlanWrite()
  const [form, setForm] = useState<UsageForm>(() => usageForm(plan))
  const [errors, setErrors] = useState<Record<string, string>>({})

  const subtitle =
    plan.kind === 'subscription'
      ? '改动存为草稿版本，发布后新购使用，老用户下个周期切换'
      : '改动存为草稿版本，发布后新购使用，已买的不受影响'

  const save = async () => {
    const problems = usageProblems(form)
    if (Object.keys(problems).length > 0) {
      setErrors(problems)
      return
    }
    const body = usageBody(form, plan.row_version)
    const res = await run('put', `v1/plans/${encodeURIComponent(plan.id)}/usage`, body, { onFields: setErrors })
    if (res) {
      toast(`已存入草稿 v${res.draft_version}`)
      onClose()
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      size={520}
      title="编辑用量"
      subtitle={subtitle}
      dismissible={!busy}
      actions={
        <>
          <Button size="dialog" onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={busy} onClick={() => void save()}>
            保存
          </Button>
        </>
      }
    >
      <div className={css.stack}>
        <Input
          mono
          inputMode="numeric"
          label={plan.kind === 'subscription' ? '每周期流量（GB）' : '总流量（GB）'}
          placeholder="留空为不限"
          value={form.gb}
          onChange={(e) => {
            setForm((f) => ({ ...f, gb: e.target.value }))
            setErrors({})
          }}
          error={errors.traffic_bytes}
        />
        <Input
          mono
          inputMode="numeric"
          label="同时在线设备上限"
          placeholder="留空为不限"
          value={form.devices}
          onChange={(e) => {
            setForm((f) => ({ ...f, devices: e.target.value }))
            setErrors({})
          }}
          error={errors.max_devices}
        />
        <Input
          mono
          inputMode="numeric"
          label="限速（Mbps）"
          placeholder="留空为不限速"
          value={form.mbps}
          onChange={(e) => {
            setForm((f) => ({ ...f, mbps: e.target.value }))
            setErrors({})
          }}
          error={errors.throttle_kbps}
        />
      </div>
    </Modal>
  )
}

export function SalesModal(props: { plan: PlanDetail; open: boolean; onClose: () => void }) {
  return <SalesModalBody key={props.open ? `open:${props.plan.row_version}` : 'closed'} {...props} />
}

function SalesModalBody({ plan, open, onClose }: { plan: PlanDetail; open: boolean; onClose: () => void }) {
  const toast = useToast()
  const { busy, run } = usePlanWrite()
  const [form, setForm] = useState<SalesForm>(() => salesForm(plan))
  const [errors, setErrors] = useState<Record<string, string>>({})

  const save = async () => {
    const problems = salesProblems(form)
    if (Object.keys(problems).length > 0) {
      setErrors(problems)
      return
    }
    const body = salesBody(form, plan.kind, plan.row_version)
    const res = await run('put', `v1/plans/${encodeURIComponent(plan.id)}/sales`, body, { onFields: setErrors })
    if (res) {
      toast('已保存')
      onClose()
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      size={520}
      title="编辑销售设置"
      dismissible={!busy}
      actions={
        <>
          <Button size="dialog" onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={busy} onClick={() => void save()}>
            保存
          </Button>
        </>
      }
    >
      <div className={css.stack}>
        <Select
          label="可见范围"
          options={VISIBILITY_OPTIONS}
          value={form.visibility}
          onChange={(e) => {
            setForm((f) => ({ ...f, visibility: e.target.value as Visibility }))
            setErrors({})
          }}
          error={errors.visibility}
        />
        {form.visibility === 'group' && (
          <GroupPicker
            picked={form.groupIds}
            onChange={(ids) => {
              setForm((f) => ({ ...f, groupIds: ids }))
              setErrors({})
            }}
            error={errors.visible_group_ids}
          />
        )}
        <Switch
          label="允许新购"
          checked={form.allowNew}
          onChange={(e) => {
            setForm((f) => ({ ...f, allowNew: e.target.checked }))
            setErrors({})
          }}
        />
        {plan.kind === 'subscription' ? (
          <Switch
            label="允许续费"
            checked={form.allowRenewal}
            onChange={(e) => {
              setForm((f) => ({ ...f, allowRenewal: e.target.checked }))
              setErrors({})
            }}
          />
        ) : (
          <div className={css.mutedSmall}>一次性套餐不能续费</div>
        )}
        <Switch
          label="允许从其他套餐变更过来"
          checked={form.allowUpgrade}
          onChange={(e) => {
            setForm((f) => ({ ...f, allowUpgrade: e.target.checked }))
            setErrors({})
          }}
        />
        <Switch
          label="标为推荐"
          checked={form.recommended}
          onChange={(e) => {
            setForm((f) => ({ ...f, recommended: e.target.checked }))
            setErrors({})
          }}
        />
        <div className={css.mutedSmall}>只想暂停售卖，关掉「允许新购」或改成隐藏即可；已有订阅不受影响。</div>
      </div>
    </Modal>
  )
}

export function ResetModal(props: { plan: PlanDetail; open: boolean; onClose: () => void }) {
  return <ResetModalBody key={props.open ? `open:${props.plan.row_version}` : 'closed'} {...props} />
}

function ResetModalBody({ plan, open, onClose }: { plan: PlanDetail; open: boolean; onClose: () => void }) {
  const toast = useToast()
  const { busy, run } = usePlanWrite()
  const [form, setForm] = useState<ResetForm>(() => resetForm(plan))
  const [errors, setErrors] = useState<Record<string, string>>({})

  const save = async () => {
    const problems = resetProblems(form)
    if (Object.keys(problems).length > 0) {
      setErrors(problems)
      return
    }
    const body = resetBody(form, plan.row_version)
    const res = await run('put', `v1/plans/${encodeURIComponent(plan.id)}/reset`, body, { onFields: setErrors })
    if (res) {
      toast('已保存')
      onClose()
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      size={520}
      title="编辑流量重置"
      subtitle="用户付费把已用流量清零，流量上限和到期时间不变"
      dismissible={!busy}
      actions={
        <>
          <Button size="dialog" onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={busy} onClick={() => void save()}>
            保存
          </Button>
        </>
      }
    >
      <div className={css.stack}>
        <Switch
          label="允许用户付费重置流量"
          checked={form.on}
          onChange={(e) => {
            setForm((f) => ({ ...f, on: e.target.checked }))
            setErrors({})
          }}
        />
        {form.on && (
          <div className={css.grid2}>
            <Input
              mono
              inputMode="decimal"
              label="每次价格（元）"
              value={form.price}
              onChange={(e) => {
                setForm((f) => ({ ...f, price: e.target.value }))
                setErrors({})
              }}
              error={errors['reset.price']}
            />
            <Input
              mono
              inputMode="numeric"
              label={plan.kind === 'subscription' ? '每周期最多次数' : '有效期内最多次数'}
              placeholder="留空为不限"
              value={form.limit}
              onChange={(e) => {
                setForm((f) => ({ ...f, limit: e.target.value }))
                setErrors({})
              }}
              error={errors['reset.limit']}
            />
          </div>
        )}
      </div>
    </Modal>
  )
}

export function OnceModal(props: { plan: PlanDetail; open: boolean; onClose: () => void }) {
  return <OnceModalBody key={props.open ? `open:${props.plan.row_version}` : 'closed'} {...props} />
}

function OnceModalBody({ plan, open, onClose }: { plan: PlanDetail; open: boolean; onClose: () => void }) {
  const toast = useToast()
  const { busy, run } = usePlanWrite()
  const [form, setForm] = useState<OnceForm>(() => onceForm(plan))
  const [errors, setErrors] = useState<Record<string, string>>({})

  const save = async () => {
    const problems = onceProblems(form)
    if (Object.keys(problems).length > 0) {
      setErrors(problems)
      return
    }
    const body = onceBody(form, plan.row_version)
    const res = await run('put', `v1/plans/${encodeURIComponent(plan.id)}/prices`, body, { onFields: setErrors })
    if (res) {
      toast('已保存')
      onClose()
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      size={520}
      title="编辑价格与有效期"
      subtitle="只影响之后的购买，已买的按原有效期"
      dismissible={!busy}
      actions={
        <>
          <Button size="dialog" onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={busy} onClick={() => void save()}>
            保存
          </Button>
        </>
      }
    >
      <div className={css.grid2}>
        <Input
          mono
          inputMode="decimal"
          label="价格（元）"
          value={form.amount}
          onChange={(e) => {
            setForm((f) => ({ ...f, amount: e.target.value }))
            setErrors({})
          }}
          error={errors.unit_amount}
        />
        <Input
          mono
          inputMode="numeric"
          label="有效期（天）"
          placeholder="留空为永久"
          value={form.days}
          onChange={(e) => {
            setForm((f) => ({ ...f, days: e.target.value }))
            setErrors({})
          }}
          error={errors.validity_days}
        />
      </div>
    </Modal>
  )
}
