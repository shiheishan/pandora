import { useMutation } from '@tanstack/react-query'
import { isApiError } from '../../../core/api'
import { useApi } from '../../../shell/runtime'
import { useToast } from '../../../ui'
import { upgradeResultToast } from './infra'
import { useFailure, useIntentKey, useInvalidateNodes } from './queries'
import { bootstrapTokenResponse, serverDeleted, serverSchema, serverUpgradeResponse, type Server, type ServerStatus } from './schemas'

export interface InstallSecret {
  serverId: string
  title: string
  command: string
  expiresAt: string
}

/** 签发服务器安装令牌；成功交给 onIssued 展示（令牌只此一次可见），失败默认 Toast，reauth 取消静默 */
export function useIssueServerToken(onIssued: (secret: InstallSecret) => void) {
  const api = useApi()
  const fail = useFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateNodes()
  return useMutation({
    mutationFn: (s: Pick<Server, 'id' | 'name'>) => api.post(`v1/servers/${s.id}/bootstrap-token`, bootstrapTokenResponse, { body: { ttl_minutes: 30 }, idempotencyKey: intent.keyFor([s.id, 'bootstrap']) }).then((r) => ({ r, s })),
    onSuccess: ({ r, s }) => {
      intent.reset()
      void invalidate()
      onIssued({ serverId: s.id, title: `在 ${s.name} 上执行`, command: r.install_command, expiresAt: r.expires_at })
    },
    onError: (error) => fail(error, { intent }),
  })
}

/** 改服务器状态（合法边由后端裁决，409 文案直接可读）；版本冲突时刷新到最新 */
export function useSetServerStatus(onDone?: (s: Server) => void) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  return useMutation({
    mutationFn: ({ server, to, reason, done }: { server: Pick<Server, 'id' | 'row_version'>; to: ServerStatus; reason?: string; done: string }) =>
      api.post(`v1/servers/${server.id}/status`, serverSchema, { body: { status: to, row_version: server.row_version, ...(reason?.trim() ? { reason: reason.trim() } : {}) } }).then((s) => ({ s, done })),
    onSuccess: ({ s, done }) => {
      toast(done)
      void invalidate()
      onDone?.(s)
    },
    onError: (error) => {
      fail(error)
      if (isApiError(error, 'conflict')) void invalidate()
    },
  })
}

/** 删除服务器（仅草稿或已退役；名下节点级联静默）；要 reauth，DELETE 必须带 { row_version } */
export function useDeleteServer(onDeleted?: (s: Pick<Server, 'id' | 'name'>) => void) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  return useMutation({
    mutationFn: (s: Pick<Server, 'id' | 'row_version' | 'name'>) => api.delete(`v1/servers/${s.id}`, serverDeleted, { body: { row_version: s.row_version } }).then(() => s),
    onSuccess: (s) => {
      toast(`服务器 ${s.name} 已删除`)
      void invalidate()
      onDeleted?.(s)
    },
    onError: (error) => {
      fail(error)
      if (isApiError(error, 'conflict')) void invalidate()
    },
  })
}

/** 批量在线升级：要 reauth（api 层自动弹框）与幂等键；结果逐台，没发起的原因在 toast 里列出；409 是发布包不能在线升级，文案原样显示 */
export function useUpgradeServers(onDone?: () => void) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateNodes()
  return useMutation({
    mutationFn: (list: readonly Pick<Server, 'id' | 'name'>[]) => {
      const ids = list.map((s) => s.id)
      return api
        .post('v1/servers/upgrade', serverUpgradeResponse, { body: { server_ids: ids }, idempotencyKey: intent.keyFor([...ids].sort()) })
        .then((r) => ({ r, names: new Map(list.map((s) => [s.id, s.name])) }))
    },
    onSuccess: ({ r, names }) => {
      intent.reset()
      const t = upgradeResultToast(r, names)
      toast(t.text, t.tone === 'danger' ? 'danger' : undefined)
      void invalidate()
      onDone?.()
    },
    onError: (error) => {
      fail(error, { intent })
      if (isApiError(error, 'conflict')) void invalidate()
    },
  })
}
