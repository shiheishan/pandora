/**
 * [INPUT]: 依赖 react 的 useId / useState，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./logic 的主题 / 时区 / 插槽函数，依赖 ./queries，依赖 ./schemas，依赖 ./content.module.css
 * [OUTPUT]: 对外提供 ThemeTab（内容与外观 · 主题与插槽标签）
 * [POS]: admin/screens/content 的主题与插槽（设计稿 t_theme）。按 5.A D-D-4 / D-E-4 只画生效的「默认 · 纸白」一张卡片（使用中，内置不可删不可停），「保存新主题」入口隐藏、custom_css 不出现；卡片旁补站点时区卡（R49，设计稿没有，按卡片风格补：读权限同邮件设置 security.audit.read，没有就不画；改要 platform.settings.write + reauth，无幂等）。
 *        前端插槽按接口返回的 7 个位（名称、key、位置说明）：失焦时内容真的变了才保存、开关带上当前内容（reauth + 每次新幂等键 appearance_slot_save），保存后回填净化后的内容，被过滤的标签逐条提示
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useId, useState, type CSSProperties } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Empty, QueryView, Select, Skeleton, Switch, Tag, TextArea, useToast } from '../../../ui'
import css from './content.module.css'
import { droppedMessage, dropsIntent, siteName, slotDirty, themePreview, timezoneOptions } from './logic'
import { useCan, useFailure, useIntentKey, useInvalidateContent, useSiteSettings, useSlots, useThemes } from './queries'
import { siteSettingsSchema, slotSaved, type Slot, type Theme } from './schemas'

export function ThemeTab() {
  const can = useCan()
  const themes = useThemes()
  const slots = useSlots()
  const writable = can('platform.appearance.write')

  return (
    <div className={css.stack}>
      <div className={css.themeGrid}>
        <QueryView query={themes} rows={2} isEmpty={(list) => !list.some((t) => t.is_active)} empty={<Empty bare title="没有生效的主题" description="门户按内置默认样式显示；主题由系统迁移维护，这里无需操作。" />}>
          {(list) => list.filter((t) => t.is_active).map((t) => <ThemeCard key={t.id} theme={t} />)}
        </QueryView>
        {can('security.audit.read') && <TimezoneCard writable={can('platform.settings.write')} />}
      </div>

      <section className={css.slots} aria-label="前端插槽">
        <div className={css.slotsHead}>
          <span className={css.cardTitle}>前端插槽</span>
          <span className={css.faint}>在门户固定位置插入文本或 HTML 片段；失焦自动保存，脚本、样式与事件属性会被过滤</span>
        </div>
        <QueryView query={slots} rows={4} empty={<Empty bare title="没有可用的插槽位" description="插槽位由门户代码定义，升级后会出现在这里。" />}>
          {(list) => list.map((s) => <SlotRow key={s.key} slot={s} writable={writable} />)}
        </QueryView>
      </section>
    </div>
  )
}

function ThemeCard({ theme }: { theme: Theme }) {
  const p = themePreview(theme)
  const vars = { '--pv-bg': p.bg, '--pv-fg': p.fg, '--pv-accent': p.accent } as CSSProperties
  const name = siteName(theme)
  return (
    <section className={css.themeCard} style={vars} aria-label={`主题 ${theme.name}`}>
      <div className={css.preview} aria-hidden="true">
        <div className={css.previewLine} />
        <div className={css.previewLine} />
        <div className={css.previewButton} />
      </div>
      <div className={css.cardBody}>
        <div className={css.row}>
          <span className={css.cardName}>{theme.name}</span>
          {theme.is_builtin && <Tag tone="neutral">内置</Tag>}
          <Tag tone="ok">使用中</Tag>
        </div>
        <div className={css.swatches} aria-hidden="true">
          <span className={`${css.swatch} ${css.swatchBg}`} />
          <span className={`${css.swatch} ${css.swatchFg}`} />
          <span className={`${css.swatch} ${css.swatchAccent}`} />
        </div>
        <div className={css.faint}>{name ? `站点名称「${name}」，` : ''}明暗两套配色随用户的主题切换，门户所有用户看到的都是这一套。</div>
      </div>
    </section>
  )
}

function TimezoneCard({ writable }: { writable: boolean }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateContent()
  const site = useSiteSettings(true)
  const id = useId()
  const [picked, setPicked] = useState<string | null>(null)
  const current = site.data?.timezone
  const value = picked ?? current ?? ''

  const save = useMutation({
    mutationFn: (timezone: string) => api.post('v1/settings/site', siteSettingsSchema, { body: { timezone } }),
    onSuccess: (r) => {
      toast(`站点时区已改为 ${r.timezone}`)
      setPicked(null)
      void invalidate('site')
    },
    onError: (error) => fail(error),
  })

  return (
    <section className={css.tzCard} aria-label="站点时区">
      <label htmlFor={`${id}-s`} className={css.cardTitle}>
        站点时区
      </label>
      {site.isPending ? (
        <Skeleton height={32} />
      ) : site.isError ? (
        <QueryView query={site} empty={null}>
          {() => null}
        </QueryView>
      ) : (
        <Select id={`${id}-s`} value={value} disabled={!writable || save.isPending} options={timezoneOptions(current)} onChange={(e) => setPicked(e.target.value)} />
      )}
      <div className={css.faint}>门户用量图按天统计、后台收入趋势都按这个时区切日；修改只影响之后的数据，已记下的按日用量不重算。</div>
      {writable ? (
        <Button variant="primary" size="sm" disabled={!picked || picked === current} busy={save.isPending} onClick={() => picked && save.mutate(picked)}>
          保存
        </Button>
      ) : (
        <div className={css.faint}>修改需要「站点设置」权限。</div>
      )}
    </section>
  )
}

function SlotRow({ slot, writable }: { slot: Slot; writable: boolean }) {
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
      if (dropsIntent(error)) intent.reset()
      fail(error)
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
          readOnly={!writable}
          disabled={save.isPending}
          hint={slot.updated_at ? `更新于 ${slot.updated_at}` : undefined}
          onChange={(e) => setDraft(e.target.value)}
          onBlur={() => {
            if (writable && slotDirty(draft, slot)) save.mutate({ content, enabled: slot.enabled })
          }}
        />
      </div>
      <Switch className={css.slotSwitch} aria-label={`启用「${slot.label}」`} checked={slot.enabled} disabled={!writable || save.isPending} onChange={(e) => save.mutate({ content, enabled: e.target.checked })} />
    </div>
  )
}
