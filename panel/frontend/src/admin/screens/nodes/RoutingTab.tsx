import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { ConfirmModal, QueryView, Segmented, useToast } from '../../../ui'
import { GroupFormModal, RouteGroupPanel } from './RouteGroupPanel'
import { ScopeRoutingEditor, type RoutingPayload } from './ScopeRouting'
import css from './nodes.module.css'
import { endsIntent, useCan, useFailure, useGlobalRouting, useIntentKey, useInvalidateNodes, useRouteGroups } from './queries'
import { globalRoutingSaved, type GlobalRouting } from './schemas'

const GLOBAL = 'global'
const NEW = 'new'

export function RoutingTab({ rest }: { rest: readonly string[] }) {
  const can = useCan()
  const groups = useRouteGroups()
  const [creating, setCreating] = useState(false)
  const picked = rest[0] ?? GLOBAL
  const current = groups.data?.find((g) => g.id === picked)
  const scope = current ? current.id : GLOBAL
  const options = [{ value: GLOBAL, label: '全局' }, ...(groups.data ?? []).map((g) => ({ value: g.id, label: `${g.name} · ${g.members.length}` })), ...(can('node.config.publish') ? [{ value: NEW, label: '＋ 新建路由组' }] : [])]

  return (
    <div className={css.stack}>
      <div className={css.toolbar}>
        <Segmented<string>
          label="路由范围"
          size="sm"
          value={scope}
          onChange={(v) => (v === NEW ? setCreating(true) : navigate(v === GLOBAL ? '/nodes/routing' : `/nodes/routing/${v}`, { replace: true }))}
          options={options}
        />
        <span className={css.faint}>生效顺序：节点私有规则 → 所在路由组（按排序）→ 全局规则；同名出站以更具体的范围为准</span>
      </div>
      {current ? <RouteGroupPanel key={current.id} group={current} /> : <GlobalRouting />}
      {creating && (
        <GroupFormModal
          onClose={() => setCreating(false)}
          onSaved={(id) => {
            setCreating(false)
            navigate(`/nodes/routing/${id}`, { replace: true })
          }}
        />
      )}
    </div>
  )
}

function GlobalRouting() {
  const routing = useGlobalRouting()
  return (
    <QueryView query={routing} rows={6} isEmpty={() => false} empty={null}>
      {(data) => <GlobalEditor key={data.revision} data={data} />}
    </QueryView>
  )
}

function GlobalEditor({ data }: { data: GlobalRouting }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const intent = useIntentKey()
  const [formError, setFormError] = useState<string | null>(null)
  const [confirming, setConfirming] = useState<Record<string, unknown> | null>(null)

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

  const submit = (p: RoutingPayload) => {
    setFormError(null)
    setConfirming({ expected_revision: data.revision, outbounds: p.outbounds, routes: p.routes })
  }

  return (
    <>
      <ScopeRoutingEditor
        outbounds={data.outbounds}
        routes={data.routes}
        ruleHint="自上而下匹配，命中即停；节点私有规则与所在路由组的规则排在这些规则之前，它们的兜底规则会遮住全部全局规则"
        emptyHint="没有全局规则。加的规则会下发到全部节点。"
        writable={can('node.config.publish')}
        busy={publish.isPending}
        formError={formError}
        publishLabel="发布到全部节点"
        readOnlyHint="当前账号只能查看，发布需要配置发布权限。"
        onSubmit={submit}
      />
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
    </>
  )
}
