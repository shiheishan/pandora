/**
 * [INPUT]: 依赖 react 的 useState，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../core/api 的 isApiError，依赖 ../../../core/router 的 navigate，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./logic 的 sameIds / ruleSummary / sourceLabel / sourceTone，依赖 ./queries、./schemas，依赖 ./nodes.module.css 与 ./infra.module.css
 * [OUTPUT]: 对外提供 NodeGroupMembership（节点所属路由组的勾选与保存）与 EffectivePreview（节点生效路由的只读预览）
 * [POS]: admin/screens/nodes 抽屉「路由」标签里与路由组（00096）有关的两块，被 NodeRouting 摆在私有路由编辑的上下：所属组按组的生效顺序列出、可勾选调整（PUT v1/nodes/{id}/route-groups 带节点 row_version，与单节点路由 PUT 同级，只要发布权限；退出组时本节点仍有规则指向该组出站会 409 原文提示）；预览取 GET v1/nodes/{id}/routing/effective，与下发给节点的同一口径，规则按匹配顺序、出站显示胜出的那条，每条标注来源（本节点 / 路由组 · 名称 / 全局）
 */
import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Checkbox, QueryView, Tag, useToast } from '../../../ui'
import { ruleSummary, sameIds, sourceLabel, sourceTone } from './logic'
import x from './infra.module.css'
import css from './nodes.module.css'
import { useCan, useEffectiveRouting, useFailure, useInvalidateNodes, useRouteGroups } from './queries'
import { routingSaved, type Route, type RouteGroupRef } from './schemas'

export function NodeGroupMembership({ nodeId, rowVersion, current }: { nodeId: string; rowVersion: number; current: readonly RouteGroupRef[] }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const groups = useRouteGroups()
  const initial = current.map((g) => g.id)
  const [picked, setPicked] = useState<string[]>(initial)
  const [error, setError] = useState<string | null>(null)
  const writable = can('node.config.publish')
  const toggle = (id: string) => setPicked(picked.includes(id) ? picked.filter((p) => p !== id) : [...picked, id])

  const save = useMutation({
    mutationFn: () => api.put(`v1/nodes/${nodeId}/route-groups`, routingSaved, { body: { row_version: rowVersion, group_ids: picked } }),
    onSuccess: () => {
      toast('所属路由组已保存，已通知节点')
      void invalidate()
    },
    onError: (e) => {
      if (isApiError(e, 'conflict') && e.fields.row_version) {
        toast('节点已被其他人修改，已刷新到最新', 'danger')
        void invalidate()
        return
      }
      if (isApiError(e, 'conflict') || (isApiError(e) && e.status === 422)) return setError(Object.values(e.fields).join('；') || e.message)
      fail(e)
    },
  })

  return (
    <section>
      <div className={css.sectionHead}>
        <h4 className={css.sectionTitle}>所属路由组</h4>
        <span className={css.faint}>组的规则排在本节点规则之后、全局规则之前，多个组按组的排序依次生效</span>
      </div>
      <QueryView query={groups} rows={1} isEmpty={(d) => d.length === 0} empty={<span className={css.faint}>还没有路由组。到「路由」标签建组后可在这里加入。</span>}>
        {(data) => (
          <div className={css.stack}>
            <div className={x.groupChecks}>
              {data.map((g) => (
                <Checkbox key={g.id} label={`${g.name}（排序 ${g.sort_order}）`} checked={picked.includes(g.id)} disabled={!writable || save.isPending} onChange={() => toggle(g.id)} />
              ))}
            </div>
            {error && <div className={css.errorBox}>{error}</div>}
            {writable && (
              <div className={css.formActions}>
                <Button size="sm" disabled={sameIds(picked, initial)} busy={save.isPending} onClick={() => (setError(null), save.mutate())}>
                  保存所属组
                </Button>
                <Button size="sm" variant="ghost" onClick={() => navigate('/nodes/routing')}>
                  管理路由组
                </Button>
              </div>
            )}
          </div>
        )}
      </QueryView>
    </section>
  )
}

export function EffectivePreview({ nodeId }: { nodeId: string }) {
  const preview = useEffectiveRouting(nodeId)
  return (
    <section>
      <div className={css.sectionHead}>
        <h4 className={css.sectionTitle}>生效结果预览</h4>
        <span className={css.faint}>已保存的配置合并后下发给节点的样子（只读，只含启用的规则）</span>
      </div>
      <QueryView query={preview} rows={3} isEmpty={() => false} empty={null}>
        {(data) => (
          <div className={css.stack}>
            <div className={css.faint}>所在路由组：{data.groups.length ? data.groups.map((g) => g.name).join(' → ') : '无'}</div>
            {data.routes.length === 0 ? (
              <span className={css.faint}>没有生效规则，节点保持默认直出。</span>
            ) : (
              <div>
                {data.routes.map((r, i) => (
                  <div key={i} className={x.builtin}>
                    <span className={`${css.mono} ${css.faint}`}>{i + 1}</span>
                    <span className={x.builtinName}>{ruleSummary(routeToRule(r.matcher, r.outbound))}</span>
                    <span className={css.mono}>→ {r.outbound}</span>
                    <Tag tone={sourceTone(r.source)}>{sourceLabel(r.source)}</Tag>
                  </div>
                ))}
              </div>
            )}
            {data.outbounds.length > 0 && (
              <div>
                {data.outbounds.map((o) => (
                  <div key={o.tag} className={x.builtin}>
                    <span className={`${x.builtinName} ${css.mono}`}>{o.tag}</span>
                    <span className={`${css.mono} ${css.faint}`}>{o.type}</span>
                    <Tag tone={sourceTone(o.source)}>{sourceLabel(o.source)}</Tag>
                  </div>
                ))}
              </div>
            )}
          </div>
        )}
      </QueryView>
    </section>
  )
}

/** 预览里的规则只有 matcher 与出站，补成 Route 形状好复用 ruleSummary */
const routeToRule = (matcher: Record<string, unknown>, outbound: string): Route => ({ priority: 0, matcher, outbound_tag: outbound, enabled: true, note: '' })
