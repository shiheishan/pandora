import { useState, type KeyboardEvent, type ReactNode } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Input, Modal, Switch, TextArea, useToast } from '../../../ui'
import { useIntentKey } from '../../actions'
import { planResponseSchema, useApplyPlan, usePlans, usePoolOptions, type PlanKind } from './api'
import { useCatalogFailure } from './failure'
import {
  codeHint,
  createBody,
  createFieldKey,
  createProblems,
  createdToast,
  emptyCreate,
  tierHint,
  TIERS,
  togglePool,
  type CreateForm,
  type Fields,
  type TierKey,
} from './model'
import css from './Plans.module.css'

export function CreateModal(props: { open: boolean; initialKind: PlanKind; onClose: () => void; onCreated: (id: string) => void }) {
  // 每次打开都从初值开始（key 重置状态）
  return props.open ? <CreateBody key={props.initialKind} {...props} /> : null
}

function CreateBody({ initialKind, onClose, onCreated }: { initialKind: PlanKind; onClose: () => void; onCreated: (id: string) => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useCatalogFailure()
  const intent = useIntentKey()
  const applyPlan = useApplyPlan()
  const allPlans = usePlans()
  const pools = usePoolOptions(true)
  const [form, setForm] = useState<CreateForm>(() => emptyCreate(initialKind))
  const [errors, setErrors] = useState<Fields>({})
  const [busy, setBusy] = useState(false)

  const set = (patch: Partial<CreateForm>) => {
    setForm((f) => ({ ...f, ...patch }))
    setErrors({})
  }
  const setTier = (key: TierKey, value: string) => {
    setForm((f) => ({ ...f, tiers: { ...f.tiers, [key]: value } }))
    setErrors({})
  }

  const submit = async () => {
    const problems = createProblems(form)
    if (Object.keys(problems).length) return setErrors(problems)
    const body = createBody(form)
    setBusy(true)
    try {
      const res = await api.post('v1/plans', planResponseSchema, { body, idempotencyKey: intent.keyFor(['plan-create', body]) })
      intent.reset()
      applyPlan(res.plan)
      toast(createdToast(form.publish))
      onCreated(res.plan.id)
      onClose()
    } catch (e) {
      fail(e, {
        fields: (fs) => setErrors(Object.fromEntries(Object.entries(fs).map(([k, v]) => [createFieldKey(k, form), v]))),
        intent,
      })
    } finally {
      setBusy(false)
    }
  }

  const otherErrors = Object.entries(errors).filter(([k]) => !KNOWN_KEYS.has(k))
  return (
    <Modal
      open
      onClose={onClose}
      title="新建"
      size={640}
      tall
      divided
      dismissible={!busy}
      actions={
        <>
          <Button size="dialog" onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={busy} onClick={() => void submit()}>
            {form.kind === 'subscription' ? '创建订阅套餐' : '创建一次性套餐'}
          </Button>
        </>
      }
    >
      <div className={css.formStack}>
        <KindCards value={form.kind} onChange={(kind) => set({ kind })} />
        <div className={css.grid2}>
          <Input
            label="名称"
            placeholder={form.kind === 'subscription' ? '如：标准版' : '如：年度买断'}
            value={form.name}
            error={errors.name}
            hint={codeHint(form.name, (allPlans.data ?? []).map((p) => p.code)) || undefined}
            onChange={(e) => set({ name: e.target.value })}
          />
          <TextArea
            label="说明（选填）"
            placeholder="用户端卡片上展示的一句话"
            rows={3}
            value={form.description}
            error={errors.description}
            onChange={(e) => set({ description: e.target.value })}
          />
        </div>
        {form.kind === 'subscription' ? <TierSection form={form} errors={errors} setTier={setTier} /> : <OnceSection form={form} errors={errors} set={set} />}
        <section className={css.section}>
          <h3 className={css.sectionTitle}>额度</h3>
          <div className={css.grid2}>
            <Input
              label={form.kind === 'subscription' ? '每周期流量（GB）' : '总流量（GB）'}
              placeholder="留空为不限"
              inputMode="numeric"
              mono
              value={form.gb}
              error={errors.traffic_bytes}
              onChange={(e) => set({ gb: e.target.value })}
            />
            <Input
              label="设备上限"
              placeholder="留空为不限"
              inputMode="numeric"
              mono
              value={form.devices}
              error={errors.max_devices}
              onChange={(e) => set({ devices: e.target.value })}
            />
          </div>
        </section>
        <PoolSection form={form} pools={pools} error={errors.pool_ids} onToggle={(id) => set({ poolIds: togglePool(form.poolIds, id) })} />
        <section className={css.section}>
          <h3 className={css.sectionTitle}>流量重置</h3>
          <div className={css.switchRow}>
            <Switch checked={form.resetOn} onChange={(e) => set({ resetOn: e.target.checked })} label="允许用户付费重置流量" />
            <span className={css.switchMeta}>已用流量清零，上限不变</span>
          </div>
          {form.resetOn && (
            <div className={css.grid2}>
              <Input
                label="每次价格（元）"
                inputMode="decimal"
                mono
                value={form.resetPrice}
                error={errors['reset.price']}
                onChange={(e) => set({ resetPrice: e.target.value })}
              />
              <Input
                label={form.kind === 'subscription' ? '每周期最多次数' : '有效期内最多次数'}
                placeholder="留空为不限"
                inputMode="numeric"
                mono
                value={form.resetLimit}
                error={errors['reset.limit']}
                onChange={(e) => set({ resetLimit: e.target.value })}
              />
            </div>
          )}
        </section>
        <section className={css.section}>
          <h3 className={css.sectionTitle}>上架</h3>
          <Switch checked={form.publish} onChange={(e) => set({ publish: e.target.checked })} label="创建后立即上架" />
          <p className={css.sectionNote}>关掉就先存为草稿，之后在详情的「版本」里发布。</p>
        </section>
        {otherErrors.length > 0 && (
          <p className={css.formError} role="alert">
            {otherErrors.map(([, v]) => v).join('；')}
          </p>
        )}
      </div>
    </Modal>
  )
}

/** 422 里不属于任何输入框的错误键（kind、reference 等） */
const KNOWN_KEYS = new Set(['name', 'description', 'traffic_bytes', 'max_devices', 'pool_ids', 'reset.price', 'reset.limit', ...TIERS.map((t) => `tier.${t.key}`), 'unit_amount', 'validity_days'])

// ---------------------------------------------------------------------------
// 类型卡：radiogroup + 左右方向键切换
// ---------------------------------------------------------------------------
const KIND_CARDS: ReadonlyArray<readonly [PlanKind, string, string]> = [
  ['subscription', '订阅套餐', '按月 / 季 / 半年 / 年付费，到期由用户续费；流量每周期清零'],
  ['one_time', '一次性套餐', '付一次用一段时间或永久，不能续费；整个有效期一份总流量'],
]

function KindCards({ value, onChange }: { value: PlanKind; onChange: (kind: PlanKind) => void }) {
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return
    e.preventDefault()
    const i = KIND_CARDS.findIndex(([k]) => k === value)
    const next = e.key === 'ArrowRight' ? (i + 1) % KIND_CARDS.length : (i - 1 + KIND_CARDS.length) % KIND_CARDS.length
    onChange(KIND_CARDS[next]![0])
  }
  return (
    <div role="radiogroup" aria-label="类型" className={css.typeGroup} onKeyDown={onKeyDown}>
      {KIND_CARDS.map(([kind, label, desc]) => (
        <button
          key={kind}
          type="button"
          role="radio"
          aria-checked={kind === value}
          tabIndex={kind === value ? 0 : -1}
          className={kind === value ? `${css.typeCard} ${css.typeCardActive}` : css.typeCard}
          onClick={() => onChange(kind)}
        >
          <span className={css.typeCardHead}>
            <span className={css.radioDot} aria-hidden="true">
              {kind === value && <span className={css.radioDotInner} />}
            </span>
            {label}
          </span>
          <span className={css.typeCardDesc}>{desc}</span>
        </button>
      ))}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 价格小节：订阅四档 / 一次性一个价 + 有效天数
// ---------------------------------------------------------------------------
function TierSection({ form, errors, setTier }: { form: CreateForm; errors: Fields; setTier: (key: TierKey, value: string) => void }) {
  return (
    <section className={css.section}>
      <h3 className={css.sectionTitle}>价格 · 填了价格的周期才会售卖</h3>
      <div className={css.grid2}>
        {TIERS.map((tier) => (
          <Input
            key={tier.key}
            label={`${tier.label}（元）`}
            placeholder="不售卖"
            inputMode="decimal"
            mono
            value={form.tiers[tier.key]}
            hint={tierHint(form.tiers[tier.key], tier.key, form.tiers['month:1']!) || undefined}
            error={errors[`tier.${tier.key}`]}
            onChange={(e) => setTier(tier.key, e.target.value)}
          />
        ))}
      </div>
    </section>
  )
}

function OnceSection({ form, errors, set }: { form: CreateForm; errors: Fields; set: (patch: Partial<CreateForm>) => void }) {
  return (
    <section className={css.section}>
      <h3 className={css.sectionTitle}>价格与有效期</h3>
      <div className={css.grid2}>
        <Input
          label="价格（元）"
          inputMode="decimal"
          mono
          value={form.amount}
          error={errors.unit_amount}
          onChange={(e) => set({ amount: e.target.value })}
        />
        <Input
          label="有效期（天）"
          placeholder="留空为永久"
          inputMode="numeric"
          mono
          value={form.days}
          error={errors.validity_days}
          onChange={(e) => set({ days: e.target.value })}
        />
      </div>
    </section>
  )
}

// ---------------------------------------------------------------------------
// 线路小节：每个节点池一行开关
// ---------------------------------------------------------------------------
function PoolSection({
  form,
  pools,
  error,
  onToggle,
}: {
  form: CreateForm
  pools: { data: ReadonlyArray<{ id: string; name: string; active_nodes: number }> | undefined; isPending: boolean }
  error: string | undefined
  onToggle: (id: string) => void
}) {
  let body: ReactNode
  if (pools.isPending) {
    body = <p className={css.sectionNote}>载入节点池…</p>
  } else if (!pools.data || pools.data.length === 0) {
    body = <p className={css.sectionNote}>还没有可用的节点池，先到「节点与服务器 › 节点池」建一个。</p>
  } else {
    body = (
      <>
        {pools.data.map((p) => (
          <div key={p.id} className={css.poolRow}>
            <Switch checked={form.poolIds.includes(p.id)} onChange={() => onToggle(p.id)} label={p.name} />
            <span className={css.poolMeta}>{p.active_nodes} 个节点</span>
          </div>
        ))}
      </>
    )
  }
  return (
    <section className={css.section}>
      <h3 className={css.sectionTitle}>线路</h3>
      {body}
      {error && (
        <p className={css.formError} role="alert">
          {error}
        </p>
      )}
    </section>
  )
}
