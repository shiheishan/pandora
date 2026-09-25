/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/api 的 isApiError，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / Card / Checkbox / ConfirmModal / Input / Select / Tag / TextArea / useToast，依赖 ../../actions 的 endsIntent / useCan / useIntentKey，依赖 ./api 的写响应 schema、useInvalidatePlans、PlanDetail / VersionRow，依赖 ./model 的版本表单与文案，依赖 ./failure 的 useCatalogFailure，依赖 ./Plans.module.css
 * [OUTPUT]: 对外提供 Versions
 * [POS]: 套餐详情的「版本」卡（后台-04）：版本行（vN、额度摘要、「草稿 · 创建人 · 日期」或发布日、草稿 / 当前发布 / 历史），展开是设计稿的三项（流量 GB、设备上限、限速 Mbps）加「高级」折叠（重置策略、超额策略、宽限、续费语义、并发、设备释放、备注；权益与其它配额原样回填）。草稿「保存草稿」走 PUT versions（catalog.write）；已发布版本在没有草稿时可「另存为新版本」。「新建版本」= POST versions → PUT 复制当前版本语义 → POST pools 复制绑定（后端不复制，契约后台-04），已有草稿时禁用。发布走确认框（catalog.publish + reauth + 幂等），先把看得出的前置条件列出来。D-C-5 已决（5.A.2、R99：写多少限多少、与超额策略无关），第 ② 步改；在那之前限速只在「用完限速」策略下可填
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { useApi } from '../../../shell/runtime'
import { Button, Card, Checkbox, ConfirmModal, Input, Select, Tag, TextArea, useToast } from '../../../ui'
import { endsIntent, useCan, useIntentKey } from '../../actions'
import { poolsBoundSchema, publishedSchema, rowVersionSchema, useInvalidatePlans, versionCreatedSchema, type PlanDetail, type VersionRow } from './api'
import { useCatalogFailure } from './failure'
import { OVERAGE_LABELS, publishBlockers, RESET_LABELS, versionBody, versionForm, versionNote, versionProblems, versionSummary, versionView, type VersionForm } from './model'
import css from './Plans.module.css'

const RESET_OPTIONS = Object.entries(RESET_LABELS).map(([value, label]) => ({ value, label }))
const OVERAGE_OPTIONS = Object.entries(OVERAGE_LABELS).map(([value, label]) => ({ value, label }))

/** 后端把流量报在 quotas.{i}.limit，表单上是「每周期流量」一格 */
const mapFields = (fields: Record<string, string>) => Object.fromEntries(Object.entries(fields).map(([k, v]) => [k.startsWith('quotas') ? 'traffic' : k, v]))

export function Versions({ plan }: { plan: PlanDetail }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useCatalogFailure()
  const invalidate = useInvalidatePlans()
  const createIntent = useIntentKey()
  const poolIntent = useIntentKey()
  const [open, setOpen] = useState<string | null>(null)
  const [publishing, setPublishing] = useState<VersionRow | null>(null)
  const [creating, setCreating] = useState(false)
  const archived = plan.status === 'archived'
  const draft = plan.versions.find((v) => v.status === 'draft')
  const base = plan.versions.find((v) => v.id === plan.current_version_id) ?? plan.versions[0]

  /** 草稿建出来后把 base 的语义写进去；form 给了就用表单值（「另存为新版本」） */
  const createDraft = async (from: VersionRow | undefined, form?: VersionForm) => {
    setCreating(true)
    let created: VersionRow | null = null
    try {
      created = (await api.post(`v1/plans/${encodeURIComponent(plan.id)}/versions`, versionCreatedSchema, { idempotencyKey: createIntent.keyFor([plan.id, 'version']) })).version
      createIntent.reset()
      if (!from) {
        toast(`已建草稿 v${created.version}，填好额度后发布`)
        return setOpen(created.id)
      }
      const saved = await api.put(`v1/plans/${encodeURIComponent(plan.id)}/versions/${encodeURIComponent(created.id)}`, rowVersionSchema, {
        body: versionBody(form ?? versionForm(from), from, created.row_version),
      })
      let pools = ''
      if (from.pool_ids.length && can('catalog.publish')) {
        const body = { version_id: created.id, expected_version_row_version: saved.row_version, pool_ids: from.pool_ids }
        await api.post(`v1/plans/${encodeURIComponent(plan.id)}/pools`, poolsBoundSchema, { body, idempotencyKey: poolIntent.keyFor([plan.id, body]) })
        poolIntent.reset()
      } else if (from.pool_ids.length) {
        pools = '；节点池绑定要发布权限，草稿暂时没有线路'
      }
      toast(`已建草稿 v${created.version}，沿用 v${from.version} 的额度与线路${pools}`)
      setOpen(created.id)
    } catch (e) {
      if (!created) return void fail(e, { intent: createIntent })
      // 草稿已经建出来，只是复制没做完：说清楚现状，让管理员在草稿上接着填
      if (endsIntent(e)) poolIntent.reset()
      const why = isApiError(e) && e.code !== 'reauth_required' ? `（${e.message}）` : ''
      toast(`草稿 v${created.version} 已建好，但复制 v${from?.version ?? ''} 的设置没做完${why}，请在草稿上检查后再发布`, 'danger')
      setOpen(created.id)
    } finally {
      setCreating(false)
      void invalidate()
    }
  }

  return (
    <Card
      flush
      title={
        <>
          版本 <span className={css.cardSub}>额度与权益以版本为单位发布，新购与续费使用当前发布版本</span>
        </>
      }
      extra={
        can('catalog.write') &&
        !archived && (
          <Button
            size="xs"
            variant="outline"
            busy={creating}
            disabled={draft !== undefined}
            title={draft ? `已有草稿 v${draft.version}，发布或在它上面改` : undefined}
            onClick={() => void createDraft(base)}
          >
            新建版本
          </Button>
        )
      }
    >
      {plan.versions.length === 0 && <p className={`${css.small} ${css.cardPad}`}>还没有版本。</p>}
      {plan.versions.map((v) => {
        const view = versionView(v, plan.current_version_id)
        const expanded = open === v.id
        return (
          <div key={v.id} className={css.version}>
            <div className={css.versionRow}>
              <span className={css.versionNo}>v{v.version}</span>
              <span className={css.priceMeta}>
                <span>{versionSummary(v)}</span>
                <span className={css.small}>{versionNote(v)}</span>
              </span>
              <span>
                <Tag tone={view.tone}>{view.label}</Tag>
              </span>
              <span className={css.versionActions}>
                <Button size="xs" variant="link" aria-expanded={expanded} onClick={() => setOpen(expanded ? null : v.id)}>
                  {expanded ? '收起' : v.status === 'draft' && can('catalog.write') && !archived ? '编辑' : '查看'}
                </Button>
                {v.status === 'draft' && can('catalog.publish') && !archived && (
                  <Button size="xs" variant="ghost" onClick={() => setPublishing(v)}>
                    发布
                  </Button>
                )}
              </span>
            </div>
            {expanded && (
              <VersionEditor
                key={`${v.id}:${v.row_version}`}
                plan={plan}
                version={v}
                editable={v.status === 'draft' && can('catalog.write') && !archived}
                saveAs={v.status !== 'draft' && !draft && can('catalog.write') && !archived ? (form) => void createDraft(v, form) : null}
                draft={draft}
              />
            )}
          </div>
        )
      })}
      <PublishDialog plan={plan} version={publishing} onClose={() => setPublishing(null)} />
    </Card>
  )
}

function VersionEditor({
  plan,
  version,
  editable,
  saveAs,
  draft,
}: {
  plan: PlanDetail
  version: VersionRow
  editable: boolean
  saveAs: ((form: VersionForm) => void) | null
  draft: VersionRow | undefined
}) {
  const api = useApi()
  const toast = useToast()
  const fail = useCatalogFailure()
  const invalidate = useInvalidatePlans()
  const [form, setForm] = useState<VersionForm>(() => versionForm(version))
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const writable = editable || saveAs !== null
  const set = <K extends keyof VersionForm>(key: K, value: VersionForm[K]) => {
    setForm((f) => ({ ...f, [key]: value }))
    setErrors({})
  }

  const submit = async () => {
    const problems = versionProblems(form)
    if (Object.keys(problems).length) return setErrors(problems)
    if (saveAs) return saveAs(form)
    setBusy(true)
    try {
      await api.put(`v1/plans/${encodeURIComponent(plan.id)}/versions/${encodeURIComponent(version.id)}`, rowVersionSchema, { body: versionBody(form, version, version.row_version) })
      toast('草稿已保存')
    } catch (e) {
      fail(e, (f) => setErrors(mapFields(f)))
    } finally {
      setBusy(false)
      void invalidate()
    }
  }

  const throttle = form.overage === 'throttle'
  const kept = [
    version.entitlements.length ? `${version.entitlements.length} 项权益` : '',
    version.quotas.filter((q) => q.metric !== 'traffic.bytes' && q.metric !== 'devices.active').length ? '其它配额' : '',
  ]
    .filter(Boolean)
    .join('与')

  return (
    <div className={css.versionForm}>
      <div className={css.versionMain}>
        <Input size="sm" mono label="每周期流量 GB" placeholder="不限" inputMode="numeric" disabled={!writable} value={form.gb} onChange={(e) => set('gb', e.target.value)} error={errors.traffic} />
        <Input
          size="sm"
          mono
          label="设备上限"
          placeholder="不限"
          inputMode="numeric"
          disabled={!writable}
          value={form.devices}
          onChange={(e) => set('devices', e.target.value)}
          error={errors.max_devices}
        />
        <Input
          size="sm"
          mono
          label="限速 Mbps"
          placeholder={throttle ? '必填' : '不限'}
          inputMode="decimal"
          disabled={!writable || !throttle}
          hint={throttle || !writable ? undefined : '仅「用完限速」策略可填，见高级'}
          value={form.mbps}
          onChange={(e) => set('mbps', e.target.value)}
          error={errors.throttle_kbps}
        />
        {writable && (
          <Button size="sm" variant="primary" className={css.alignEnd} busy={busy} onClick={() => void submit()}>
            {saveAs ? '另存为新版本' : '保存草稿'}
          </Button>
        )}
      </div>
      {!writable && version.status !== 'draft' && draft && <p className={css.small}>已发布的版本不能改；已有草稿 v{draft.version}，在草稿上改。</p>}
      {saveAs && <p className={css.small}>已发布的版本不能改，保存会另建一个草稿版本，发布后才生效。</p>}
      <details className={css.advanced}>
        <summary>高级：重置、超额、宽限与续费</summary>
        <div className={css.stack}>
          <div className={css.grid3}>
            <Select
              size="sm"
              label="流量重置"
              options={RESET_OPTIONS}
              disabled={!writable}
              value={form.strategy}
              onChange={(e) => set('strategy', e.target.value as VersionForm['strategy'])}
              error={errors.quota_reset_strategy}
            />
            {form.strategy === 'fixed_day' && (
              <Input
                size="sm"
                mono
                label="每月几号（1–28）"
                inputMode="numeric"
                disabled={!writable}
                value={form.resetDay}
                onChange={(e) => set('resetDay', e.target.value)}
                error={errors.quota_reset_day}
              />
            )}
            <Select
              size="sm"
              label="流量用完后"
              options={OVERAGE_OPTIONS}
              disabled={!writable}
              value={form.overage}
              onChange={(e) => set('overage', e.target.value as VersionForm['overage'])}
              error={errors.overage_policy}
            />
            <Input
              size="sm"
              mono
              label="宽限期（小时）"
              inputMode="numeric"
              disabled={!writable}
              value={form.graceHours}
              onChange={(e) => set('graceHours', e.target.value)}
              error={errors.grace_period_hours}
            />
            <Input
              size="sm"
              mono
              label="最大并发连接"
              placeholder="不限"
              inputMode="numeric"
              disabled={!writable}
              value={form.maxConcurrent}
              onChange={(e) => set('maxConcurrent', e.target.value)}
              error={errors.max_concurrent}
            />
            <Input
              size="sm"
              mono
              label="设备释放（小时）"
              inputMode="numeric"
              disabled={!writable}
              value={form.releaseHours}
              onChange={(e) => set('releaseHours', e.target.value)}
              error={errors.device_release_hours}
            />
          </div>
          <div className={css.toggles}>
            <Checkbox label="宽限期内保持服务" disabled={!writable} checked={form.graceKeeps} onChange={(e) => set('graceKeeps', e.target.checked)} />
            <Checkbox label="续费顺延周期" disabled={!writable} checked={form.renewalExtends} onChange={(e) => set('renewalExtends', e.target.checked)} />
            <Checkbox label="续费重置流量" disabled={!writable} checked={form.renewalResets} onChange={(e) => set('renewalResets', e.target.checked)} />
            <Checkbox label="续费保留加购" disabled={!writable} checked={form.renewalKeepsAddons} onChange={(e) => set('renewalKeepsAddons', e.target.checked)} />
          </div>
          <TextArea label="备注（内部）" rows={2} disabled={!writable} value={form.notes} onChange={(e) => set('notes', e.target.value)} error={errors.notes} />
          {kept && <p className={css.small}>这个版本的{kept}在这里不显示，保存时原样保留。</p>}
        </div>
      </details>
    </div>
  )
}

/** POST v1/plans/{id}/versions/{vid}/publish：两个乐观锁都取自详情 */
function PublishDialog({ plan, version, onClose }: { plan: PlanDetail; version: VersionRow | null; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useCatalogFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidatePlans()
  const blockers = version ? publishBlockers(plan, version) : []
  const confirm = async () => {
    if (!version) return
    const body = { expected_plan_row_version: plan.row_version, expected_version_row_version: version.row_version }
    try {
      await api.post(`v1/plans/${encodeURIComponent(plan.id)}/versions/${encodeURIComponent(version.id)}/publish`, publishedSchema, { body, idempotencyKey: intent.keyFor([version.id, body]) })
      intent.reset()
      toast(`v${version.version} 已发布${plan.status === 'draft' ? '，套餐已上架' : ''}`)
      onClose()
    } catch (e) {
      fail(e, { intent })
    } finally {
      void invalidate()
    }
  }
  return (
    <ConfirmModal
      open={version !== null}
      title={`发布 v${version?.version ?? ''}？`}
      body={
        <>
          新购与续费立即使用此版本，已生效周期不变。
          {blockers.map((b) => (
            <span key={b} className={`${css.note} ${css.noteBlock}`}>
              {b}
            </span>
          ))}
        </>
      }
      confirmLabel="发布"
      onCancel={onClose}
      onConfirm={confirm}
    />
  )
}
