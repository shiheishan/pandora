/**
 * [INPUT]: 依赖 react 的 useId / useState，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../core/api 的 isApiError，依赖 ../../../core/router 的 navigate，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./logic 的知识库函数，依赖 ./queries，依赖 ./schemas，依赖 ./content.module.css
 * [OUTPUT]: 对外提供 KbEditor（知识库文章编辑区）
 * [POS]: admin/screens/content 知识库的中栏：标题 + 分类（已有分类 + 自定义输入）、Markdown 正文，设计缺、契约待补·前端的新文章标识（slug）与「发布设置」折叠区（类型、摘要、语言、可见性、平台、客户端版本范围、限定套餐、复审日期）补齐。
 *        每次保存都是 POST v1/content-pages 生成新版本（reauth + 幂等 content_page_version_create，expected_latest_version = 当前最大版本）：「保存为 v{n}」发布、「保存草稿」留草稿；受众字段原样沿用上一版，改了会提示旧发布版不会被自动归档。
 *        「归档」对最新的已发布版本调用（reauth + 幂等 content_page_archive），「恢复」把已归档的最新版内容再发布成新版本。别人先保存出新版本时不覆盖输入，提示后可载入最新
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useId, useState } from 'react'
import { isApiError } from '../../../core/api'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Checkbox, ConfirmModal, Input, Menu, Select, TextArea, useToast, type MenuEntry } from '../../../ui'
import css from './content.module.css'
import { audienceChanged, dropsIntent, emptyKbForm, KIND_LABEL, kbBody, kbFormFrom, PLATFORM_LABEL, validateKb, VISIBILITY_LABEL, type Article, type KbBody, type KbForm } from './logic'
import { useCan, useFailure, useIntentKey, useInvalidateContent, usePlanCatalog } from './queries'
import { KINDS, pageArchived, pageSaved, PLATFORMS, VISIBILITIES, type Kind, type Page, type Visibility } from './schemas'

interface Props {
  /** null = 新文章 */
  article: Article | null
  /** article 最新版本的完整内容（含正文）；新文章为 null */
  page: Page | null
  kind: Kind
  categories: readonly string[]
  takenSlugs: readonly string[]
}

const same = (a: KbForm, b: KbForm) => JSON.stringify(a) === JSON.stringify(b)

export function KbEditor({ article, page, kind, categories, takenSlugs }: Props) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const can = useCan()
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
    mutationFn: ({ body }: { body: KbBody; restore?: boolean }) => api.post('v1/content-pages', pageSaved, { body, idempotencyKey: intent.keyFor([body.slug, body]) }),
    onSuccess: (r, { restore }) => {
      intent.reset()
      // 恢复存的是已归档那一版而不是表单；本地若有未保存的修改，留着并按「有新版本」提示
      if (!restore) setBase(form)
      toast(restore ? `已恢复为 v${r.page.version}` : r.page.status === 'published' ? `已保存为 v${r.page.version}` : `草稿 v${r.page.version} 已保存`)
      void invalidate('pages').then(() => !article && navigate(`/content/kb/${r.page.slug}`, { replace: true }))
    },
    onError: (error) => {
      if (dropsIntent(error)) intent.reset()
      if (isApiError(error, 'conflict')) void invalidate('pages')
      fail(error, setErrors)
    },
  })

  const archive = useMutation({
    mutationFn: (p: Page) => api.post(`v1/content-pages/${p.id}/archive`, pageArchived, { body: { expected_version: p.version }, idempotencyKey: intent.keyFor([p.id, 'archive', p.version]) }),
    onSuccess: (r) => {
      intent.reset()
      toast(r.page.already_archived ? '这个版本已经归档过了' : '已归档，门户不再展示')
      void invalidate('pages')
    },
    onError: (error) => {
      if (dropsIntent(error)) intent.reset()
      if (isApiError(error, 'conflict')) void invalidate('pages')
      fail(error)
    },
  })

  const submit = (status: 'draft' | 'published') => {
    const errs = validateKb(form, article ? [] : takenSlugs)
    setErrors(errs)
    if (Object.keys(errs).length === 0) save.mutate({ body: kbBody(form, status, latestVersion, page) })
  }

  const restore = () => {
    if (page) save.mutate({ body: kbBody(kbFormFrom(page), 'published', latestVersion, page), restore: true })
  }

  const busy = save.isPending || archive.isPending
  const canPlans = can('catalog.read')
  const catalog = usePlanCatalog(canPlans)
  const planName = (id: string) => catalog.data?.find((p) => p.id === id)?.name ?? (catalog.isPending ? '…' : '不可用套餐')
  const changedAudience = page !== null && audienceChanged(form, page)

  return (
    <section className={css.panel} aria-label={article ? `编辑文章 ${article.latest.title}` : '新文章'}>
      {stale && page && (
        <div className={`${css.banner} ${css.warnBanner} ${css.row}`}>
          <span>这篇文章刚有了新版本 v{page.version}，保存会在它之上再生成一个版本。</span>
          <span className={css.spacer} />
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
      {!article && (
        <Input
          label="标识"
          mono
          value={form.slug}
          error={errors.slug}
          hint="小写字母、数字与单个连字符，如 clash-verge-rev；门户链接用它，保存后不能改"
          disabled={busy}
          onChange={(e) => edit({ slug: e.target.value })}
        />
      )}
      <div className={css.titleRow}>
        <Input aria-label="文章标题" className={css.kbTitle} value={form.title} placeholder="文章标题" error={errors.title} disabled={busy} onChange={(e) => edit({ title: e.target.value })} />
        <Input aria-label="分类" list={`${ids}-cats`} className={css.kbTitle} value={form.category} placeholder="分类" error={errors.category} disabled={busy} onChange={(e) => edit({ category: e.target.value })} />
        <datalist id={`${ids}-cats`}>
          {categories.map((c) => (
            <option key={c} value={c} />
          ))}
        </datalist>
      </div>
      <TextArea aria-label="文章正文" mono rows={14} value={form.body} placeholder="正文，支持 Markdown，至少 10 个字" error={errors.body} disabled={busy} onChange={(e) => edit({ body: e.target.value })} />

      <details className={css.settings} open={!article || undefined}>
        <summary>发布设置</summary>
        <div className={css.settingsBody}>
          <Select label="类型" value={form.kind} disabled={busy} options={KINDS.map((k) => ({ value: k, label: KIND_LABEL[k] }))} onChange={(e) => edit({ kind: e.target.value as Kind })} />
          <Select label="可见性" value={form.visibility} disabled={busy} options={VISIBILITIES.map((v) => ({ value: v, label: VISIBILITY_LABEL[v] }))} onChange={(e) => edit({ visibility: e.target.value as Visibility })} />
          <TextArea label="摘要（可选）" fieldClassName={css.full} rows={2} value={form.summary} error={errors.summary} disabled={busy} onChange={(e) => edit({ summary: e.target.value })} />
          <Input label="语言" mono value={form.locale} error={errors.locale} disabled={busy} onChange={(e) => edit({ locale: e.target.value })} />
          <Input label="复审日期（可选）" type="date" value={form.reviewDue} disabled={busy} onChange={(e) => edit({ reviewDue: e.target.value })} />
          <Input label="最低客户端版本" mono value={form.minClient} placeholder="不限" error={errors.min_client_version} disabled={busy} onChange={(e) => edit({ minClient: e.target.value })} />
          <Input label="最高客户端版本" mono value={form.maxClient} placeholder="不限" error={errors.max_client_version} disabled={busy} onChange={(e) => edit({ maxClient: e.target.value })} />
          <div className={css.full}>
            <div className={css.groupLabel}>平台（不选 = 全部平台）</div>
            <div className={css.checks}>
              {PLATFORMS.map((p) => (
                <Checkbox
                  key={p}
                  label={PLATFORM_LABEL[p]}
                  checked={form.platforms.includes(p)}
                  disabled={busy}
                  onChange={(e) => edit({ platforms: e.target.checked ? [...form.platforms, p] : form.platforms.filter((x) => x !== p) })}
                />
              ))}
            </div>
          </div>
          <div className={css.full}>
            <div className={css.groupLabel}>限定套餐（不选 = 所有登录用户）</div>
            <PlanPicker planIds={form.planIds} plans={canPlans ? (catalog.data ?? []) : null} planName={planName} disabled={busy} onChange={(planIds) => edit({ planIds })} />
          </div>
          {changedAudience && <div className={`${css.full} ${css.banner} ${css.warnBanner}`}>受众（语言、平台、客户端版本、限定套餐或可见性）和上一版不同：保存后上一版的发布内容不会被自动归档，两版会同时对各自的用户可见；不需要旧版时请在版本历史里查看并归档。</div>}
        </div>
      </details>

      <div className={css.faint}>每次保存生成新版本，门户展示最新发布的版本</div>
      <div className={css.editorBar}>
        <span className={css.spacer} />
        {article?.published && (
          <Button variant="danger" disabled={busy} busy={archive.isPending} onClick={() => setArchiving(true)}>
            归档
          </Button>
        )}
        {article?.archived && (
          <Button disabled={busy} busy={save.isPending && save.variables?.restore === true} onClick={restore}>
            恢复
          </Button>
        )}
        <Button disabled={busy} busy={save.isPending && save.variables?.body.status === 'draft'} onClick={() => submit('draft')}>
          保存草稿
        </Button>
        <Button variant="primary" disabled={busy} busy={save.isPending && save.variables?.body.status === 'published' && !save.variables.restore} onClick={() => submit('published')}>
          保存为 v{latestVersion + 1}
        </Button>
      </div>

      <ConfirmModal
        open={archiving}
        title={`归档 v${article?.published?.version ?? ''}？`}
        body="门户不再展示这篇文章（如果另有面向其他受众的已发布版本，那一版照常展示）。之后可以点「恢复」重新发布成新版本。"
        confirmLabel="归档"
        tone="danger"
        onConfirm={() => {
          if (article?.published) archive.mutate(article.published)
          setArchiving(false)
        }}
        onCancel={() => setArchiving(false)}
      />
    </section>
  )
}

/** 限定套餐：有 catalog.read 时是开关菜单；没有时只列出沿用的 id 数，保存时原样带上 */
function PlanPicker({ planIds, plans, planName, disabled, onChange }: { planIds: string[]; plans: ReadonlyArray<{ id: string; name: string; status: string }> | null; planName: (id: string) => string; disabled: boolean; onChange: (ids: string[]) => void }) {
  const label = planIds.length === 0 ? '所有登录用户' : plans ? planIds.map(planName).join('、') : `已限定 ${planIds.length} 个套餐`
  if (!plans) return <span className={css.faint}>{planIds.length === 0 ? '不限套餐（没有套餐读取权限，无法在这里选择）' : `${label}（没有套餐读取权限，保存时原样沿用）`}</span>
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
