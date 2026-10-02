/**
 * [INPUT]: 依赖 react 的 useState，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../core/api 的 isApiError，依赖 ../../../core/router 的 navigate，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./logic 的路由组表单 / 成员比较 / 可引用出站 / 节点状态，依赖 ./ScopeRouting 的 ScopeRoutingEditor，依赖 ./queries、./schemas，依赖 ./nodes.module.css 与 ./infra.module.css
 * [OUTPUT]: 对外提供 RouteGroupPanel（路由标签里选中一个组时的内容）与 GroupFormModal（新建 / 编辑组信息，RoutingTab 的「＋ 新建路由组」也用）
 * [POS]: admin/screens/nodes 的路由组（00096）：头部卡片（名称、说明、排序、成员标签点了跳节点抽屉的路由页）带「编辑信息 / 成员 / 删除」，下面是复用 ScopeRoutingEditor 的组内出站与规则（规则还可指向全局出站），「发布到组内 N 个节点」确认后 PUT v1/route-groups/{id}/routing 带 row_version；改信息 PATCH（只带改了的字段）、成员 PUT .../members（勾选未退役节点）、删除 DELETE 带 { row_version }。这些写都影响多个节点，要 reauth（外框对话框接管）与幂等键；新建只要幂等键。行版本冲突刷新到最新，引用冲突 409 原文就地提示
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Checkbox, ConfirmModal, Input, Modal, QueryView, Tag, TextArea, useToast } from '../../../ui'
import { groupCreateBody, groupFormErrors, groupFormFrom, groupPatchBody, nodeState, referenceOutbounds, sameIds, type GroupForm } from './logic'
import { ScopeRoutingEditor, type RoutingPayload } from './ScopeRouting'
import x from './infra.module.css'
import css from './nodes.module.css'
import { endsIntent, useCan, useFailure, useGlobalRouting, useGroupRouting, useIntentKey, useInvalidateNodes, useNodes } from './queries'
import { groupRoutingSaved, routeGroupDeleted, routeGroupSchema, routeGroupUpdated, type GroupRouting, type RouteGroup } from './schemas'

/** 写失败的共同处理：行版本冲突刷新、其余 409 / 422 原文交给调用方就地显示 */
function useGroupFailure(setError: (m: string | null) => void) {
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  return (error: unknown) => {
    if (isApiError(error, 'conflict') && error.fields.row_version) {
      toast('路由组已被其他管理员修改，已刷新到最新（这次的修改没有保存）', 'danger')
      void invalidate()
      return
    }
    if (isApiError(error, 'conflict') || (isApiError(error) && error.status === 422)) return setError(Object.values(error.fields).join('；') || error.message)
    fail(error)
  }
}

export function RouteGroupPanel({ group }: { group: RouteGroup }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const invalidate = useInvalidateNodes()
  const intent = useIntentKey()
  const routing = useGroupRouting(group.id)
  const writable = can('node.config.publish')
  const [editing, setEditing] = useState(false)
  const [members, setMembers] = useState(false)
  const [removing, setRemoving] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)
  const onDeleteError = useGroupFailure(setDeleteError)

  const remove = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.delete(`v1/route-groups/${group.id}`, routeGroupDeleted, { body, idempotencyKey: intent.keyFor(body) }),
    onSuccess: (r) => {
      intent.reset()
      toast(`路由组「${group.name}」已删除，${r.affected_nodes} 个节点会拉到新配置`)
      navigate('/nodes/routing', { replace: true })
      void invalidate()
    },
    onError: (error) => {
      if (endsIntent(error)) intent.reset()
      onDeleteError(error)
    },
  })

  return (
    <div className={css.stack}>
      <section className={x.routingPanel} aria-label="路由组">
        <div className={x.panelHead}>
          <h3 className={x.panelTitle}>{group.name}</h3>
          <Tag tone="neutral">排序 {group.sort_order}</Tag>
          <span className={css.faint}>{group.description || '没有说明'}</span>
        </div>
        <div className={x.panelBody}>
          <div className={x.groupChecks}>
            {group.members.length === 0 ? (
              <span className={css.faint}>还没有成员节点。组内规则只对成员节点生效。</span>
            ) : (
              group.members.map((m) => (
                <Button key={m.id} size="xs" variant="ghost" onClick={() => navigate(`/nodes/nodes/${m.id}/routing`)}>
                  {m.name}
                </Button>
              ))
            )}
          </div>
          {deleteError && <div className={css.errorBox}>{deleteError}</div>}
          {writable && (
            <div className={css.formActions}>
              <Button size="sm" onClick={() => setEditing(true)}>
                编辑信息
              </Button>
              <Button size="sm" onClick={() => setMembers(true)}>
                成员（{group.members.length}）
              </Button>
              <Button size="sm" variant="ghost" busy={remove.isPending} onClick={() => (setDeleteError(null), setRemoving(true))}>
                删除路由组
              </Button>
            </div>
          )}
        </div>
      </section>

      <QueryView query={routing} rows={6} isEmpty={() => false} empty={null}>
        {(data) => <GroupEditor key={data.row_version} group={group} data={data} />}
      </QueryView>

      {editing && <GroupFormModal group={group} onClose={() => setEditing(false)} onSaved={() => setEditing(false)} />}
      {members && <MembersModal group={group} onClose={() => setMembers(false)} />}
      <ConfirmModal
        open={removing}
        title={`删除路由组「${group.name}」？`}
        body={`组内 ${group.outbound_count} 个出站与 ${group.rule_count} 条规则一并删除，${group.members.length} 个成员节点退回只用全局与私有路由并重新发布；成员节点的规则若还指向组内出站会被拒绝。需要重新验证身份。`}
        confirmLabel="删除"
        onConfirm={() => {
          setRemoving(false)
          remove.mutate({ row_version: group.row_version })
        }}
        onCancel={() => setRemoving(false)}
      />
    </div>
  )
}

function GroupEditor({ group, data }: { group: RouteGroup; data: GroupRouting }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const invalidate = useInvalidateNodes()
  const intent = useIntentKey()
  const global = useGlobalRouting()
  const [formError, setFormError] = useState<string | null>(null)
  const [confirming, setConfirming] = useState<Record<string, unknown> | null>(null)
  const onError = useGroupFailure(setFormError)

  const publish = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.put(`v1/route-groups/${group.id}/routing`, groupRoutingSaved, { body, idempotencyKey: intent.keyFor(body) }),
    onSuccess: (r) => {
      intent.reset()
      toast(`路由组已发布，${r.affected_nodes} 个节点会拉到新配置`)
      void invalidate()
    },
    onError: (error) => {
      if (endsIntent(error)) intent.reset()
      onError(error)
    },
  })

  const submit = (p: RoutingPayload) => {
    setFormError(null)
    setConfirming({ row_version: data.row_version, outbounds: p.outbounds, routes: p.routes })
  }

  return (
    <>
      <ScopeRoutingEditor
        outbounds={data.outbounds}
        routes={data.routes}
        references={referenceOutbounds([{ label: '全局', tags: (global.data?.outbounds ?? []).map((o) => o.tag) }])}
        ruleHint="成员节点上排在节点私有规则之后、全局规则之前；同名出站覆盖全局的。规则可以指向本组与全局出站"
        emptyHint="组内没有规则。成员节点只用全局与私有规则。"
        writable={can('node.config.publish')}
        busy={publish.isPending}
        formError={formError}
        publishLabel={`发布到组内 ${group.members.length} 个节点`}
        readOnlyHint="当前账号只能查看，发布需要配置发布权限。"
        onSubmit={submit}
      />
      <ConfirmModal
        open={confirming !== null}
        title={`发布路由组「${group.name}」？`}
        body={`${(confirming?.routes as unknown[] | undefined)?.length ?? 0} 条规则会下发到组内 ${group.members.length} 个节点；离线节点上线后拉到同一份。需要重新验证身份。`}
        confirmLabel="发布"
        onConfirm={() => {
          if (confirming) publish.mutate(confirming)
          setConfirming(null)
        }}
        onCancel={() => setConfirming(null)}
      />
    </>
  )
}

/** 新建（group 缺省）或编辑组信息：名称、说明、排序；排序决定节点在多个组里时谁先生效 */
export function GroupFormModal({ group, onClose, onSaved }: { group?: RouteGroup; onClose: () => void; onSaved: (id: string) => void }) {
  const api = useApi()
  const toast = useToast()
  const invalidate = useInvalidateNodes()
  const intent = useIntentKey()
  const [form, setForm] = useState<GroupForm>(() => groupFormFrom(group))
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [formError, setFormError] = useState<string | null>(null)
  const onError = useGroupFailure(setFormError)
  const set = (patch: Partial<GroupForm>) => setForm({ ...form, ...patch })

  const save = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      group
        ? api.request(`v1/route-groups/${group.id}`, routeGroupUpdated, { method: 'PATCH', body, idempotencyKey: intent.keyFor(body) }).then((r) => ({ id: r.group.id, affected: r.affected_nodes }))
        : api.post('v1/route-groups', routeGroupSchema, { body, idempotencyKey: intent.keyFor(body) }).then((g) => ({ id: g.id, affected: 0 })),
    onSuccess: (r) => {
      intent.reset()
      toast(group ? (r.affected ? `已保存，排序变了，${r.affected} 个节点会拉到新配置` : '已保存') : '路由组已建好，接着添加出站、规则与成员')
      void invalidate()
      onSaved(r.id)
    },
    onError: (error) => {
      if (endsIntent(error)) intent.reset()
      if (isApiError(error) && error.status === 422) return setErrors(error.fields)
      onError(error)
    },
  })

  const submit = () => {
    setFormError(null)
    const e = groupFormErrors(form)
    setErrors(e)
    if (Object.keys(e).length) return
    if (!group) return save.mutate(groupCreateBody(form))
    const body = groupPatchBody(group, form)
    if (!body) return onClose()
    save.mutate(body)
  }

  return (
    <Modal
      open
      onClose={onClose}
      size="md"
      title={group ? `编辑路由组「${group.name}」` : '新建路由组'}
      eyebrow={group ? '改排序会让成员节点重新发布，需要重新验证身份' : '建好后再添加出站、规则与成员'}
      actions={
        <>
          <Button size="dialog" variant="ghost" onClick={onClose}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={save.isPending} onClick={submit}>
            {group ? '保存' : '新建'}
          </Button>
        </>
      }
    >
      <div className={css.stack}>
        <div className={css.grid2}>
          <Input label="名称" value={form.name} error={errors.name} onChange={(e) => set({ name: e.target.value })} placeholder="如 香港 · 流媒体解锁" />
          <Input label="排序" mono value={form.sortOrder} error={errors.sort_order} onChange={(e) => set({ sortOrder: e.target.value })} />
          <TextArea label="说明（可选）" rows={3} value={form.description} error={errors.description} fieldClassName={css.span2} onChange={(e) => set({ description: e.target.value })} />
        </div>
        <div className={css.faint}>排序小的先生效：节点在多个组里时，排在前面的组的规则先匹配，同名出站也以它为准。</div>
        {formError && <div className={css.errorBox}>{formError}</div>}
      </div>
    </Modal>
  )
}

/** 成员：勾选未退役的节点；只发改了的集合 */
function MembersModal({ group, onClose }: { group: RouteGroup; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const invalidate = useInvalidateNodes()
  const intent = useIntentKey()
  const nodes = useNodes()
  const initial = group.members.map((m) => m.id)
  const [picked, setPicked] = useState<string[]>(initial)
  const [formError, setFormError] = useState<string | null>(null)
  const onError = useGroupFailure(setFormError)
  const toggle = (id: string) => setPicked(picked.includes(id) ? picked.filter((p) => p !== id) : [...picked, id])

  const save = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.put(`v1/route-groups/${group.id}/members`, groupRoutingSaved, { body, idempotencyKey: intent.keyFor(body) }),
    onSuccess: (r) => {
      intent.reset()
      toast(`成员已更新，${r.affected_nodes} 个节点会拉到新配置`)
      void invalidate()
      onClose()
    },
    onError: (error) => {
      if (endsIntent(error)) intent.reset()
      onError(error)
    },
  })

  return (
    <Modal
      open
      onClose={onClose}
      size="md"
      title={`「${group.name}」的成员节点`}
      eyebrow="进出组的节点会重新发布，需要重新验证身份"
      actions={
        <>
          <Button size="dialog" variant="ghost" onClick={onClose}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={save.isPending} onClick={() => (sameIds(picked, initial) ? onClose() : save.mutate({ row_version: group.row_version, node_ids: picked }))}>
            保存
          </Button>
        </>
      }
    >
      <div className={css.stack}>
        <QueryView query={nodes} rows={4} isEmpty={(d) => d.nodes.length === 0} empty={<span className={css.faint}>还没有节点。</span>}>
          {(data) => (
            <div className={x.groupChecks}>
              {data.nodes
                .filter((n) => n.serving_status !== 'retired' || picked.includes(n.id))
                .map((n) => (
                  <Checkbox key={n.id} label={`${n.name} · ${nodeState(n).label}`} checked={picked.includes(n.id)} onChange={() => toggle(n.id)} />
                ))}
            </div>
          )}
        </QueryView>
        <span className={css.faint}>一个节点可以在多个组里，按组的排序依次生效。移出组的节点若还有规则指向组内出站，保存会被拒绝。</span>
        {formError && <div className={css.errorBox}>{formError}</div>}
      </div>
    </Modal>
  )
}
