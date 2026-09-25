/**
 * [INPUT]: 依赖 react 的 useId / useState，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../core/api 的 isApiError，依赖 ../../../core/format 的 formatDateTime，依赖 ../../../core/router 的 navigate，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./logic 的公告表单函数，依赖 ./queries，依赖 ./schemas，依赖 ./content.module.css
 * [OUTPUT]: 对外提供 AnnounceEditor（公告编辑区）
 * [POS]: admin/screens/content 公告的右栏：标题、Markdown 正文，设计缺、契约待补·前端的「可见范围」（套餐 + 用户组多选，全不选 = 全部用户；「即将到期」是待决 D-D-2，未决前不提供）、级别、定时发布、自动下线补齐。
 *        保存与撤回都要 reauth + 幂等（announcement_save / announcement_withdraw）、带 expected_version；编辑是全量覆盖，没改的时间原样送回。已发布只能「保存修改」且发布时间锁定，定时可「撤回」（= 取消，不可恢复）或存回草稿，已撤回是终态（待决 D-D-3 未决前）：只读，给「复制为新公告」。
 *        别人改了这条（版本变了）而本地有未保存的修改时不覆盖输入，提示「保存会覆盖对方的修改」并可载入最新
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useId, useState } from 'react'
import { isApiError } from '../../../core/api'
import { formatDateTime } from '../../../core/format'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Input, Menu, Select, Switch, TextArea, useToast, type MenuEntry } from '../../../ui'
import css from './content.module.css'
import { annActions, annBody, annFieldErrors, annFormFrom, dropsIntent, emptyAnnForm, SEVERITY, validateAnn, type AnnBody, type AnnForm } from './logic'
import { useFailure, useIntentKey, useInvalidateContent } from './queries'
import { annSaved, annWithdrawn, SEVERITIES, type Announcement, type AnnouncementsData, type Severity } from './schemas'

interface Props {
  data: AnnouncementsData
  /** null = 新建（本地草稿） */
  original: Announcement | null
  prefill: AnnForm | null
  onCopy: (form: AnnForm) => void
}

const same = (a: AnnForm, b: AnnForm) => JSON.stringify(a) === JSON.stringify(b)

export function AnnounceEditor({ data, original, prefill, onCopy }: Props) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateContent()
  const ids = useId()
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
      // 新建：等列表带上新公告再换地址，免得先落到第一条上闪一下
      void invalidate('announcements').then(() => !original && navigate(`/content/announce/${r.id}`, { replace: true }))
    },
    onError: (error) => {
      if (dropsIntent(error)) intent.reset()
      if (isApiError(error, 'conflict')) void invalidate('announcements')
      fail(error, (f) => setErrors(annFieldErrors(f)))
    },
  })

  const withdraw = useMutation({
    mutationFn: (a: Announcement) => api.post(`v1/announcements/${a.id}/withdraw`, annWithdrawn, { body: { expected_version: a.version }, idempotencyKey: intent.keyFor([a.id, 'withdraw', a.version]) }),
    onSuccess: () => {
      intent.reset()
      toast('公告已撤回')
      void invalidate('announcements')
    },
    onError: (error) => {
      if (dropsIntent(error)) intent.reset()
      if (isApiError(error, 'conflict')) void invalidate('announcements')
      fail(error)
    },
  })

  const submit = (publish: boolean) => {
    const errs = validateAnn(form)
    setErrors(errs)
    if (Object.keys(errs).length === 0) save.mutate(annBody(form, publish, original))
  }

  const busy = save.isPending || withdraw.isPending
  const disabled = locked || busy

  return (
    <section className={css.panel} aria-label="编辑公告">
      {locked && <div className={css.banner}>已撤回的公告不能再编辑或重新发布。需要再发一次时，复制为新公告。</div>}
      {status === 'scheduled' && original?.publish_at && <div className={css.banner}>定时公告，将于 {formatDateTime(original.publish_at)} 自动发布；到点前可以修改或撤回。</div>}
      {stale && original && (
        <div className={`${css.banner} ${css.warnBanner} ${css.row}`}>
          <span>这条公告刚被其他管理员修改过，保存会覆盖对方的修改。</span>
          <span className={css.spacer} />
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

      <div className={css.editorBar}>
        <span className={css.inline}>
          可见范围
          <TargetPicker data={data} original={original} form={form} disabled={disabled} onChange={edit} />
        </span>
        <span className={css.inline}>
          <label htmlFor={`${ids}-sev`}>级别</label>
          <Select id={`${ids}-sev`} size="sm" className={css.inlineSelect} value={form.severity} disabled={disabled} options={SEVERITIES.map((s) => ({ value: s, label: SEVERITY[s].label }))} onChange={(e) => edit({ severity: e.target.value as Severity })} />
        </span>
        <Switch label="置顶" checked={form.pinned} disabled={disabled} onChange={(e) => edit({ pinned: e.target.checked })} />
      </div>
      <div className={css.editorBar}>
        <span className={css.inline}>
          <label htmlFor={`${ids}-pub`}>定时发布</label>
          <Input
            id={`${ids}-pub`}
            type="datetime-local"
            size="sm"
            className={css.timeInput}
            value={form.publishAt}
            disabled={disabled || actions.scheduleLocked}
            title={status === 'published' ? '已发布的公告不能改发布时间' : undefined}
            onChange={(e) => edit({ publishAt: e.target.value })}
          />
        </span>
        <span className={css.inline}>
          <label htmlFor={`${ids}-exp`}>自动下线</label>
          <Input id={`${ids}-exp`} type="datetime-local" size="sm" className={css.timeInput} value={form.expiresAt} disabled={disabled} onChange={(e) => edit({ expiresAt: e.target.value })} />
        </span>
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

      <div className={css.editorBar}>
        <span className={css.faint}>{locked ? '' : actions.scheduleLocked ? '已发布，修改保存后门户立即更新' : '定时发布留空即立即发布；自动下线留空则一直显示'}</span>
        <span className={css.spacer} />
        {locked && original && (
          <Button variant="primary" onClick={() => onCopy({ ...annFormFrom(original), publishAt: '', expiresAt: '' })}>
            复制为新公告
          </Button>
        )}
        {actions.withdraw && (
          <Button variant="danger" disabled={busy} busy={withdraw.isPending} onClick={() => setWithdrawing(true)}>
            撤回
          </Button>
        )}
        {actions.draft && (
          <Button disabled={busy} busy={save.isPending && save.variables?.publish === false} onClick={() => submit(false)}>
            保存草稿
          </Button>
        )}
        {actions.primary && (
          <Button variant="primary" disabled={busy} busy={save.isPending && save.variables?.publish === true} onClick={() => submit(true)}>
            {actions.primary}
          </Button>
        )}
      </div>

      <ConfirmModal
        open={withdrawing}
        title={status === 'scheduled' ? '取消定时并撤回？' : '撤回公告？'}
        body={
          status === 'scheduled'
            ? '这条公告不会再发布，撤回后也不能再编辑，只能复制为新公告。只想改时间的话，直接修改定时发布时间或存回草稿。'
            : '门户立即隐藏这条公告，已推送出去的通知不会撤回。撤回后不能再编辑或重新发布，只能复制为新公告。'
        }
        confirmLabel="撤回"
        tone="danger"
        onConfirm={() => {
          if (original) withdraw.mutate(original)
          setWithdrawing(false)
        }}
        onCancel={() => setWithdrawing(false)}
      />
    </section>
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
