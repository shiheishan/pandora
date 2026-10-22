import { PauseCircleIcon, PlayCircleIcon } from '@hugeicons-pro/core-stroke-rounded'
import { useMutation } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { isApiError } from '../../../core/api'
import { navigate, useHashLocation } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Input, Modal, Select, useToast } from '../../../ui'
import { Callout, DangerRow } from '../../kit'
import css from './detail.module.css'
import x from './infra.module.css'
import {
  activateBlock,
  activationHint,
  bindBlock,
  bindBody,
  canActivate,
  freePort,
  regionLabel,
  serverPickOptions,
  portHint,
  usedPorts,
  UNBOUND_HINT,
  validateBind,
} from './logic'
import { endsIntent, useFailure, useIntentKey, useInvalidateNodes, useNodes, usePools, useServers } from './queries'
import { adminNodeSchema, batchStatusResponse, okResponse, type NodeRow } from './schemas'

type Pending = 'toggle' | 'retire' | 'revoke' | 'unbind' | null

/** 上线成功 toast：池绑了套餐时说明影响范围 */
function activationServeToast(
  node: Pick<NodeRow, 'pool_id' | 'pool_name'>,
  pools: readonly { id: string; name: string; plan_names: string[] }[] | undefined,
): string {
  const fallback = '已上线：节点进入服务，服务器已就绪'
  if (!node.pool_id) return fallback
  const pool = pools?.find((p) => p.id === node.pool_id)
  if (!pool?.plan_names.length) return fallback
  const poolName = node.pool_name ?? pool.name
  const names = pool.plan_names
  const listed = names.length <= 3 ? names.join('、') : `${names.slice(0, 3).join('、')}等 ${names.length} 个`
  return `已上线：节点进入服务。节点池「${poolName}」绑定的套餐（${listed}）的用户更新订阅后就能用到这条线路`
}

/** 详情页头部：上线 / 停用 */
export function NodeHeaderAction({ node }: { node: NodeRow }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const pools = usePools()
  const activateIntent = useIntentKey()
  const toggleIntent = useIntentKey()
  const [confirmToggle, setConfirmToggle] = useState(false)
  const blocked = activateBlock(node)

  const activate = useMutation({
    mutationFn: () => {
      const body = { row_version: node.row_version }
      return api.post(`v1/nodes/${node.id}/activate`, adminNodeSchema, { body, idempotencyKey: activateIntent.keyFor([node.id, body]) })
    },
    onSuccess: (r) => {
      activateIntent.reset()
      if (r.warnings) r.warnings.forEach((w) => toast(activationHint(w)))
      else toast(activationServeToast(node, pools.data))
      void invalidate()
    },
    onError: (e) => {
      fail(e, { intent: activateIntent })
      void invalidate()
    },
  })

  const toggle = useMutation({
    mutationFn: (serving_status: 'active' | 'disabled') => {
      const body = { items: [{ id: node.id, row_version: node.row_version }], serving_status }
      return api.post('v1/nodes/status:batch', batchStatusResponse, { body, idempotencyKey: toggleIntent.keyFor(body) })
    },
    onSuccess: (_, serving_status) => {
      toggleIntent.reset()
      toast(serving_status === 'active' ? '已上线，心跳正常后开始下发给用户' : '已停用，不再下发给用户')
      void invalidate()
    },
    onError: (e) => {
      fail(e, { intent: toggleIntent })
      void invalidate()
    },
  })

  if (node.serving_status === 'retired') return null

  const busy = activate.isPending || toggle.isPending

  if (canActivate(node)) {
    return (
      <Button
        size="sm"
        variant="secondary"
        icon={PlayCircleIcon}
        busy={busy}
        disabled={blocked !== null}
        title={blocked ?? undefined}
        onClick={() => activate.mutate()}
      >
        上线节点
      </Button>
    )
  }

  if (node.serving_status === 'disabled') {
    return (
      <Button
        size="sm"
        variant="secondary"
        icon={PlayCircleIcon}
        busy={busy}
        disabled={blocked !== null}
        title={blocked ?? undefined}
        onClick={() => toggle.mutate('active')}
      >
        上线节点
      </Button>
    )
  }

  if (node.serving_status === 'active') {
    return (
      <>
        <Button size="sm" variant="secondary" icon={PauseCircleIcon} busy={busy} onClick={() => setConfirmToggle(true)}>
          停用节点
        </Button>
        <ConfirmModal
          open={confirmToggle}
          title={`停用「${node.name}」？`}
          body="停用后不再下发给用户，已连接的用户下次更新订阅时会换到其他节点。"
          confirmLabel="停用"
          onConfirm={() => {
            setConfirmToggle(false)
            toggle.mutate('disabled')
          }}
          onCancel={() => setConfirmToggle(false)}
        />
      </>
    )
  }

  return null
}

/** 「身份与操作」里的所在服务器：绑定 / 改绑弹窗与解绑入口（解绑确认框在 NodeOps）；能不能操作只看 bindBlock（后端 movable） */
function NodeServerBinding({
  node,
  onUnbindRequest,
  unbindBusy,
}: {
  node: NodeRow
  onUnbindRequest: () => void
  unbindBusy: boolean
}) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const location = useHashLocation()
  const servers = useServers()
  const nodes = useNodes()
  const bindIntent = useIntentKey()

  const nodeList = nodes.data?.nodes ?? []
  const bound = node.server_id != null
  const block = bindBlock(node)
  const autoOpenBind = location.query.get('bind') === '1' && !bound && block === null

  const [bindOpen, setBindOpen] = useState(autoOpenBind)
  const [serverId, setServerId] = useState(() => node.server_id ?? '')
  const [port, setPort] = useState(() => (node.server_port ? String(node.server_port) : ''))
  const [portManual, setPortManual] = useState(false)
  const [errors, setErrors] = useState<Record<string, string>>({})

  const desc = bound
    ? `当前：${node.server_name ?? '服务器'}${node.server_port ? ` · 端口 ${node.server_port}` : ''}`
    : UNBOUND_HINT
  const descWithBlock = block ? `${desc} · ${block}` : desc
  const selectedServer = (servers.data ?? []).find((s) => s.id === serverId)

  const applyDefaultPort = (sid: string) => {
    setPort(String(freePort(sid, nodeList, node.id)))
  }

  const openBind = () => {
    const sid = node.server_id ?? ''
    setServerId(sid)
    setPort(node.server_port ? String(node.server_port) : '')
    setPortManual(false)
    setErrors({})
    setBindOpen(true)
  }

  useEffect(() => {
    if (location.query.get('bind') !== '1') return
    const query: Record<string, string> = {}
    location.query.forEach((value, key) => {
      if (key !== 'bind') query[key] = value
    })
    navigate(location.path, { query, replace: true })
  }, [location.path, location.query])

  const bind = useMutation({
    mutationFn: (body: ReturnType<typeof bindBody>) =>
      api.post(`v1/nodes/${node.id}/bind`, adminNodeSchema, { body, idempotencyKey: bindIntent.keyFor([node.id, body]) }),
    onSuccess: (r) => {
      bindIntent.reset()
      if (r.warnings?.length) r.warnings.forEach((w) => toast(w))
      else toast('已绑定服务器：上线后开始下发')
      setBindOpen(false)
      void invalidate()
    },
    onError: (error) => {
      if (isApiError(error, 'conflict') && (error.fields.server_port || error.fields.capacity_nodes)) {
        setErrors({ ...error.fields })
        if (endsIntent(error)) bindIntent.reset()
        return
      }
      fail(error, { fields: setErrors, intent: bindIntent })
      void invalidate()
    },
  })

  const submitBind = () => {
    const found = validateBind(serverId, port)
    setErrors(found)
    if (Object.keys(found).length) return
    bind.mutate(bindBody(node, serverId, port))
  }

  return (
    <>
      <div className={css.bindingRow}>
        <div className={css.bindingBody}>
          <b className={css.bindingTitle}>所在服务器</b>
          <small className={css.bindingDescription}>{descWithBlock}</small>
        </div>
        <Button size="sm" variant="secondary" disabled={block !== null} title={block ?? undefined} onClick={openBind}>
          {bound ? '改绑' : '绑定服务器'}
        </Button>
      </div>
      {bound && (
        <DangerRow
          title="解绑服务器"
          description="这台服务器不再运行这个节点，端口随之释放；协议配置、节点池与路由都保留，之后可以再绑定。"
          actionLabel="解绑"
          busy={unbindBusy}
          disabled={block !== null}
          onAction={onUnbindRequest}
        />
      )}
      <Modal
        open={bindOpen}
        onClose={() => !bind.isPending && setBindOpen(false)}
        size="md"
        title={bound ? `改绑服务器 · ${node.name}` : `绑定服务器 · ${node.name}`}
      >
        <div className={css.stack}>
          <Select
            label="服务器"
            placeholder="选择服务器"
            value={serverId}
            error={errors.server_id ?? errors.capacity_nodes}
            disabled={bind.isPending}
            onChange={(e) => {
              const nextId = e.target.value
              setServerId(nextId)
              if (nextId && !portManual) applyDefaultPort(nextId)
              setErrors((er) => {
                const next = { ...er }
                delete next.server_id
                delete next.capacity_nodes
                return next
              })
            }}
            options={serverPickOptions(servers.data ?? [])}
          />
          {serverId ? (
            <p className={css.faint}>
              这台服务器：{selectedServer?.region?.trim() ? selectedServer.region : '没填地区'} · 这个节点：
              {regionLabel(node.country_code)}
            </p>
          ) : null}
          <Input
            label="端口"
            mono
            inputMode="numeric"
            value={port}
            error={errors.server_port}
            hint={serverId ? portHint(usedPorts(serverId, nodeList, node.id)) : '同一台服务器上端口先到先得'}
            disabled={bind.isPending}
            onChange={(e) => {
              setPortManual(true)
              setPort(e.target.value)
              setErrors((er) => {
                const next = { ...er }
                delete next.server_port
                return next
              })
            }}
          />
          <div className={x.editModalActions}>
            <Button size="dialog" variant="ghost" disabled={bind.isPending} onClick={() => setBindOpen(false)}>
              取消
            </Button>
            <Button size="dialog" variant="primary" busy={bind.isPending} onClick={submitBind}>
              绑定
            </Button>
          </div>
        </div>
      </Modal>
    </>
  )
}

/** 「身份与操作」页签：所在服务器、吊销、退役 */
export function NodeOps({ node }: { node: NodeRow }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const retireIntent = useIntentKey()
  const unbindIntent = useIntentKey()
  const [confirm, setConfirm] = useState<Pending>(null)
  const retired = node.serving_status === 'retired'

  const revoke = useMutation({
    mutationFn: () => api.post(`v1/nodes/${node.id}/revoke-identity`, okResponse),
    onSuccess: () => {
      toast('节点身份已吊销，心跳停止后会显示离线')
      void invalidate()
    },
    onError: (e) => fail(e),
  })

  const retire = useMutation({
    mutationFn: () => {
      const body = { row_version: node.row_version }
      return api.post(`v1/nodes/${node.id}/retire`, adminNodeSchema, { body, idempotencyKey: retireIntent.keyFor([node.id, body]) })
    },
    onSuccess: () => {
      retireIntent.reset()
      toast('已退役：不再下发，历史流量与审计保留')
      // 留在详情页，列表失效后页面自然显示已退役
      void invalidate()
    },
    onError: (e) => {
      if (endsIntent(e)) retireIntent.reset()
      fail(e, { intent: retireIntent })
      void invalidate()
    },
  })

  const unbind = useMutation({
    mutationFn: () => {
      const body = { row_version: node.row_version }
      return api.post(`v1/nodes/${node.id}/unbind`, adminNodeSchema, { body, idempotencyKey: unbindIntent.keyFor([node.id, body]) })
    },
    onSuccess: () => {
      unbindIntent.reset()
      toast('已解绑：节点留作配置，端口已释放')
      void invalidate()
    },
    onError: (e) => {
      fail(e, { intent: unbindIntent })
      void invalidate()
    },
  })

  return (
    <div className={css.stack}>
      {retired ? (
        <Callout>节点已退役：退役后不可恢复，只保留历史流量与审计记录。</Callout>
      ) : (
        <>
          <NodeServerBinding node={node} onUnbindRequest={() => setConfirm('unbind')} unbindBusy={unbind.isPending} />
          {/* 接入身份是服务器级的，没绑服务器就没有可吊销的（后端也回 409） */}
          {node.server_id != null && (
            <DangerRow
              title="吊销身份"
              description="节点端立刻停服，要在服务器上重新安装才能恢复。用于机器被入侵或转让。"
              actionLabel="吊销身份"
              busy={revoke.isPending}
              onAction={() => setConfirm('revoke')}
            />
          )}
          <DangerRow
            title="退役"
            description="不再下发给用户、吊销身份；退役后不可恢复，历史流量与审计保留。"
            actionLabel="退役"
            busy={retire.isPending}
            onAction={() => setConfirm('retire')}
          />
        </>
      )}

      <ConfirmModal
        open={confirm === 'revoke'}
        title={`吊销「${node.name}」的身份？`}
        body="节点会被拒绝接入，要到「服务器」页重新签发安装令牌、在机器上重新执行安装命令才能恢复。服务状态不变，心跳停止后显示为离线。"
        confirmLabel="吊销身份"
        tone="danger"
        onConfirm={() => {
          setConfirm(null)
          revoke.mutate()
        }}
        onCancel={() => setConfirm(null)}
      />
      <ConfirmModal
        open={confirm === 'retire'}
        title={`退役「${node.name}」？`}
        body="退役会吊销节点身份、停止下发，退役后不可恢复；历史流量与审计保留。"
        confirmLabel="退役"
        tone="danger"
        onConfirm={() => {
          setConfirm(null)
          retire.mutate()
        }}
        onCancel={() => setConfirm(null)}
      />
      <ConfirmModal
        open={confirm === 'unbind'}
        tone="danger"
        title={`解绑「${node.name}」的服务器？`}
        body="解绑后这台服务器不再运行这个节点，端口随之释放；协议配置、节点池与路由都保留，之后可以再绑定到任意服务器。"
        confirmLabel="解绑"
        onConfirm={() => {
          setConfirm(null)
          unbind.mutate()
        }}
        onCancel={() => setConfirm(null)}
      />
    </div>
  )
}
