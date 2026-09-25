/**
 * [INPUT]: 依赖 react 的 useState，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../core/api 的 isApiError，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./logic 的规则行互转，依赖 ./queries、./schemas，依赖 ./nodes.module.css
 * [OUTPUT]: 对外提供 NodeRouting（节点抽屉「路由」标签）与 RuleRows（规则行编辑器，全局路由标签 RoutingTab 复用）
 * [POS]: admin/screens/nodes 抽屉的单节点路由（契约待补·前端：后端有、设计缺）：GET / PUT v1/nodes/{id}/routing，全量替换本节点私有出站与规则；规则可以指向 direct / block、本节点私有出站与全局出站（R26，大小写不敏感）；匹配类型只放后端支持的（D-D-1），兜底必须是最后一条启用规则，新规则插在末尾兜底之前；出站行的校验与互转在 logic。保存要 node.config.publish，成功后后端通知节点
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { useApi } from '../../../shell/runtime'
import { Button, Checkbox, Empty, IconClose, Input, QueryView, Select, TextArea, useToast } from '../../../ui'
import { MATCH_KINDS, OUTBOUND_TYPES, insertRule, outboundToRow, routeToRow, rowsToOutbounds, rowsToRoutes, type MatchKind, type OutboundRow, type RuleRow } from './logic'
import css from './nodes.module.css'
import { useCan, useFailure, useGlobalRouting, useInvalidateNodes, useNodeRouting } from './queries'
import { routingSaved, type NodeRouting as Routing } from './schemas'

export function NodeRouting({ nodeId }: { nodeId: string }) {
  const routing = useNodeRouting(nodeId)
  return (
    <QueryView query={routing} rows={3} isEmpty={() => false} empty={null}>
      {(data) => <RoutingEditor key={data.row_version} nodeId={nodeId} data={data} />}
    </QueryView>
  )
}

function RoutingEditor({ nodeId, data }: { nodeId: string; data: Routing }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const global = useGlobalRouting()
  const writable = can('node.config.publish')
  const [rules, setRules] = useState<RuleRow[]>(() => data.routes.map(routeToRow))
  const [outbounds, setOutbounds] = useState<OutboundRow[]>(() => data.outbounds.map(outboundToRow))
  const [ruleErrors, setRuleErrors] = useState<Record<number, string>>({})
  const [outErrors, setOutErrors] = useState<Record<number, string>>({})
  const [formError, setFormError] = useState<string | null>(null)

  const tags = [
    ['direct', '直连 direct'],
    ['block', '拦截 block'],
    ...outbounds.filter((o) => o.tag.trim()).map((o) => [o.tag.trim(), `${o.tag.trim()}（本节点）`]),
    ...(global.data?.outbounds ?? []).map((o) => [o.tag, `${o.tag}（全局）`]),
  ] as Array<[string, string]>

  const save = useMutation({
    mutationFn: (body: object) => api.put(`v1/nodes/${nodeId}/routing`, routingSaved, { body }),
    onSuccess: () => {
      toast('单节点路由已保存，已通知节点')
      void invalidate()
    },
    onError: (error) => {
      if (isApiError(error) && error.status === 422) return setFormError(Object.values(error.fields).join('；') || error.message)
      if (isApiError(error, 'conflict')) {
        toast('节点已被其他人修改，已刷新到最新', 'danger')
        void invalidate()
        return
      }
      fail(error)
    },
  })

  const submit = () => {
    setFormError(null)
    const { routes, errors } = rowsToRoutes(rules)
    const parsed = rowsToOutbounds(outbounds)
    setRuleErrors(errors)
    setOutErrors(parsed.errors)
    if (Object.keys(errors).length || Object.keys(parsed.errors).length) return
    save.mutate({ row_version: data.row_version, outbounds: parsed.outbounds, routes })
  }

  return (
    <div className={css.stackLg}>
      <section>
        <div className={css.sectionHead}>
          <h4 className={css.sectionTitle}>分流规则</h4>
          <span className={css.faint}>本节点的规则排在全局规则之前，自上而下匹配，命中即停；这里的兜底规则会遮住全部全局规则</span>
        </div>
        <RuleRows rows={rules} errors={ruleErrors} outbounds={tags} disabled={!writable || save.isPending} onChange={setRules} emptyHint="沿用全局规则。加一条规则只影响这个节点。" />
      </section>

      <section>
        <div className={css.sectionHead}>
          <h4 className={css.sectionTitle}>本节点私有出站</h4>
          <span className={css.faint}>全局出站在「路由」标签统一管理，这里可以直接引用</span>
        </div>
        <OutboundRows rows={outbounds} errors={outErrors} disabled={!writable || save.isPending} onChange={setOutbounds} emptyHint="没有私有出站。" />
      </section>

      {formError && <div className={css.errorBox}>{formError}</div>}
      {writable ? (
        <div className={css.formActions}>
          <Button variant="primary" busy={save.isPending} onClick={submit}>
            保存并通知节点
          </Button>
        </div>
      ) : (
        <div className={css.faint}>当前账号只能查看，修改需要配置发布权限。</div>
      )}
    </div>
  )
}

/** 出站行编辑器：标签 + 类型 + settings JSON + 删除（抽屉够宽，逐行平铺；全局路由页右栏窄，改用列表 + 弹窗） */
function OutboundRows({ rows, errors, disabled, onChange, emptyHint }: { rows: OutboundRow[]; errors: Record<number, string>; disabled: boolean; onChange: (rows: OutboundRow[]) => void; emptyHint: string }) {
  const update = (i: number, patch: Partial<OutboundRow>) => onChange(rows.map((x, j) => (j === i ? { ...x, ...patch } : x)))
  return (
    <div className={css.rules}>
      {rows.length === 0 && <div className={css.faint}>{emptyHint}</div>}
      {rows.map((o, i) => (
        <div key={i} className={css.outboundRow}>
          <Input label="标签" mono value={o.tag} disabled={disabled} error={errors[i]} onChange={(e) => update(i, { tag: e.target.value })} />
          <Select label="类型" value={o.type} disabled={disabled} onChange={(e) => update(i, { type: e.target.value })} options={OUTBOUND_TYPES.map((t) => ({ value: t, label: t }))} />
          <TextArea label="settings（JSON）" mono rows={2} value={o.settings} disabled={disabled} fieldClassName={css.span2} onChange={(e) => update(i, { settings: e.target.value })} />
          {!disabled && (
            <Button size="xs" variant="ghost" aria-label={`删除出站 ${o.tag || i + 1}`} onClick={() => onChange(rows.filter((_, j) => j !== i))}>
              <IconClose />
            </Button>
          )}
        </div>
      ))}
      {!disabled && (
        <div>
          <Button size="sm" onClick={() => onChange([...rows, { tag: '', type: 'shadowsocks', settings: '{}' }])}>
            ＋ 添加出站
          </Button>
        </div>
      )}
    </div>
  )
}

/** 规则行编辑器：匹配类型 + 值 + 出站 + 启用 + 备注，上下移与删除 */
export function RuleRows({
  rows,
  errors,
  outbounds,
  disabled,
  onChange,
  emptyHint,
}: {
  rows: RuleRow[]
  errors: Record<number, string>
  outbounds: ReadonlyArray<readonly [string, string]>
  disabled: boolean
  onChange: (rows: RuleRow[]) => void
  emptyHint: string
}) {
  const update = (i: number, patch: Partial<RuleRow>) => onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)))
  const move = (i: number, d: -1 | 1) => {
    const j = i + d
    if (j < 0 || j >= rows.length) return
    const next = [...rows]
    ;[next[i], next[j]] = [next[j]!, next[i]!]
    onChange(next)
  }
  return (
    <div className={css.rules}>
      {rows.length === 0 && <Empty bare title="没有规则" description={emptyHint} />}
      {rows.map((r, i) => (
        <div key={i} className={css.ruleRow}>
          <span className={`${css.mono} ${css.faint} ${css.rNo}`}>{i + 1}</span>
          <Select aria-label="匹配类型" size="sm" fieldClassName={css.rKind} value={r.kind} disabled={disabled} onChange={(e) => update(i, { kind: e.target.value as MatchKind, value: e.target.value === 'fallback' ? '' : r.value })} options={MATCH_KINDS.map(([value, label]) => ({ value, label }))} />
          <Input aria-label="匹配值" size="sm" mono fieldClassName={css.rValue} value={r.value} disabled={disabled || r.kind === 'fallback'} placeholder={r.kind === 'fallback' ? '全部流量' : '多个用逗号分隔'} onChange={(e) => update(i, { value: e.target.value })} />
          <Select aria-label="出站" size="sm" fieldClassName={css.rOut} placeholder="选择出站" value={r.outbound} disabled={disabled} onChange={(e) => update(i, { outbound: e.target.value })} options={outbounds.map(([value, label]) => ({ value, label }))} />
          <Checkbox label="启用" className={css.rOn} checked={r.enabled} disabled={disabled} onChange={(e) => update(i, { enabled: e.target.checked })} />
          <span className={`${css.ruleTools} ${css.rTools}`}>
            <Button size="xs" variant="ghost" aria-label={`第 ${i + 1} 条上移`} disabled={disabled || i === 0} onClick={() => move(i, -1)}>
              ↑
            </Button>
            <Button size="xs" variant="ghost" aria-label={`第 ${i + 1} 条下移`} disabled={disabled || i === rows.length - 1} onClick={() => move(i, 1)}>
              ↓
            </Button>
            <Button size="xs" variant="ghost" aria-label={`删除第 ${i + 1} 条`} disabled={disabled} onClick={() => onChange(rows.filter((_, j) => j !== i))}>
              <IconClose />
            </Button>
          </span>
          <Input aria-label="备注" size="sm" value={r.note} disabled={disabled} placeholder="备注（可选）" onChange={(e) => update(i, { note: e.target.value })} fieldClassName={css.rNote} />
          {errors[i] && <span className={`${css.error} ${css.rErr}`}>{errors[i]}</span>}
        </div>
      ))}
      {!disabled && (
        <div>
          <Button size="sm" onClick={() => onChange(insertRule(rows, { kind: 'domain_suffix', value: '', outbound: 'direct', enabled: true, note: '' }))}>
            ＋ 添加规则
          </Button>
        </div>
      )}
    </div>
  )
}
