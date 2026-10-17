import { PlusSignIcon } from '@hugeicons-pro/core-stroke-rounded'
import { useState, type ReactNode } from 'react'
import { Button, Input, Select, TextArea, useToast } from '../../../ui'
import { OUTBOUND_TYPES, outboundToRow, routeToRow, rowsToOutbounds, rowsToRoutes, rulesUsing, type OutboundRow, type RuleRow } from './logic'
import { AddRuleButton, RuleRows } from './NodeRouting'
import css from './routing.module.css'
import n from './nodes.module.css'
import type { Outbound, Route } from './schemas'
import { outboundAddress } from './routing'

export interface RoutingPayload {
  outbounds: Array<{ tag: string; type: string; settings: unknown }>
  routes: ReturnType<typeof rowsToRoutes>['routes']
}

const emptyDraft = (): OutboundRow => ({ tag: '', type: 'trojan', settings: '' })

export function ScopeRoutingEditor({
  outbounds: initialOutbounds,
  routes: initialRoutes,
  references = [],
  ruleHint,
  emptyHint,
  busy,
  formError,
  publishLabel,
  actions,
  onSubmit,
}: {
  outbounds: readonly Outbound[]
  routes: readonly Route[]
  /** 其他范围可以引用的出站（[tag, 显示名]），如路由组规则可以指向全局出站 */
  references?: ReadonlyArray<readonly [string, string]>
  ruleHint: ReactNode
  emptyHint: string
  busy: boolean
  formError: string | null
  publishLabel: string
  /** 发布行最右侧的按钮（路由组的「改名」「删除路由组」） */
  actions?: ReactNode
  onSubmit: (payload: RoutingPayload) => void
}) {
  const toast = useToast()
  const [initial] = useState(() => ({ rules: initialRoutes.map(routeToRow), outbounds: initialOutbounds.map(outboundToRow) }))
  const [rules, setRules] = useState<RuleRow[]>(initial.rules)
  const [outbounds, setOutbounds] = useState<OutboundRow[]>(initial.outbounds)
  const [ruleErrors, setRuleErrors] = useState<Record<number, string>>({})
  const [outErrors, setOutErrors] = useState<Record<number, string>>({})
  const [adding, setAdding] = useState(false)
  const [draft, setDraft] = useState<OutboundRow>(emptyDraft)
  const [draftError, setDraftError] = useState<string | null>(null)
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

  /** 就地表单提交：新行拼到最后，用 rowsToOutbounds 校验那一行，没错才收起 */
  const addOutbound = () => {
    const next = [...outbounds, draft]
    const at = next.length - 1
    const error = rowsToOutbounds(next).errors[at]
    if (error) return setDraftError(error)
    setOutbounds(next.map((o, j) => (j === at ? { ...o, tag: o.tag.trim() } : o)))
    setOutErrors({})
    setDraft(emptyDraft())
    setDraftError(null)
    setAdding(false)
  }

  /** 出站行的地址列：settings 是 JSON 文本，先解析；解析失败按没有地址显示 */
  const addressOf = (o: OutboundRow) => {
    try {
      return outboundAddress(JSON.parse(o.settings))
    } catch {
      return '—'
    }
  }

  return (
    <div className={css.page}>
      <section className={css.section} aria-label="出站">
        <div className={css.sectionHead}>
          <h3 className={css.sectionTitle}>出站</h3>
          <Button size="sm" icon={PlusSignIcon} disabled={busy} onClick={() => (setDraft(emptyDraft()), setDraftError(null), setAdding(true))}>
            添加出站
          </Button>
        </div>
        <div className={css.rows}>
          <div className={css.row}>
            <span className={css.tag} title="direct">direct</span>
            <span className={css.type}>直连</span>
            <span className={css.address}>—</span>
            <span className={css.builtinText}>内置</span>
          </div>
          <div className={css.row}>
            <span className={css.tag} title="block">block</span>
            <span className={css.type}>拦截</span>
            <span className={css.address}>—</span>
            <span className={css.builtinText}>内置</span>
          </div>
          {outbounds.map((o, i) => {
            const referred = rulesUsing(rules, o.tag.trim()) > 0
            return (
              <div key={i}>
                <div className={css.row}>
                  <span className={css.tag} title={o.tag}>{o.tag}</span>
                  <span className={css.type}>{o.type}</span>
                  <span className={css.address}>{addressOf(o)}</span>
                  <Button
                    size="xs"
                    variant="ghost"
                    disabled={busy}
                    className={referred ? css.referred : undefined}
                    aria-label={`删除出站 ${o.tag}`}
                    onClick={() => removeOutbound(i)}
                  >
                    删除
                  </Button>
                </div>
                {outErrors[i] && <div className={n.errorBox}>{outErrors[i]}</div>}
              </div>
            )
          })}
          {adding && (
            <div className={css.addForm}>
              <Input label="标签" mono value={draft.tag} error={draftError?.startsWith('settings') ? undefined : (draftError ?? undefined)} placeholder="如 US-LAX-01" onChange={(e) => setDraft({ ...draft, tag: e.target.value })} />
              <Select label="类型" value={draft.type} onChange={(e) => setDraft({ ...draft, type: e.target.value })} options={OUTBOUND_TYPES.map((t) => ({ value: t, label: t }))} />
              <div className={css.addSettings}>
                <TextArea
                  label="settings（JSON）"
                  mono
                  rows={4}
                  value={draft.settings}
                  error={draftError?.startsWith('settings') ? draftError : undefined}
                  fieldClassName={n.span2}
                  placeholder='{"server": "proxy.example.com", "server_port": 443}'
                  onChange={(e) => setDraft({ ...draft, settings: e.target.value })}
                />
              </div>
              <div className={css.addActions}>
                <Button size="sm" onClick={addOutbound}>添加</Button>
                <Button size="sm" variant="ghost" onClick={() => (setAdding(false), setDraftError(null))}>取消</Button>
              </div>
            </div>
          )}
        </div>
      </section>

      <section className={css.section} aria-label="规则">
        <div className={css.sectionHead}>
          <h3 className={css.sectionTitle}>规则</h3>
          <AddRuleButton rows={rules} disabled={busy} onChange={setRules} />
        </div>
        <p className={css.sectionHint}>{ruleHint}</p>
        <RuleRows rows={rules} errors={ruleErrors} outbounds={tags} disabled={busy} onChange={setRules} emptyHint={emptyHint} inlineAdd={false} />
      </section>

      {formError && <div className={n.errorBox}>{formError}</div>}
      <div className={css.publishRow}>
        <Button variant="primary" busy={busy} disabled={!dirty} onClick={submit}>
          {publishLabel}
        </Button>
        {dirty && <span className={css.dirtyNote}>有未发布的修改</span>}
        <span className={css.spacer} />
        {actions}
      </div>
    </div>
  )
}
