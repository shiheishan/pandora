import { useMutation } from '@tanstack/react-query'
import { useId, useState } from 'react'
import { isApiError } from '../../../core/api'
import { formatDateTime } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, IconClose, Input, Menu, Modal, Select, Tag, TextArea, useToast, type MenuEntry } from '../../../ui'
import css from './kb.module.css'
import { audienceChanged, emptyKbForm, kbBody, kbFormFrom, PAGE_STATUS_LABEL, validateKb, VISIBILITY_LABEL, type Article, type KbBody, type KbForm } from './logic'
import { useFailure, useIntentKey, useInvalidateContent, usePlanCatalog } from './queries'
import { pageArchived, pageSaved, VISIBILITIES, type Kind, type Page, type Visibility } from './schemas'

interface Props {
  /** null = 新文章 */
  article: Article | null
  /** article 最新版本的完整内容（含正文）；新文章为 null */
  page: Page | null
  kind: Kind
  categories: readonly string[]
  takenSlugs: readonly string[]
  onClose: () => void
}

const STATUS_TONE = { draft: 'warn', published: 'ok', archived: 'neutral' } as const

const same = (a: KbForm, b: KbForm) => JSON.stringify(a) === JSON.stringify(b)

export function KbEditor({ article, page, kind, categories, takenSlugs, onClose }: Props) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateContent()
  const ids = useId()
  const fresh = () => (page ? kbFormFrom(page) : emptyKbForm(kind))
  const [form, setForm] = useState<KbForm>(fresh)
  const [base, setBase] = useState<KbForm>(fresh)
  const [seen, setSeen] = useState(page?.id ?? '')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [archiving, setArchiving] = useState(false)
  const dirty = !same(form, base)
  if (page && page.id !== seen) {
    setSeen(page.id)
    if (!dirty) {
      setForm(kbFormFrom(page))
      setBase(kbFormFrom(page))
    }
  }
  const stale = page !== null && dirty && !same(base, kbFormFrom(page))
  const latestVersion = article ? (article.latest.latest_version ?? article.latest.version) : 0
  const edit = (patch: Partial<KbForm>) => setForm((f) => ({ ...f, ...patch }))

  const save = useMutation({
    mutationFn: ({ body }: { body: KbBody }) => api.post('v1/content-pages', pageSaved, { body, idempotencyKey: intent.keyFor([body.slug, body]) }),
    onSuccess: (r) => {
      intent.reset()
      setBase(form)
      toast(r.page.status === 'published' ? `已发布 v${r.page.version}` : `草稿 v${r.page.version} 已保存`)
      void invalidate('pages').then(onClose)
    },
    onError: (error) => {
      if (isApiError(error, 'conflict')) void invalidate('pages')
      fail(error, { fields: setErrors, intent })
    },
  })

  const archive = useMutation({
    mutationFn: (p: Page) => api.post(`v1/content-pages/${p.id}/archive`, pageArchived, { body: { expected_version: p.version }, idempotencyKey: intent.keyFor([p.id, 'archive', p.version]) }),
    onSuccess: (r) => {
      intent.reset()
      toast(r.page.already_archived ? '这个版本已经归档过了' : '已归档，用户端不再展示')
      void invalidate('pages').then(onClose)
    },
    onError: (error) => {
      if (isApiError(error, 'conflict')) void invalidate('pages')
      fail(error, { intent })
    },
  })

  const submit = (status: 'draft' | 'published') => {
    const errs = validateKb(form, article ? [] : takenSlugs)
    setErrors(errs)
    if (Object.keys(errs).length === 0) save.mutate({ body: kbBody(form, status, latestVersion, page) })
  }

  const busy = save.isPending || archive.isPending
  const catalog = usePlanCatalog()
  const planName = (id: string) => catalog.data?.find((p) => p.id === id)?.name ?? (catalog.isPending ? '…' : '不可用套餐')
  const changedAudience = page !== null && audienceChanged(form, page)
  const status = article?.latest.status ?? 'draft'
  const titleLabel = article ? article.latest.title : '新文章'

  return (
    <Modal
      open
      size={720}
      divided
      dismissible={!busy}
      onClose={onClose}
      title={
        <span className={css.headRow}>
          <Tag tone={STATUS_TONE[status]}>{PAGE_STATUS_LABEL[status]}</Tag>
          <span className={css.headMeta}>每次保存生成新版本 · 用户端展示最新发布的版本</span>
        </span>
      }
      headExtra={
        <Button variant="ghost" size="sm" aria-label="关闭" onClick={onClose} disabled={busy}>
          <IconClose />
        </Button>
      }
      footerStart={
        article?.published ? (
          <Button size="dialog" className={css.dangerText} disabled={busy} busy={archive.isPending} onClick={() => setArchiving(true)}>
            归档
          </Button>
        ) : undefined
      }
      actions={
        <>
          <Button size="dialog" disabled={busy} busy={save.isPending && save.variables?.body.status === 'draft'} onClick={() => submit('draft')}>
            存为草稿
          </Button>
          <Button size="dialog" variant="primary" disabled={busy} busy={save.isPending && save.variables?.body.status === 'published'} onClick={() => submit('published')}>
            发布新版本
          </Button>
        </>
      }
    >
      {stale && page && (
        <div className={`${css.banner} ${css.bannerWarn} ${css.bannerRow}`}>
          <span>这篇文章刚有了新版本 v{page.version}，保存会在它之上再生成一个版本。</span>
          <span className={css.bannerSpacer} />
          <Button
            size="xs"
            onClick={() => {
              setForm(kbFormFrom(page))
              setBase(kbFormFrom(page))
            }}
          >
            载入最新
          </Button>
        </div>
      )}

      <Input aria-label="文章标题" className={css.titleInput} value={form.title} placeholder="文章标题" error={errors.title} disabled={busy} onChange={(e) => edit({ title: e.target.value })} />
      <TextArea aria-label="文章正文" mono rows={9} value={form.body} placeholder="正文，支持 Markdown，至少 10 个字" error={errors.body} disabled={busy} onChange={(e) => edit({ body: e.target.value })} />
      <p className={css.hint}>
        可用变量：{'{{siteName}}'} 站点名、{'{{siteUrl}}'} 面板地址、{'{{subscribeUrl}}'} 订阅链接（另有 {'{{urlEncodeSubscribeUrl}}'}、{'{{safeBase64SubscribeUrl}}'}），用户端按每个用户替换。把只给有效订阅用户看的内容写在 {'<!--access start-->'} 与{' '}
        {'<!--access end-->'} 之间，没有有效订阅的用户看到的是一句提示。
      </p>

      <div className={css.fieldGrid}>
        <label className={css.field}>
          <span className={css.fieldLabel}>标识</span>
          {article ? (
            <span className={css.slugInput}>{form.slug}</span>
          ) : (
            <Input size="sm" mono value={form.slug} placeholder="how-to-import" error={errors.slug} disabled={busy} onChange={(e) => edit({ slug: e.target.value })} />
          )}
        </label>
        <label className={css.field}>
          <span className={css.fieldLabel}>分类</span>
          <Input size="sm" list={`${ids}-cats`} value={form.category} placeholder="未分类" error={errors.category} disabled={busy} onChange={(e) => edit({ category: e.target.value })} />
          <datalist id={`${ids}-cats`}>
            {categories.map((c) => (
              <option key={c} value={c} />
            ))}
          </datalist>
        </label>
        <label className={css.field}>
          <span className={css.fieldLabel}>可见性</span>
          <Select size="sm" value={form.visibility} disabled={busy} options={VISIBILITIES.map((v) => ({ value: v, label: VISIBILITY_LABEL[v] }))} onChange={(e) => edit({ visibility: e.target.value as Visibility })} />
        </label>
        <label className={css.field}>
          <span className={css.fieldLabel}>限定套餐</span>
          <PlanPicker planIds={form.planIds} plans={catalog.data ?? []} planName={planName} disabled={busy} onChange={(planIds) => edit({ planIds })} />
        </label>
      </div>

      {changedAudience && (
        <div className={`${css.banner} ${css.bannerWarn}`}>
          受众（限定套餐或可见性）和上一版不同：保存后上一版的发布内容不会被自动归档，两版会同时对各自的用户可见；不需要旧版时先把它归档。
        </div>
      )}

      <div>
        <div className={css.historyHead}>版本历史</div>
        {article ? (
          article.rows.map((r) => {
            const isLatest = r.id === article.latest.id
            return (
              <div key={r.id} className={css.historyRow}>
                <span className={css.historyVer}>v{r.version}</span>
                <Tag tone={STATUS_TONE[r.status]} className={css.historyTag}>
                  {PAGE_STATUS_LABEL[r.status]}{isLatest ? ' · 当前' : ''}
                </Tag>
                <span className={css.historyMeta}>
                  {r.created_by_name ?? '未知作者'} · {formatDateTime(r.created_at).slice(5, 10)}
                </span>
              </div>
            )
          })
        ) : (
          <div className={css.historyEmpty}>保存后在这里看到每个版本。</div>
        )}
      </div>

      <ConfirmModal
        open={archiving}
        title={`归档「${titleLabel}」？`}
        body="用户端不再展示这篇文章（如果另有面向其他受众的已发布版本，那一版照常展示）。版本历史保留，之后可以基于它发布新版本。"
        confirmLabel="归档"
        tone="danger"
        onConfirm={() => {
          if (article?.published) archive.mutate(article.published)
          setArchiving(false)
        }}
        onCancel={() => setArchiving(false)}
      />
    </Modal>
  )
}

/** 限定套餐：开关菜单；目录里已没有的 id 也列出来，保存时原样带上 */
function PlanPicker({ planIds, plans, planName, disabled, onChange }: { planIds: string[]; plans: ReadonlyArray<{ id: string; name: string; status: string }>; planName: (id: string) => string; disabled: boolean; onChange: (ids: string[]) => void }) {
  const label = planIds.length === 0 ? '所有登录用户' : planIds.map(planName).join('、')
  const options = [...plans.map((p) => ({ id: p.id, name: p.status === 'archived' ? `${p.name}（已归档）` : p.name }))]
  for (const id of planIds) if (!options.some((p) => p.id === id)) options.push({ id, name: planName(id) })
  if (disabled) return <span className={css.picker}>{label}</span>
  const entries: MenuEntry[] = [
    { key: 'all', label: '所有登录用户', current: planIds.length === 0, onSelect: () => onChange([]) },
    { kind: 'separator', key: 's' },
    ...options.map((p): MenuEntry => ({ kind: 'toggle', key: p.id, label: p.name, checked: planIds.includes(p.id), onChange: (on) => onChange(on ? [...planIds, p.id] : planIds.filter((x) => x !== p.id)) })),
  ]
  return <Menu label="限定套餐" align="start" triggerLabel={`限定套餐：${label}`} triggerClassName={css.picker} menuClassName={css.pickerMenu} trigger={<span className={css.pickerText}>{label}</span>} entries={entries} />
}
