/**
 * [INPUT]: 依赖 react 的 useState，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../core/api 的 isApiError，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./logic 的规则行与出站行纯函数，依赖 ./NodeRouting 的 RuleRows，依赖 ./queries、./schemas，依赖 ./nodes.module.css 与 ./infra.module.css
 * [OUTPUT]: 对外提供 RoutingTab（节点与服务器 · 路由标签）
 * [POS]: admin/screens/nodes 的全局出站与分流（设计稿 t_routing；契约 GET / PUT v1/nodes/routing，R56）：左栏规则（复用 RuleRows，下拉只放后端支持的匹配类型，D-D-1；新规则插在兜底之前），右栏出站（内置 direct / block + 自定义出站列表，新增 / 编辑用弹窗，契约待补·前端；被规则引用的先拦，改名时规则跟着改），底部「发布到全部节点」：确认框「N 条规则会下发到 M 个在线节点」，一次 PUT 带 expected_revision、reauth、幂等 node_routing_global_publish；revision 冲突刷新、删除仍被节点私有规则引用的出站回 409 并列出节点。编辑是本地的，发布才生效
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Input, Modal, QueryView, Select, Tag, TextArea, useToast } from '../../../ui'
import { OUTBOUND_TYPES, outboundToRow, renameOutbound, routeToRow, rowsToOutbounds, rowsToRoutes, rulesUsing, type OutboundRow, type RuleRow } from './logic'
import { RuleRows } from './NodeRouting'
import x from './infra.module.css'
import css from './nodes.module.css'
import { endsIntent, useCan, useFailure, useGlobalRouting, useIntentKey, useInvalidateNodes } from './queries'
import { globalRoutingSaved, type GlobalRouting } from './schemas'

export function RoutingTab() {
  const routing = useGlobalRouting()
  return (
    <QueryView query={routing} rows={6} isEmpty={() => false} empty={null}>
      {(data) => <RoutingEditor key={data.revision} data={data} />}
    </QueryView>
  )
}

interface Editing {
  /** null = 新增 */
  index: number | null
  row: OutboundRow
  error: string | null
}

function RoutingEditor({ data }: { data: GlobalRouting }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const intent = useIntentKey()
  const writable = can('node.config.publish')
  const [initial] = useState(() => ({ rules: data.routes.map(routeToRow), outbounds: data.outbounds.map(outboundToRow) }))
  const [rules, setRules] = useState<RuleRow[]>(initial.rules)
  const [outbounds, setOutbounds] = useState<OutboundRow[]>(initial.outbounds)
  const [ruleErrors, setRuleErrors] = useState<Record<number, string>>({})
  const [outErrors, setOutErrors] = useState<Record<number, string>>({})
  const [formError, setFormError] = useState<string | null>(null)
  const [editing, setEditing] = useState<Editing | null>(null)
  const [confirming, setConfirming] = useState<Record<string, unknown> | null>(null)
  const dirty = JSON.stringify({ rules, outbounds }) !== JSON.stringify(initial)

  const tags: Array<[string, string]> = [['direct', '直连 direct'], ['block', '拦截 block'], ...outbounds.filter((o) => o.tag.trim()).map((o): [string, string] => [o.tag.trim(), `${o.tag.trim()} · ${o.type}`])]

  const publish = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.put('v1/nodes/routing', globalRoutingSaved, { body, idempotencyKey: intent.keyFor(body) }),
    onSuccess: (r) => {
      intent.reset()
      toast(`路由已发布，${r.affected_nodes} 个节点会拉到新配置`)
      void invalidate()
    },
    onError: (error) => {
      if (endsIntent(error)) intent.reset()
      if (isApiError(error, 'conflict') && error.fields.expected_revision) {
        toast('全局路由已被其他管理员修改，已刷新到最新（这次的修改没有发布）', 'danger')
        void invalidate()
        return
      }
      if (isApiError(error, 'conflict') || (isApiError(error) && error.status === 422)) return setFormError(Object.values(error.fields).join('；') || error.message)
      fail(error)
    },
  })

  const submit = () => {
    setFormError(null)
    const r = rowsToRoutes(rules)
    const o = rowsToOutbounds(outbounds)
    setRuleErrors(r.errors)
    setOutErrors(o.errors)
    if (Object.keys(r.errors).length || Object.keys(o.errors).length) return
    setConfirming({ expected_revision: data.revision, outbounds: o.outbounds, routes: r.routes })
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
    <div className={css.stack}>
      <div className={x.routing}>
        <section className={x.routingPanel} aria-label="分流规则">
          <div className={x.panelHead}>
            <h3 className={x.panelTitle}>分流规则</h3>
            <span className={css.faint}>自上而下匹配，命中即停；节点私有规则排在这些规则之前，节点私有的兜底规则会遮住全部全局规则</span>
          </div>
          <div className={x.panelBody}>
            <RuleRows rows={rules} errors={ruleErrors} outbounds={tags} disabled={!writable || publish.isPending} onChange={setRules} emptyHint="没有全局规则。加的规则会下发到全部节点。" />
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
                        <Button size="xs" variant="ghost" disabled={publish.isPending} aria-label={`编辑出站 ${o.tag}`} onClick={() => setEditing({ index: i, row: o, error: null })}>
                          编辑
                        </Button>
                        <Button size="xs" variant="ghost" disabled={publish.isPending} aria-label={`移除出站 ${o.tag}`} onClick={() => removeOutbound(i)}>
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
                <Button size="sm" disabled={publish.isPending} onClick={() => setEditing({ index: null, row: { tag: '', type: 'trojan', settings: '{}' }, error: null })}>
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
                <Button variant="primary" className={x.publishButton} busy={publish.isPending} onClick={submit}>
                  发布到全部节点
                </Button>
              </>
            ) : (
              <div className={css.faint}>当前账号只能查看，发布需要配置发布权限。</div>
            )}
          </div>
        </section>
      </div>

      <ConfirmModal
        open={confirming !== null}
        title="发布路由到全部节点？"
        body={`${(confirming?.routes as unknown[] | undefined)?.length ?? 0} 条规则会下发到 ${data.online_nodes} 个在线节点；离线节点上线后拉到同一份。需要重新验证身份。`}
        confirmLabel="发布"
        onConfirm={() => {
          if (confirming) publish.mutate(confirming)
          setConfirming(null)
        }}
        onCancel={() => setConfirming(null)}
      />
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
