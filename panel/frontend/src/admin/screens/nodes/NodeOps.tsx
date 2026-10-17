import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, useToast } from '../../../ui'
import { Callout, DangerRow } from '../../kit'
import css from './detail.module.css'
import { activationHint, canActivate } from './logic'
import { endsIntent, useFailure, useIntentKey, useInvalidateNodes } from './queries'
import { adminNodeSchema, batchStatusResponse, okResponse, type NodeRow } from './schemas'

type Pending = 'toggle' | 'retire' | 'revoke' | null

/** 详情页头部：上线 / 停用 */
export function NodeHeaderAction({ node }: { node: NodeRow }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const activateIntent = useIntentKey()
  const toggleIntent = useIntentKey()
  const [confirmToggle, setConfirmToggle] = useState(false)

  const activate = useMutation({
    mutationFn: () => {
      const body = { row_version: node.row_version }
      return api.post(`v1/nodes/${node.id}/activate`, adminNodeSchema, { body, idempotencyKey: activateIntent.keyFor([node.id, body]) })
    },
    onSuccess: (r) => {
      activateIntent.reset()
      if (r.warnings) r.warnings.forEach((w) => toast(activationHint(w)))
      else toast('已上线：节点进入服务，服务器已就绪')
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
      <Button size="sm" variant="secondary" busy={busy} onClick={() => activate.mutate()}>
        上线节点
      </Button>
    )
  }

  if (node.serving_status === 'disabled') {
    return (
      <Button size="sm" variant="secondary" busy={busy} onClick={() => toggle.mutate('active')}>
        上线节点
      </Button>
    )
  }

  if (node.serving_status === 'active') {
    return (
      <>
        <Button size="sm" variant="secondary" busy={busy} onClick={() => setConfirmToggle(true)}>
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

/** 「身份与操作」页签：吊销、退役 */
export function NodeOps({ node }: { node: NodeRow }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const retireIntent = useIntentKey()
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

  return (
    <div className={css.stack}>
      {retired ? (
        <Callout>节点已退役：退役后不可恢复，只保留历史流量与审计记录。</Callout>
      ) : (
        <>
          <DangerRow
            title="吊销身份"
            description="节点端立刻停服，要在服务器上重新安装才能恢复。用于机器被入侵或转让。"
            actionLabel="吊销身份"
            busy={revoke.isPending}
            onAction={() => setConfirm('revoke')}
          />
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
        body="节点会被拒绝接入，要重新签发安装令牌并注册才能恢复。服务状态不变，心跳停止后显示为离线。"
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
    </div>
  )
}
