import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { formatDateTime } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, IconClose, Input, Menu, Modal, Select, Switch, Tag, TextArea, useToast, type MenuEntry } from '../../../ui'
import css from './announce.module.css'
import { ANN_STATUS, annActions, annBody, annFieldErrors, annFormFrom, annMeta, emptyAnnForm, SEVERITY, validateAnn, type AnnBody, type AnnForm } from './logic'
import { useFailure, useIntentKey, useInvalidateContent } from './queries'
import { annSaved, annWithdrawn, SEVERITIES, type Announcement, type AnnouncementsData, type Severity } from './schemas'

interface Props {
  data: AnnouncementsData
  /** null = 新建（本地草稿） */
  original: Announcement | null
  prefill: AnnForm | null
  onCopy: (form: AnnForm) => void
  onClose: () => void
}

const same = (a: AnnForm, b: AnnForm) => JSON.stringify(a) === JSON.stringify(b)

export function AnnounceEditor({ data, original, prefill, onCopy, onClose }: Props) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateContent()
  const fresh = () => (original ? annFormFrom(original) : (prefill ?? emptyAnnForm()))
  const [form, setForm] = useState<AnnForm>(fresh)
  // base：表单是从哪一版载入的；seen：上次看到的版本号。版本变了而表单没改就跟着换成最新
  const [base, setBase] = useState<AnnForm>(fresh)
  const [seen, setSeen] = useState(original?.version ?? 0)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [withdrawing, setWithdrawing] = useState(false)
  const dirty = !same(form, base)
  if (original && original.version !== seen) {
    setSeen(original.version)
    if (!dirty) {
      setForm(annFormFrom(original))
      setBase(annFormFrom(original))
    }
  }
  const stale = original !== null && dirty && !same(base, annFormFrom(original))

  const status = original?.status ?? 'new'
  const actions = annActions(status, form.publishAt)
  const locked = actions.readOnly
  const edit = (patch: Partial<AnnForm>) => setForm((f) => ({ ...f, ...patch }))

  const save = useMutation({
    mutationFn: (body: AnnBody) => {
      const idempotencyKey = intent.keyFor([original?.id ?? 'new', body])
      return original ? api.post(`v1/announcements/${original.id}`, annSaved, { body, idempotencyKey }) : api.post('v1/announcements', annSaved, { body, idempotencyKey })
    },
    onSuccess: (r, body) => {
      intent.reset()
      setBase(form)
      const when = body.publish_at ? formatDateTime(body.publish_at).slice(5) : ''
      toast(r.status === 'scheduled' ? `已定时，${when} 发布` : r.status === 'draft' ? '草稿已保存' : original?.status === 'published' ? '已保存修改' : '公告已发布')
      // 等列表带上新公告再关弹窗，回来就能看到它
      void invalidate('announcements').then(onClose)
    },
    onError: (error) => {
      if (isApiError(error, 'conflict')) void invalidate('announcements')
      fail(error, { fields: (f) => setErrors(annFieldErrors(f)), intent })
    },
  })

  const withdraw = useMutation({
    mutationFn: (a: Announcement) => api.post(`v1/announcements/${a.id}/withdraw`, annWithdrawn, { body: { expected_version: a.version }, idempotencyKey: intent.keyFor([a.id, 'withdraw', a.version]) }),
    onSuccess: () => {
      intent.reset()
      toast('公告已撤回')
      void invalidate('announcements').then(onClose)
    },
    onError: (error) => {
      if (isApiError(error, 'conflict')) void invalidate('announcements')
      fail(error, { intent })
    },
  })

  const submit = (publish: boolean) => {
    const errs = validateAnn(form)
    setErrors(errs)
    if (Object.keys(errs).length === 0) save.mutate(annBody(form, publish, original))
  }

  const busy = save.isPending || withdraw.isPending
  const disabled = locked || busy
  // 底部左提示：已撤回没有，已发布说明保存即生效，其余提醒两个留空的行为
  const hint = locked ? null : actions.scheduleLocked ? '已发布，修改保存后用户端立即更新' : '定时发布留空即立即发布；自动下线留空则一直显示'

  return (
    <Modal
      open
      size={640}
      divided
      dismissible={!busy}
      onClose={onClose}
      title={
        <span className={css.headRow}>
          {/* 新建还没有状态，头部按草稿显示 */}
          {original ? <Tag tone={ANN_STATUS[original.status].tone}>{ANN_STATUS[original.status].label}</Tag> : <Tag tone="warn">草稿</Tag>}
          <span className={css.headMeta}>{annMeta(original)}</span>
        </span>
      }
      headExtra={
        <Button variant="ghost" size="sm" aria-label="关闭" onClick={onClose}>
          <IconClose />
        </Button>
      }
      footerStart={hint ? <span className={css.footerHint}>{hint}</span> : undefined}
      actions={
        <>
          {locked && original && (
            <Button size="dialog" variant="primary" onClick={() => onCopy({ ...annFormFrom(original), publishAt: '', expiresAt: '' })}>
              复制为新公告
            </Button>
          )}
          {actions.withdraw && (
            <Button size="dialog" className={css.dangerText} disabled={busy} busy={withdraw.isPending} onClick={() => setWithdrawing(true)}>
              撤回
            </Button>
          )}
          {actions.draft && (
            <Button size="dialog" disabled={busy} busy={save.isPending && save.variables?.publish === false} onClick={() => submit(false)}>
              存为草稿
            </Button>
          )}
          {actions.primary && (
            <Button size="dialog" variant="primary" disabled={busy} busy={save.isPending && save.variables?.publish === true} onClick={() => submit(true)}>
              {actions.primary}
            </Button>
          )}
        </>
      }
    >
      {locked && <p className={css.lockedNote}>已撤回的公告不能再编辑或重新发布。需要再发一次时，复制为新公告。</p>}
      {stale && original && (
        <div className={`${css.banner} ${css.bannerWarn} ${css.bannerRow}`}>
          <span>这条公告刚被其他管理员修改过，保存会覆盖对方的修改。</span>
          <span className={css.bannerSpacer} />
          <Button
            size="xs"
            onClick={() => {
              setForm(annFormFrom(original))
              setBase(annFormFrom(original))
            }}
          >
            载入最新
          </Button>
        </div>
      )}

      <Input aria-label="公告标题" className={css.titleInput} value={form.title} placeholder="公告标题" error={errors.title} disabled={disabled} onChange={(e) => edit({ title: e.target.value })} />
      <TextArea aria-label="公告正文" rows={9} value={form.body} placeholder="正文，支持 Markdown" error={errors.body} disabled={disabled} onChange={(e) => edit({ body: e.target.value })} />

      <div className={css.fieldGrid}>
        <label className={css.field}>
          <span className={css.fieldLabel}>级别</span>
          <Select size="sm" value={form.severity} disabled={disabled} options={SEVERITIES.map((s) => ({ value: s, label: SEVERITY[s].label }))} onChange={(e) => edit({ severity: e.target.value as Severity })} />
        </label>
        <label className={css.field}>
          <span className={css.fieldLabel}>可见范围</span>
          <TargetPicker data={data} original={original} form={form} disabled={disabled} onChange={edit} />
        </label>
        <label className={css.field}>
          <span className={css.fieldLabel}>定时发布</span>
          <Input
            type="datetime-local"
            size="sm"
            className={css.timeInput}
            value={form.publishAt}
            disabled={disabled || actions.scheduleLocked}
            title={status === 'published' ? '已发布的公告不能改发布时间' : undefined}
            onChange={(e) => edit({ publishAt: e.target.value })}
          />
        </label>
        <label className={css.field}>
          <span className={css.fieldLabel}>自动下线</span>
          <Input type="datetime-local" size="sm" className={css.timeInput} value={form.expiresAt} disabled={disabled} onChange={(e) => edit({ expiresAt: e.target.value })} />
        </label>
      </div>
      {(errors.expiresAt || errors.targets) && (
        <div className={css.errorStack}>
          {errors.expiresAt && (
            <span className={css.error} role="alert">
              {errors.expiresAt}
            </span>
          )}
          {errors.targets && (
            <span className={css.error} role="alert">
              {errors.targets}
            </span>
          )}
        </div>
      )}

      <div>
        <Switch label="置顶" checked={form.pinned} disabled={disabled} onChange={(e) => edit({ pinned: e.target.checked })} />
      </div>

      <ConfirmModal
        open={withdrawing}
        title={status === 'scheduled' ? '取消定时并撤回？' : '撤回公告？'}
        body={
          status === 'scheduled'
            ? '这条公告不会再发布，撤回后也不能再编辑，只能复制为新公告。只想改时间的话，直接修改定时发布时间或存回草稿。'
            : '用户端立即隐藏这条公告，已推送出去的通知不会撤回。撤回后不能再编辑或重新发布，只能复制为新公告。'
        }
        confirmLabel="撤回"
        tone="danger"
        onConfirm={() => {
          if (original) withdraw.mutate(original)
          setWithdrawing(false)
        }}
        onCancel={() => setWithdrawing(false)}
      />
    </Modal>
  )
}

/** 可见范围：套餐与用户组两组开关，全不选 = 全部用户；已删除的套餐仍列出来，方便取消 */
function TargetPicker({ data, original, form, disabled, onChange }: { data: AnnouncementsData; original: Announcement | null; form: AnnForm; disabled: boolean; onChange: (p: Partial<AnnForm>) => void }) {
  const plans = [...data.plans.map((p) => ({ id: p.id, name: p.status === 'archived' ? `${p.name}（已归档）` : p.name }))]
  for (const id of form.planIds) if (!plans.some((p) => p.id === id)) plans.push({ id, name: original?.plan_targets.find((t) => t.id === id)?.name ?? '不可用套餐' })
  const groups = [...data.user_groups]
  for (const id of form.groupIds) if (!groups.some((g) => g.id === id)) groups.push({ id, name: original?.user_group_targets.find((t) => t.id === id)?.name ?? '已删除的用户组' })

  const flip = (list: string[], id: string, on: boolean) => (on ? [...list, id] : list.filter((x) => x !== id))
  const names = [...plans.filter((p) => form.planIds.includes(p.id)), ...groups.filter((g) => form.groupIds.includes(g.id))].map((x) => x.name)
  const entries: MenuEntry[] = [
    { key: 'all', label: '全部用户', current: names.length === 0, onSelect: () => onChange({ planIds: [], groupIds: [] }) },
    ...(plans.length > 0 ? [{ kind: 'separator', key: 'sp' } as const] : []),
    ...plans.map((p): MenuEntry => ({ kind: 'toggle', key: `p:${p.id}`, label: `套餐 · ${p.name}`, checked: form.planIds.includes(p.id), onChange: (on) => onChange({ planIds: flip(form.planIds, p.id, on) }) })),
    ...(groups.length > 0 ? [{ kind: 'separator', key: 'sg' } as const] : []),
    ...groups.map((g): MenuEntry => ({ kind: 'toggle', key: `g:${g.id}`, label: `用户组 · ${g.name}`, checked: form.groupIds.includes(g.id), onChange: (on) => onChange({ groupIds: flip(form.groupIds, g.id, on) }) })),
  ]
  const label = names.length === 0 ? '全部用户' : names.join('、')
  if (disabled) return <span className={css.picker}>{label}</span>
  return (
    <Menu
      label="可见范围"
      align="start"
      triggerLabel={`可见范围：${label}`}
      triggerClassName={css.picker}
      menuClassName={css.pickerMenu}
      trigger={<span className={css.pickerText}>{label}</span>}
      entries={entries}
    />
  )
}
