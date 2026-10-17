import { Add01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { useMutation } from '@tanstack/react-query'
import { useId, useState } from 'react'
import { Row, RowList } from '../../kit'
import { useApi } from '../../../shell/runtime'
import { Button, Card, ConfirmModal, Empty, Input, QueryView, Select, Skeleton, Switch, Tag, TextArea, useToast } from '../../../ui'
import { droppedMessage, slotDirty, timezoneOptions } from './logic'
import { useFailure, useIntentKey, useInvalidateContent, useSiteSettings, useSlots, useThemes } from './queries'
import { siteSettingsSchema, slotSaved, themeActivated, themeDeleted, type Slot, type Theme } from './schemas'
import { activateNotice, themeSwatches, themeTagline, type ThemeTarget } from './theme'
import { ThemeEditor } from './ThemeEditor'
import css from './theme.module.css'

export function ThemeTab() {
  const themes = useThemes()
  const slots = useSlots()
  const [editing, setEditing] = useState<ThemeTarget | null>(null)
  const [activating, setActivating] = useState<Theme | null>(null)
  const [deleting, setDeleting] = useState<Theme | null>(null)

  return (
    <div className={css.stack}>
      <Card title="站点设置">
        <SiteCard />
      </Card>

      <Card
        flush
        title="主题"
        subtitle="用户端同一时间只用一套，明暗两组随用户切换"
        extra={
          <Button size="sm" icon={Add01Icon} onClick={() => setEditing({ kind: 'create', from: null })}>
            新建
          </Button>
        }
      >
        <QueryView query={themes} rows={2} empty={<Empty bare title="还没有主题" />}>
          {(list) => (
            <RowList label="主题列表">
              {list.map((t) => (
                <ThemeRow
                  key={t.id}
                  theme={t}
                  onCopy={() => setEditing({ kind: 'create', from: t })}
                  onEdit={() => setEditing({ kind: 'edit', theme: t })}
                  onActivate={() => setActivating(t)}
                  onDelete={() => setDeleting(t)}
                />
              ))}
            </RowList>
          )}
        </QueryView>
      </Card>

      <Card
        flush
        title="前端插槽"
        subtitle="在用户端固定位置插入文本或 HTML；失焦自动保存，脚本、样式与事件属性会被过滤"
      >
        <QueryView
          query={slots}
          rows={4}
          empty={<Empty bare title="没有可用的插槽位" description="插槽位由用户端代码定义，升级后会出现在这里。" />}
        >
          {(list) => (
            <div className={css.slots}>
              {list.map((s) => (
                <SlotRow key={s.key} slot={s} />
              ))}
            </div>
          )}
        </QueryView>
      </Card>

      <ThemeEditor target={editing} onClose={() => setEditing(null)} />
      <ThemeConfirms activating={activating} deleting={deleting} onDone={() => (setActivating(null), setDeleting(null))} />
    </div>
  )
}

// ---------------------------------------------------------------------------
// 主题列表的一行：色块、名称与标语、激活 / 复制 / 编辑 / 删除
// ---------------------------------------------------------------------------
function ThemeRow({
  theme: t,
  onCopy,
  onEdit,
  onActivate,
  onDelete,
}: {
  theme: Theme
  onCopy: () => void
  onEdit: () => void
  onActivate: () => void
  onDelete: () => void
}) {
  const swatches = themeSwatches(t)
  const tagline = themeTagline(t)
  const meta = [
    tagline ? `标语「${tagline}」` : '没有标语',
    t.is_active ? '用户端所有用户看到的都是这一套' : '',
  ]
    .filter(Boolean)
    .join(' · ')

  return (
    <Row
      lead={
        <span className={css.sws} aria-hidden="true">
          {swatches.map((c, i) => (
            <span key={i} className={css.swsSwatch} style={{ background: c }} />
          ))}
        </span>
      }
      title={
        <span className={css.themeTitleWrap}>
          <span>{t.name}</span>
          <span className={css.themeCode}>{t.code}</span>
          {t.is_builtin && <Tag tone="neutral">内置</Tag>}
          {t.is_active && <Tag tone="ok">使用中</Tag>}
        </span>
      }
      meta={meta}
      actions={
        <>
          {!t.is_active && (
            <button type="button" className={css.linkBtn} onClick={onActivate}>
              激活
            </button>
          )}
          <button type="button" className={css.linkBtn} onClick={onCopy}>
            复制
          </button>
          {!t.is_builtin && (
            <button type="button" className={`${css.linkBtn} ${css.hideM}`} onClick={onEdit}>
              编辑
            </button>
          )}
          {!t.is_builtin && !t.is_active && (
            <button type="button" className={`${css.linkDanger} ${css.hideM}`} onClick={onDelete}>
              删除
            </button>
          )}
        </>
      }
    />
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
        onConfirm={() => run(() => api.post(`v1/themes/${encodeURIComponent(activating!.code)}/activate`, themeActivated), '主题已激活，用户端刷新后生效')}
        onCancel={onDone}
      />
      <ConfirmModal
        open={deleting !== null}
        title={deleting ? `删除「${deleting.name}」？` : ''}
        body="删除后不能恢复。内置主题与正在使用的主题不能删除。"
        confirmLabel="删除主题"
        tone="danger"
        onConfirm={() => run(() => api.delete(`v1/themes/${encodeURIComponent(deleting!.code)}`, themeDeleted), `已删除「${deleting!.name}」`)}
        onCancel={onDone}
      />
    </>
  )
}

// ---------------------------------------------------------------------------
// 站点设置：站点名 + 时区，只提交改了的键
// ---------------------------------------------------------------------------
function SiteCard() {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateContent()
  const site = useSiteSettings()
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

  if (site.isPending) return <Skeleton height={32} />
  if (site.isError)
    return (
      <QueryView query={site} empty={null}>
        {() => null}
      </QueryView>
    )
  return (
    <>
      <div className={css.siteRow}>
        <Input
          label="站点名"
          value={nameValue}
          maxLength={40}
          error={errors.name}
          disabled={save.isPending}
          onChange={(e) => {
            setName(e.target.value)
            if (errors.name) setErrors((prev) => ({ ...prev, name: '' }))
          }}
        />
        <Select
          label="时区"
          value={tzValue}
          error={errors.timezone}
          disabled={save.isPending}
          options={timezoneOptions(current?.timezone)}
          onChange={(e) => {
            setPicked(e.target.value)
            if (errors.timezone) setErrors((prev) => ({ ...prev, timezone: '' }))
          }}
        />
        <Button className={css.siteSave} size="sm" disabled={!dirty} busy={save.isPending} onClick={submit}>
          保存
        </Button>
      </div>
      <p className={css.hint}>
        站点名用在浏览器标题、邮件和订阅配置名里；用户端用量图按天统计、后台收入趋势都按这个时区切日；修改只影响之后的数据，已记下的按日用量不重算。
      </p>
    </>
  )
}

// ---------------------------------------------------------------------------
// 插槽：开关切换即保存，文本失焦且真的变了才保存（每次保存都要 reauth 与新幂等键）
// ---------------------------------------------------------------------------
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
    <div className={css.slot}>
      <div className={css.slotMeta}>
        <label className={css.slotName} htmlFor={`${id}-c`}>
          {slot.label}
        </label>
        <div className={css.slotKey}>{slot.key}</div>
        <Switch
          className={css.slotSwl}
          aria-label={`启用「${slot.label}」`}
          label={slot.enabled ? '已开启' : '已关闭'}
          checked={slot.enabled}
          disabled={save.isPending}
          onChange={(e) => save.mutate({ content, enabled: e.target.checked })}
        />
      </div>
      <TextArea
        id={`${id}-c`}
        className={css.slotArea}
        rows={3}
        mono
        value={content}
        placeholder="留空则不显示"
        disabled={save.isPending}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={() => {
          if (slotDirty(draft, slot)) save.mutate({ content, enabled: slot.enabled })
        }}
      />
    </div>
  )
}
