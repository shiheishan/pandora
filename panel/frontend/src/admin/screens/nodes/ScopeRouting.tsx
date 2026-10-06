import { useState, type ReactNode } from 'react'
import { Button, Input, Modal, Select, Tag, TextArea, useToast } from '../../../ui'
import { OUTBOUND_TYPES, outboundToRow, renameOutbound, routeToRow, rowsToOutbounds, rowsToRoutes, rulesUsing, type OutboundRow, type RuleRow } from './logic'
import { RuleRows } from './NodeRouting'
import x from './infra.module.css'
import css from './nodes.module.css'
import type { Outbound, Route } from './schemas'

export interface RoutingPayload {
  outbounds: Array<{ tag: string; type: string; settings: unknown }>
  routes: ReturnType<typeof rowsToRoutes>['routes']
}

interface Editing {
  /** null = 新增 */
  index: number | null
  row: OutboundRow
  error: string | null
}

export function ScopeRoutingEditor({
  outbounds: initialOutbounds,
  routes: initialRoutes,
  references = [],
  ruleHint,
  emptyHint,
  writable,
  busy,
  formError,
  publishLabel,
  readOnlyHint,
  onSubmit,
}: {
  outbounds: readonly Outbound[]
  routes: readonly Route[]
  /** 其他范围可以引用的出站（[tag, 显示名]），如路由组规则可以指向全局出站 */
  references?: ReadonlyArray<readonly [string, string]>
  ruleHint: ReactNode
  emptyHint: string
  writable: boolean
  busy: boolean
  formError: string | null
  publishLabel: string
  readOnlyHint: string
  onSubmit: (payload: RoutingPayload) => void
}) {
  const toast = useToast()
  const [initial] = useState(() => ({ rules: initialRoutes.map(routeToRow), outbounds: initialOutbounds.map(outboundToRow) }))
  const [rules, setRules] = useState<RuleRow[]>(initial.rules)
  const [outbounds, setOutbounds] = useState<OutboundRow[]>(initial.outbounds)
  const [ruleErrors, setRuleErrors] = useState<Record<number, string>>({})
  const [outErrors, setOutErrors] = useState<Record<number, string>>({})
  const [editing, setEditing] = useState<Editing | null>(null)
  const dirty = JSON.stringify({ rules, outbounds }) !== JSON.stringify(initial)

  const own = outbounds.filter((o) => o.tag.trim()).map((o): [string, string] => [o.tag.trim(), `${o.tag.trim()} · ${o.type}`])
  const ownKeys = new Set(own.map(([t]) => t))
  const tags: Array<readonly [string, string]> = [['direct', '直连 direct'], ['block', '拦截 block'], ...own, ...references.filter(([t]) => !ownKeys.has(t))]

  const submit = () => {
    const r = rowsToRoutes(rules)
    const o = rowsToOutbounds(outbounds)
    setRuleErrors(r.errors)
    setOutErrors(o.errors)
    if (Object.keys(r.errors).length || Object.keys(o.errors).length) return
    onSubmit({ outbounds: o.outbounds, routes: r.routes })
  }

  const removeOutbound = (i: number) => {
    const tag = outbounds[i]!.tag
    const used = rulesUsing(rules, tag)
    if (used) return toast(`还有 ${used} 条规则指向「${tag}」，先把这些规则改到别的出站`, 'danger')
    setOutbounds(outbounds.filter((_, j) => j !== i))
    setOutErrors({})
  }

  const saveOutbound = (e: Editing) => {
    const next = e.index === null ? [...outbounds, e.row] : outbounds.map((o, j) => (j === e.index ? e.row : o))
    const at = e.index ?? next.length - 1
    const error = rowsToOutbounds(next).errors[at]
    if (error) return setEditing({ ...e, error })
    if (e.index !== null) setRules(renameOutbound(rules, outbounds[e.index]!.tag, e.row.tag))
    setOutbounds(next.map((o, j) => (j === at ? { ...o, tag: o.tag.trim() } : o)))
    setOutErrors({})
    setEditing(null)
  }

  return (
    <div className={x.routing}>
      <section className={x.routingPanel} aria-label="分流规则">
        <div className={x.panelHead}>
          <h3 className={x.panelTitle}>分流规则</h3>
          <span className={css.faint}>{ruleHint}</span>
        </div>
        <div className={x.panelBody}>
          <RuleRows rows={rules} errors={ruleErrors} outbounds={tags} disabled={!writable || busy} onChange={setRules} emptyHint={emptyHint} />
        </div>
      </section>

      <section className={x.routingPanel} aria-label="出站">
        <div className={x.panelHead}>
          <h3 className={x.panelTitle}>出站</h3>
        </div>
        <div className={x.panelBody}>
          <div>
            <div className={x.builtin}>
              <span className={x.builtinName}>直连</span>
              <span className={`${css.mono} ${css.faint}`}>direct</span>
              <Tag tone="neutral">内置</Tag>
            </div>
            <div className={x.builtin}>
              <span className={x.builtinName}>拦截</span>
              <span className={`${css.mono} ${css.faint}`}>block</span>
              <Tag tone="neutral">内置</Tag>
            </div>
            {outbounds.map((o, i) => (
              <div key={i}>
                <div className={x.builtin}>
                  <span className={`${x.builtinName} ${css.mono}`} title={o.tag}>
                    {o.tag}
                  </span>
                  <span className={`${css.mono} ${css.faint}`}>{o.type}</span>
                  {writable && (
                    <>
                      <Button size="xs" variant="ghost" disabled={busy} aria-label={`编辑出站 ${o.tag}`} onClick={() => setEditing({ index: i, row: o, error: null })}>
                        编辑
                      </Button>
                      <Button size="xs" variant="ghost" disabled={busy} aria-label={`移除出站 ${o.tag}`} onClick={() => removeOutbound(i)}>
                        移除
                      </Button>
                    </>
                  )}
                </div>
                {outErrors[i] && <div className={x.outErr}>{outErrors[i]}</div>}
              </div>
            ))}
          </div>
          {writable && (
            <div>
              <Button size="sm" disabled={busy} onClick={() => setEditing({ index: null, row: { tag: '', type: 'trojan', settings: '{}' }, error: null })}>
                ＋ 添加出站
              </Button>
            </div>
          )}
        </div>
        <div className={x.publish}>
          {formError && <div className={`${css.errorBox} ${x.dirty}`}>{formError}</div>}
          {writable ? (
            <>
              {dirty && <div className={x.dirty}>有未发布的修改，发布后才会下发。</div>}
              <Button variant="primary" className={x.publishButton} busy={busy} onClick={submit}>
                {publishLabel}
              </Button>
            </>
          ) : (
            <div className={css.faint}>{readOnlyHint}</div>
          )}
        </div>
      </section>
      {editing && <OutboundModal editing={editing} onChange={setEditing} onSave={saveOutbound} onClose={() => setEditing(null)} />}
    </div>
  )
}

function OutboundModal({ editing, onChange, onSave, onClose }: { editing: Editing; onChange: (e: Editing) => void; onSave: (e: Editing) => void; onClose: () => void }) {
  const row = editing.row
  const settingsError = editing.error?.startsWith('settings') ? editing.error : undefined
  const set = (patch: Partial<OutboundRow>) => onChange({ ...editing, row: { ...row, ...patch }, error: null })
  return (
    <Modal
      open
      onClose={onClose}
      size="md"
      title={editing.index === null ? '添加出站' : `编辑出站 ${row.tag}`}
      eyebrow="在本地修改，发布后生效"
      actions={
        <>
          <Button size="dialog" variant="ghost" onClick={onClose}>
            取消
          </Button>
          <Button size="dialog" variant="primary" onClick={() => onSave(editing)}>
            {editing.index === null ? '添加' : '完成'}
          </Button>
        </>
      }
    >
      <div className={css.stack}>
        <div className={css.grid2}>
          <Input label="标签" mono value={row.tag} error={settingsError ? undefined : (editing.error ?? undefined)} onChange={(e) => set({ tag: e.target.value })} placeholder="如 US-LAX-01" />
          <Select label="类型" value={row.type} onChange={(e) => set({ type: e.target.value })} options={OUTBOUND_TYPES.map((t) => ({ value: t, label: t }))} />
          <TextArea label="settings（JSON）" mono rows={6} value={row.settings} error={settingsError} fieldClassName={css.span2} onChange={(e) => set({ settings: e.target.value })} />
        </div>
        <div className={css.faint}>改标签时，指向它的规则会一起改过去。</div>
      </div>
    </Modal>
  )
}
