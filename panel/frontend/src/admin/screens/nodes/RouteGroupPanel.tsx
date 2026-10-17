import { useMutation } from '@tanstack/react-query'
import { useState, type ReactNode } from 'react'
import { isApiError } from '../../../core/api'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Input, Modal, QueryView, useToast } from '../../../ui'
import { groupFormErrors, groupFormFrom, groupPatchBody, referenceOutbounds, type GroupForm } from './logic'
import { ScopeRoutingEditor, type RoutingPayload } from './ScopeRouting'
import n from './nodes.module.css'
import { endsIntent, useFailure, useGlobalRouting, useGroupRouting, useIntentKey, useInvalidateNodes } from './queries'
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
  const toast = useToast()
  const invalidate = useInvalidateNodes()
  const intent = useIntentKey()
  const routing = useGroupRouting(group.id)
  const [renaming, setRenaming] = useState(false)
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

  /** 发布行右侧的组操作：改名与删组都在这里 */
  const actions = (
    <>
      <Button size="sm" variant="ghost" onClick={() => (setDeleteError(null), setRenaming(true))}>
        改名
      </Button>
      <Button size="sm" variant="ghost" busy={remove.isPending} onClick={() => (setDeleteError(null), setRemoving(true))}>
        删除路由组
      </Button>
    </>
  )

  return (
    <div className={n.stack}>
      {deleteError && <div className={n.errorBox}>{deleteError}</div>}
      <QueryView query={routing} rows={6} isEmpty={() => false} empty={null}>
        {(data) => <GroupEditor key={data.row_version} group={group} data={data} actions={actions} />}
      </QueryView>
      {renaming && <GroupFormModal group={group} onClose={() => setRenaming(false)} onSaved={() => setRenaming(false)} />}
      <ConfirmModal
        open={removing}
        title={`删除路由组「${group.name}」？`}
        body={`组内 ${group.outbound_count} 个出站与 ${group.rule_count} 条规则一并删除，${group.members.length} 个节点退回只用全局与私有路由并重新发布；节点的规则若还指向组内出站会被拒绝。需要重新验证身份。`}
        confirmLabel="删除"
        tone="danger"
        onConfirm={() => {
          setRemoving(false)
          remove.mutate({ row_version: group.row_version })
        }}
        onCancel={() => setRemoving(false)}
      />
    </div>
  )
}

function GroupEditor({ group, data, actions }: { group: RouteGroup; data: GroupRouting; actions: ReactNode }) {
  const api = useApi()
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
        ruleHint="组内规则排在全局规则之前，只作用于这个组里的节点"
        emptyHint="这个组还没有规则。"
        busy={publish.isPending}
        formError={formError}
        publishLabel="发布到组内节点"
        actions={actions}
        onSubmit={submit}
      />
      <ConfirmModal
        open={confirming !== null}
        title="发布路由到组内节点？"
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

/** 新建（group 缺省）或改名：路由组只有名称，说明、排序、成员都不在前端管 */
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
        ? api.request(`v1/route-groups/${group.id}`, routeGroupUpdated, { method: 'PATCH', body, idempotencyKey: intent.keyFor(body) }).then((r) => ({ id: r.group.id }))
        : api.post('v1/route-groups', routeGroupSchema, { body, idempotencyKey: intent.keyFor(body) }).then((g) => ({ id: g.id })),
    onSuccess: (r) => {
      intent.reset()
      toast(group ? '已改名' : `路由组「${form.name.trim()}」已创建`)
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
    if (e.name) return
    if (!group) return save.mutate({ name: form.name.trim() })
    // 说明与排序不归前端管：表单初值取自组本身，diff 后只会带 name 与 row_version
    const body = groupPatchBody(group, form)
    if (!body) return onClose()
    save.mutate(body)
  }

  return (
    <Modal
      open
      onClose={onClose}
      size={480}
      title={group ? `重命名路由组「${group.name}」` : '新建路由组'}
      subtitle={group ? undefined : '路由组里的规则只作用于加入它的节点；在节点详情「路由」里把节点加进来。'}
      actions={
        <>
          <Button size="dialog" variant="ghost" onClick={onClose}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={save.isPending} onClick={submit}>
            {group ? '保存' : '创建'}
          </Button>
        </>
      }
    >
      <div className={n.stack}>
        <Input label="名称" value={form.name} error={errors.name} onChange={(e) => set({ name: e.target.value })} placeholder="路由组名称，如 流媒体解锁" />
        {formError && <div className={n.errorBox}>{formError}</div>}
      </div>
    </Modal>
  )
}
