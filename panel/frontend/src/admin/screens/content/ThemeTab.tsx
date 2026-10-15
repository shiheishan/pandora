import { useMutation } from '@tanstack/react-query'
import { useId, useState, type CSSProperties } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Empty, Input, QueryView, Select, Skeleton, Switch, Tag, TextArea, useToast } from '../../../ui'
import css from './content.module.css'
import { droppedMessage, slotDirty, timezoneOptions } from './logic'
import { useFailure, useIntentKey, useInvalidateContent, useSiteSettings, useSlots, useThemes } from './queries'
import { siteSettingsSchema, slotSaved, themeActivated, themeDeleted, type Slot, type Theme } from './schemas'
import { activateNotice, themeSwatches, themeTagline, type ThemeTarget } from './theme'
import { ThemeEditor } from './ThemeEditor'
import theme from './theme.module.css'

const SITE_NAME_INPUT_ID = 'site-settings-name'

export function ThemeTab() {
  const themes = useThemes()
  const slots = useSlots()
  const [editing, setEditing] = useState<ThemeTarget | null>(null)
  const [activating, setActivating] = useState<Theme | null>(null)
  const [deleting, setDeleting] = useState<Theme | null>(null)
  const list = themes.data ?? []
  const active = list.find((t) => t.is_active)

  // 主题编辑器里的「去改站点名」：关掉抽屉，把焦点交给站点设置卡的站点名输入框
  const editSiteName = () => {
    setEditing(null)
    requestAnimationFrame(() => {
      const el = document.getElementById(SITE_NAME_INPUT_ID)
      el?.scrollIntoView({ block: 'center' })
      el?.focus()
    })
  }

  return (
    <div className={css.stack}>
      <div className={css.themeGrid}>
        {/* 列表末尾总有「新建主题」卡，所以没有主题时也不显示空状态 */}
        <QueryView query={themes} rows={2} isEmpty={() => false} empty={null}>
          {(l) => (
            <>
              {l.map((t) => (
                <ThemeCard key={t.id} theme={t} onEdit={() => setEditing(t.is_builtin ? { kind: 'create', from: t } : { kind: 'edit', theme: t })} onActivate={() => setActivating(t)} onDelete={() => setDeleting(t)} />
              ))}
              <button type="button" className={theme.newCard} onClick={() => setEditing({ kind: 'create', from: active ?? null })}>
                ＋ 新建主题{active ? `（复制「${active.name}」）` : ''}
              </button>
            </>
          )}
        </QueryView>
        <SiteCard />
      </div>

      <section className={css.slots} aria-label="前端插槽">
        <div className={css.slotsHead}>
          <span className={css.cardTitle}>前端插槽</span>
          <span className={css.faint}>在门户固定位置插入文本或 HTML 片段；失焦自动保存，脚本、样式与事件属性会被过滤</span>
        </div>
        <QueryView query={slots} rows={4} empty={<Empty bare title="没有可用的插槽位" description="插槽位由门户代码定义，升级后会出现在这里。" />}>
          {(list) => list.map((s) => <SlotRow key={s.key} slot={s} />)}
        </QueryView>
      </section>

      <ThemeEditor target={editing} onClose={() => setEditing(null)} onEditSiteName={editSiteName} />
      <ThemeConfirms activating={activating} deleting={deleting} onDone={() => (setActivating(null), setDeleting(null))} />
    </div>
  )
}

function ThemeCard({ theme: t, onEdit, onActivate, onDelete }: { theme: Theme; onEdit: () => void; onActivate: () => void; onDelete: () => void }) {
  const p = themeSwatches(t)
  const vars = { '--pv-bg': p.bg, '--pv-fg': p.fg, '--pv-accent': p.accent } as CSSProperties
  const tagline = themeTagline(t)
  return (
    <section className={css.themeCard} style={vars} aria-label={`主题 ${t.name}`}>
      <div className={css.preview} aria-hidden="true">
        <div className={css.previewLine} />
        <div className={css.previewLine} />
        <div className={css.previewButton} />
      </div>
      <div className={css.cardBody}>
        <div className={css.row}>
          <span className={css.cardName}>{t.name}</span>
          {t.is_builtin && <Tag tone="neutral">内置</Tag>}
          {t.is_active && <Tag tone="ok">使用中</Tag>}
        </div>
        <div className={css.swatches} aria-hidden="true">
          <span className={`${css.swatch} ${css.swatchBg}`} />
          <span className={`${css.swatch} ${css.swatchFg}`} />
          <span className={`${css.swatch} ${css.swatchAccent}`} />
        </div>
        <div className={css.faint}>
          <span className={css.mono}>{t.code}</span>
          {tagline ? ` · 标语「${tagline}」` : ''}
          {t.is_active ? '。门户所有用户看到的都是这一套，明暗两组随用户切换。' : ''}
        </div>
        <div className={theme.actions}>
          {t.is_builtin ? (
            <Button size="xs" onClick={onEdit}>
              另存为
            </Button>
          ) : (
            <Button size="xs" onClick={onEdit}>
              编辑
            </Button>
          )}
          {!t.is_active && (
            <Button size="xs" variant="primary" onClick={onActivate}>
              激活
            </Button>
          )}
          {!t.is_builtin && !t.is_active && (
            <Button size="xs" variant="ghost" onClick={onDelete}>
              删除
            </Button>
          )}
        </div>
      </div>
    </section>
  )
}

/** 激活与删除：都要 reauth（api 层弹框），不带幂等键（契约：路由上没有幂等中间件） */
function ThemeConfirms({ activating, deleting, onDone }: { activating: Theme | null; deleting: Theme | null; onDone: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateContent()
  const run = async (work: () => Promise<unknown>, ok: string) => {
    try {
      await work()
      toast(ok)
    } catch (e) {
      fail(e)
    } finally {
      void invalidate('themes')
      onDone()
    }
  }
  return (
    <>
      <ConfirmModal
        open={activating !== null}
        title={activating ? `激活「${activating.name}」？` : ''}
        body={activating ? activateNotice(activating) : ''}
        confirmLabel="激活主题"
        onConfirm={() => run(() => api.post(`v1/themes/${encodeURIComponent(activating!.code)}/activate`, themeActivated), `已激活「${activating!.name}」，门户刷新后生效`)}
        onCancel={onDone}
      />
      <ConfirmModal
        open={deleting !== null}
        title={deleting ? `删除「${deleting.name}」？` : ''}
        body="删除后无法恢复；正在使用的主题和内置主题不能删除。"
        confirmLabel="删除主题"
        tone="danger"
        onConfirm={() => run(() => api.delete(`v1/themes/${encodeURIComponent(deleting!.code)}`, themeDeleted), `已删除「${deleting!.name}」`)}
        onCancel={onDone}
      />
    </>
  )
}

function SiteCard() {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateContent()
  const site = useSiteSettings()
  const id = useId()
  const [name, setName] = useState<string | null>(null)
  const [picked, setPicked] = useState<string | null>(null)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const current = site.data
  const nameValue = name ?? current?.name ?? ''
  const tzValue = picked ?? current?.timezone ?? ''

  const dirty = (name !== null && name.trim() !== current?.name) || (picked !== null && picked !== current?.timezone)

  const save = useMutation({
    mutationFn: (body: { name?: string; timezone?: string }) => api.post('v1/settings/site', siteSettingsSchema, { body }),
    onSuccess: (r, vars) => {
      if (vars.name !== undefined && vars.timezone !== undefined) {
        toast('站点设置已保存')
      } else if (vars.name !== undefined) {
        toast(`站点名已改为「${r.name}」`)
      } else if (vars.timezone !== undefined) {
        toast(`站点时区已改为 ${r.timezone}`)
      }
      setName(null)
      setPicked(null)
      setErrors({})
      void invalidate('site')
    },
    onError: (error) => fail(error, { fields: setErrors }),
  })

  const submit = () => {
    const nameTrimmed = nameValue.trim()
    const nameChanged = name !== null && name.trim() !== current?.name
    const tzChanged = picked !== null && picked !== current?.timezone

    const errs: Record<string, string> = {}
    if (nameChanged) {
      if (!nameTrimmed) {
        errs.name = '站点名必填'
      } else if ([...nameTrimmed].length > 40) {
        errs.name = '站点名最多 40 个字'
      }
    }
    if (Object.keys(errs).length > 0) {
      setErrors((prev) => ({ ...prev, ...errs }))
      return
    }

    const body: { name?: string; timezone?: string } = {}
    if (nameChanged) body.name = nameTrimmed
    if (tzChanged) body.timezone = picked!
    save.mutate(body)
  }

  return (
    <section className={css.tzCard} aria-label="站点设置">
      <span className={css.cardTitle}>站点设置</span>
      {site.isPending ? (
        <Skeleton height={32} />
      ) : site.isError ? (
        <QueryView query={site} empty={null}>
          {() => null}
        </QueryView>
      ) : (
        <>
          <Input
            id={SITE_NAME_INPUT_ID}
            label="站点名"
            value={nameValue}
            maxLength={40}
            error={errors.name}
            hint="门户标题、浏览器标签、邮件里的站点名与发件人名、订阅配置名都用它；主题只管标语与 Logo。"
            disabled={save.isPending}
            onChange={(e) => {
              setName(e.target.value)
              if (errors.name) setErrors((prev) => ({ ...prev, name: '' }))
            }}
          />
          <label htmlFor={`${id}-s`} className={css.cardTitle}>
            站点时区
          </label>
          <Select
            id={`${id}-s`}
            value={tzValue}
            error={errors.timezone}
            disabled={save.isPending}
            options={timezoneOptions(current?.timezone)}
            onChange={(e) => {
              setPicked(e.target.value)
              if (errors.timezone) setErrors((prev) => ({ ...prev, timezone: '' }))
            }}
          />
        </>
      )}
      <div className={css.faint}>门户用量图按天统计、后台收入趋势都按这个时区切日；修改只影响之后的数据，已记下的按日用量不重算。</div>
      <Button variant="primary" size="sm" disabled={!dirty} busy={save.isPending} onClick={submit}>
        保存
      </Button>
    </section>
  )
}

function SlotRow({ slot }: { slot: Slot }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateContent()
  const id = useId()
  // 未改动时为 undefined，显示服务端（净化后）的内容
  const [draft, setDraft] = useState<string | undefined>(undefined)
  const content = draft ?? slot.content

  const save = useMutation({
    mutationFn: (body: { content: string; enabled: boolean }) => api.post(`v1/slots/${encodeURIComponent(slot.key)}`, slotSaved, { body, idempotencyKey: intent.keyFor([slot.key, body]) }),
    onSuccess: (r, body) => {
      intent.reset()
      const dropped = droppedMessage(r.dropped)
      toast(dropped ?? (body.enabled !== slot.enabled ? (body.enabled ? `「${slot.label}」已开启` : `「${slot.label}」已关闭`) : '插槽已保存'), dropped ? 'danger' : 'ok')
      void invalidate('slots').then(() => setDraft(undefined))
    },
    onError: (error) => {
      fail(error, { intent })
    },
  })

  return (
    <div className={css.slotRow}>
      <div>
        <label className={css.slotName} htmlFor={`${id}-c`}>
          {slot.label}
        </label>
        <div className={css.slotKey}>{slot.key}</div>
        <div className={css.slotWhere}>{slot.where}</div>
      </div>
      <div>
        <TextArea
          id={`${id}-c`}
          rows={2}
          className={css.slotArea}
          value={content}
          disabled={save.isPending}
          hint={slot.updated_at ? `更新于 ${slot.updated_at}` : undefined}
          onChange={(e) => setDraft(e.target.value)}
          onBlur={() => {
            if (slotDirty(draft, slot)) save.mutate({ content, enabled: slot.enabled })
          }}
        />
      </div>
      <Switch className={css.slotSwitch} aria-label={`启用「${slot.label}」`} checked={slot.enabled} disabled={save.isPending} onChange={(e) => save.mutate({ content, enabled: e.target.checked })} />
    </div>
  )
}
