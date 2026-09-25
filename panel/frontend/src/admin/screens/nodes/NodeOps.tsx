/**
 * [INPUT]: 依赖 react 的 useState / ReactNode，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../core/api 的 isApiError，依赖 ../../../core/router 的 navigate，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./logic、./queries、./schemas，依赖 ./nodes.module.css
 * [OUTPUT]: 对外提供 NodeOps（节点抽屉「操作」标签）
 * [POS]: admin/screens/nodes 抽屉的操作页（设计稿 d_ops，文案按契约改写）：发布配置（POST config/publish scope=node payload={}，等于强制重新下发）、复制节点（补目标服务器与复制路由两个选项，这也是已部署节点换机器的正确路径）、迁移（保留规则 5：只有从未部署过的草稿能迁移，409 时列出仍绑定的资产）、上线（R108 activate：生命周期在接入尾段 attesting 至 canary 时显示，一步推到 active 并让服务器就绪，409 原样显示原因、warnings 逐条 Toast）、启用 / 停用（status:batch 只放一项）、退役（R57 retire，不可逆）、删除（转终态 destroyed、名字可复用、历史保留）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useState, type ReactNode } from 'react'
import { isApiError } from '../../../core/api'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Checkbox, ConfirmModal, Input, Modal, Select, useToast } from '../../../ui'
import { MOVE_BLOCKED_HINT, canActivate, canMove, canTransition, moveBlockers } from './logic'
import css from './nodes.module.css'
import { endsIntent, useCan, useFailure, useIntentKey, useInvalidateNodes, useServers } from './queries'
import { adminNodeSchema, batchStatusResponse, deletedResponse, publishResponse, type NodeRow } from './schemas'

type Pending = 'publish' | 'toggle' | 'retire' | 'delete' | null

export function NodeOps({ node, onGone }: { node: NodeRow; onGone: () => void }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const servers = useServers()
  const publishIntent = useIntentKey()
  const toggleIntent = useIntentKey()
  const retireIntent = useIntentKey()
  const moveIntent = useIntentKey()
  const activateIntent = useIntentKey()
  const [confirm, setConfirm] = useState<Pending>(null)
  const [copying, setCopying] = useState(false)
  const [moveTo, setMoveTo] = useState('')
  const [moveBlock, setMoveBlock] = useState<string[] | null>(null)
  const retired = node.serving_status === 'retired'
  const enabling = node.serving_status === 'draft' || node.serving_status === 'disabled'
  const toggleTo = enabling ? 'active' : 'disabled'

  const after = (message: string) => () => {
    toast(message)
    void invalidate()
  }

  const publish = useMutation({
    mutationFn: () => {
      const body = { scope: 'node', scope_ref: node.id, payload: {} }
      return api.post('v1/nodes/config/publish', publishResponse, { body, idempotencyKey: publishIntent.keyFor(body) })
    },
    onSuccess: (r) => {
      publishIntent.reset()
      after(`配置已发布 · 版本 #${r.version}`)()
    },
    onError: (e) => fail(e, { intent: publishIntent }),
  })
  const toggle = useMutation({
    mutationFn: () => {
      const body = { items: [{ id: node.id, row_version: node.row_version }], serving_status: toggleTo }
      return api.post('v1/nodes/status:batch', batchStatusResponse, { body, idempotencyKey: toggleIntent.keyFor(body) })
    },
    onSuccess: () => {
      toggleIntent.reset()
      after(enabling ? '已启用，心跳正常后开始下发给用户' : '已停用，不再下发给用户')()
    },
    onError: (e) => {
      fail(e, { intent: toggleIntent })
      void invalidate()
    },
  })
  const activate = useMutation({
    mutationFn: () => {
      const body = { row_version: node.row_version }
      return api.post(`v1/nodes/${node.id}/activate`, adminNodeSchema, { body, idempotencyKey: activateIntent.keyFor([node.id, body]) })
    },
    onSuccess: (r) => {
      activateIntent.reset()
      // warnings（如「未划入节点池，不服务任何用户」）逐条提示，不挡上线
      r.warnings?.forEach((w) => toast(w, 'danger'))
      after('已上线：节点进入服务，服务器已就绪')()
    },
    onError: (e) => {
      // 409 的原因（身份、协议、终态等）由 useFailure 原样 Toast
      fail(e, { intent: activateIntent })
      void invalidate()
    },
  })
  const retire = useMutation({
    mutationFn: () => {
      const body = { row_version: node.row_version }
      return api.post(`v1/nodes/${node.id}/retire`, adminNodeSchema, { body, idempotencyKey: retireIntent.keyFor([node.id, body]) })
    },
    onSuccess: () => {
      retireIntent.reset()
      after('已退役：不再下发，历史流量与审计保留')()
    },
    onError: (e) => {
      fail(e, { intent: retireIntent })
      void invalidate()
    },
  })
  const remove = useMutation({
    mutationFn: () => api.delete(`v1/nodes/${node.id}`, deletedResponse, { body: { row_version: node.row_version } }),
    onSuccess: () => {
      after('节点已从列表移除')()
      onGone()
    },
    onError: (e) => fail(e),
  })
  const move = useMutation({
    mutationFn: () => {
      const body = { server_id: moveTo, row_version: node.row_version }
      return api.post(`v1/nodes/${node.id}/move`, adminNodeSchema, { body, idempotencyKey: moveIntent.keyFor([node.id, body]) })
    },
    onSuccess: () => {
      moveIntent.reset()
      setMoveTo('')
      after('已迁移到新服务器')()
    },
    onError: (e) => {
      if (endsIntent(e)) moveIntent.reset()
      if (isApiError(e, 'conflict') && moveBlockers(e.fields).length) return setMoveBlock(moveBlockers(e.fields))
      fail(e)
      void invalidate()
    },
  })

  const movable = canMove(node)
  const targets = (servers.data ?? []).filter((s) => s.id !== node.server_id)

  return (
    <div className={css.stackLg}>
      {canActivate(node) && (
        <Action
          title="上线"
          detail={`节点已接入（生命周期 ${node.status}），上线后进入服务，所在服务器一并就绪。之后按正常启停管理。`}
          blocked={!can('node.lifecycle') ? '需要节点生命周期权限' : null}
        >
          <Button size="sm" variant="primary" busy={activate.isPending} onClick={() => activate.mutate()}>
            上线
          </Button>
        </Action>
      )}
      <Action title="发布配置" detail="把当前协议参数与路由重新下发到节点，节点热加载，已有连接不中断。" blocked={!can('node.config.publish') ? '需要配置发布权限' : retired ? '已退役的节点不能发布' : null}>
        <Button size="sm" busy={publish.isPending} onClick={() => setConfirm('publish')}>
          发布
        </Button>
      </Action>
      <Action title="复制节点" detail="复制协议参数，生成一个新的草稿节点；可以放到另一台服务器上。" blocked={!can('node.provision') ? '需要节点部署权限' : !node.server_id ? '原节点没有绑定服务器，不能复制' : null}>
        <Button size="sm" onClick={() => setCopying(true)}>
          复制
        </Button>
      </Action>
      <Action title="迁移到其他服务器" detail={movable ? '换一台承载机器。只有从未部署过的草稿节点可以迁移。' : MOVE_BLOCKED_HINT} blocked={!can('node.provision') ? '需要节点部署权限' : null}>
        {movable && (
          <span className={css.inlineControls}>
            <Select aria-label="目标服务器" size="sm" placeholder="选择服务器" value={moveTo} onChange={(e) => setMoveTo(e.target.value)} options={targets.map((s) => ({ value: s.id, label: s.name, disabled: s.status !== 'ready' }))} />
            <Button size="sm" disabled={!moveTo} busy={move.isPending} onClick={() => move.mutate()}>
              迁移
            </Button>
          </span>
        )}
      </Action>
      <Action
        title={enabling ? '启用节点' : '停用节点'}
        detail={enabling ? '启用后心跳正常就开始下发给用户。要求服务器就绪、协议通过校验。' : '停用后不再下发给用户，可随时恢复。'}
        blocked={!can('node.lifecycle') ? '需要节点生命周期权限' : retired ? '已退役，不能再启用' : canActivate(node) ? '节点还在接入尾段，先用上面的「上线」' : !canTransition(node.serving_status, toggleTo) ? '当前状态不能直接切换' : null}
      >
        <Button size="sm" busy={toggle.isPending} onClick={() => (enabling ? toggle.mutate() : setConfirm('toggle'))}>
          {enabling ? '启用' : '停用'}
        </Button>
      </Action>
      <Action title="退役节点" detail="永久下线，保留历史流量与审计。退役后不能再启用。" blocked={!can('node.lifecycle') ? '需要节点生命周期权限' : retired ? '已经退役' : null}>
        <Button size="sm" className={css.warnButton} busy={retire.isPending} onClick={() => setConfirm('retire')}>
          退役
        </Button>
      </Action>
      <Action title="删除节点" detail="从列表移除，名字可以复用；历史流量与审计保留。在服务中的节点要先停用或退役。" blocked={!can('node.lifecycle') ? '需要节点生命周期权限' : node.serving_status === 'active' || node.serving_status === 'draining' ? '节点还在服务中，先停用或退役' : null}>
        <Button size="sm" className={css.dangerButton} busy={remove.isPending} onClick={() => setConfirm('delete')}>
          删除
        </Button>
      </Action>

      <ConfirmModal open={confirm === 'publish'} title={`发布配置到「${node.name}」？`} body="节点会热加载当前协议与路由，已有连接不中断。" confirmLabel="发布" onConfirm={() => {
          setConfirm(null)
          publish.mutate()
        }} onCancel={() => setConfirm(null)} />
      <ConfirmModal open={confirm === 'toggle'} title={`停用「${node.name}」？`} body="停用后不再下发给用户，已连接的用户下次更新订阅时会换到其他节点。" confirmLabel="停用" onConfirm={() => {
          setConfirm(null)
          toggle.mutate()
        }} onCancel={() => setConfirm(null)} />
      <ConfirmModal
        open={confirm === 'retire'}
        title={`退役「${node.name}」？`}
        body="退役会吊销节点身份、停止下发，不能再启用；历史流量与审计保留。之后可以删除它来让出名字。"
        confirmLabel="退役"
        tone="danger"
        onConfirm={() => {
          setConfirm(null)
          retire.mutate()
        }}
        onCancel={() => setConfirm(null)}
      />
      <ConfirmModal
        open={confirm === 'delete'}
        title={`删除「${node.name}」？`}
        body="节点从列表移除，名字可以给新节点用；历史流量与审计记录保留。"
        confirmLabel="删除"
        tone="danger"
        onConfirm={() => {
          setConfirm(null)
          remove.mutate()
        }}
        onCancel={() => setConfirm(null)}
      />
      <Modal
        open={moveBlock !== null}
        onClose={() => setMoveBlock(null)}
        title="节点仍绑定部署资产，不能迁移"
        actions={
          <Button size="dialog" variant="primary" onClick={() => setMoveBlock(null)}>
            知道了
          </Button>
        }
      >
        <div className={css.stack}>
          <div className={css.muted}>{MOVE_BLOCKED_HINT}</div>
          <ul className={css.blockers}>{moveBlock?.map((b) => <li key={b}>{b}</li>)}</ul>
        </div>
      </Modal>
      {copying && <CopyModal node={node} onClose={() => setCopying(false)} />}
    </div>
  )
}

function Action({ title, detail, blocked, children }: { title: string; detail: string; blocked: string | null; children: ReactNode }) {
  return (
    <div className={css.action}>
      <div className={css.actionText}>
        <div className={css.actionTitle}>{title}</div>
        <div className={css.faint}>{blocked ?? detail}</div>
      </div>
      {!blocked && children}
    </div>
  )
}

function CopyModal({ node, onClose }: { node: NodeRow; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const intent = useIntentKey()
  const servers = useServers()
  const [name, setName] = useState(`${node.name} 副本`)
  const [target, setTarget] = useState(node.server_id ?? '')
  const [copyRouting, setCopyRouting] = useState(true)
  const [errors, setErrors] = useState<Record<string, string>>({})

  const copy = useMutation({
    mutationFn: () => {
      const body = { row_version: node.row_version, name: name.trim(), copy_routing: copyRouting, ...(target && target !== node.server_id ? { target_server_id: target } : {}) }
      return api.post(`v1/nodes/${node.id}/copy`, adminNodeSchema, { body, idempotencyKey: intent.keyFor([node.id, body]) })
    },
    onSuccess: (created) => {
      intent.reset()
      void invalidate()
      created.warnings?.forEach((w) => toast(w, 'danger'))
      toast('已复制为草稿节点，签发安装令牌并启用后才会下发')
      onClose()
      navigate(`/nodes/nodes/${created.id}/proto`)
    },
    onError: (e) => {
      if (endsIntent(e)) intent.reset()
      if (isApiError(e, 'conflict') && /名称/.test(e.message)) return setErrors({ name: e.message })
      fail(e, setErrors)
    },
  })

  return (
    <Modal
      open
      onClose={onClose}
      dismissible={!copy.isPending}
      title={`复制「${node.name}」`}
      actions={
        <>
          <Button size="dialog" onClick={onClose} disabled={copy.isPending}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={copy.isPending} disabled={!name.trim()} onClick={() => copy.mutate()}>
            复制为草稿
          </Button>
        </>
      }
    >
      <div className={css.stack}>
        <Input label="新节点名称" value={name} onChange={(e) => setName(e.target.value)} error={errors.name} data-autofocus="" />
        <Select
          label="目标服务器"
          value={target}
          onChange={(e) => setTarget(e.target.value)}
          error={errors.target_server_id ?? errors.capacity_nodes}
          options={(servers.data ?? []).map((s) => ({ value: s.id, label: `${s.name}${s.id === node.server_id ? '（原服务器）' : ''}`, disabled: s.status !== 'ready' }))}
        />
        <Checkbox label="同时复制单节点路由规则" checked={copyRouting} onChange={(e) => setCopyRouting(e.target.checked)} />
        <div className={css.faint}>已部署节点要换机器，就复制到新服务器，再退役原节点。</div>
      </div>
    </Modal>
  )
}
