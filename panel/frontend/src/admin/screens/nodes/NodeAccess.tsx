import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import {
  Button,
  Card,
  Checkbox,
  ConfirmModal,
  Empty,
  Input,
  Modal,
  QueryView,
  Switch,
  useToast,
} from '../../../ui'
import { useFailure, useIntentKey, useInvalidateNodes } from './queries'
import { deletedResponse, relaySchema, type NodeRow, type Relay } from './schemas'
import { RELAYS_PER_NODE_MAX } from './xb'
import { useNodeRelays } from './xbQueries'
import { NodeECH } from './NodeECH'
import css from './xb.module.css'

export function NodeAccess({ node }: { node: NodeRow }) {
  return (
    <div className={css.stack}>
      <NodeRelaysSection node={node} />
      <NodeECH node={node} />
    </div>
  )
}

function NodeRelaysSection({ node }: { node: NodeRow }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const createIntent = useIntentKey()

  const retired = node.serving_status === 'retired'
  const relaysQuery = useNodeRelays(node.id)

  const [modalMode, setModalMode] = useState<'create' | 'edit' | null>(null)
  const [editingRelay, setEditingRelay] = useState<Relay | null>(null)

  // 弹窗表单状态
  const [formName, setFormName] = useState('')
  const [formHost, setFormHost] = useState('')
  const [formPort, setFormPort] = useState('')
  const [formSni, setFormSni] = useState('')
  const [formHostHeader, setFormHostHeader] = useState('')
  const [formEnabled, setFormEnabled] = useState(true)
  const [formSortOrder, setFormSortOrder] = useState('0')
  const [formErrors, setFormErrors] = useState<Record<string, string>>({})

  // 删除确认状态
  const [deletingRelay, setDeletingRelay] = useState<Relay | null>(null)

  const openCreateModal = () => {
    setModalMode('create')
    setEditingRelay(null)
    setFormName('')
    setFormHost('')
    setFormPort('')
    setFormSni('')
    setFormHostHeader('')
    setFormEnabled(true)
    setFormSortOrder('0')
    setFormErrors({})
  }

  const openEditModal = (r: Relay) => {
    setModalMode('edit')
    setEditingRelay(r)
    setFormName(r.name)
    setFormHost(r.server_host)
    setFormPort(String(r.server_port))
    setFormSni(r.sni ?? '')
    setFormHostHeader(r.host_header ?? '')
    setFormEnabled(r.enabled)
    setFormSortOrder(String(r.sort_order))
    setFormErrors({})
  }

  const closeModal = () => {
    setModalMode(null)
    setEditingRelay(null)
  }

  // 创建中转
  const createMutation = useMutation({
    mutationFn: (body: {
      name: string
      server_host: string
      server_port: number
      sni: string | null
      host_header: string | null
      enabled?: boolean
      sort_order: number
    }) =>
      api.post(`v1/nodes/${node.id}/relays`, relaySchema, {
        body,
        idempotencyKey: createIntent.keyFor([node.id, body]),
      }),
    onSuccess: () => {
      createIntent.reset()
      toast('中转线路已添加')
      closeModal()
      void invalidate()
    },
    onError: (e) => {
      fail(e, {
        intent: createIntent,
        fields: (fields) => setFormErrors(fields),
      })
    },
  })

  // 修改中转
  const updateMutation = useMutation({
    mutationFn: ({
      rid,
      body,
    }: {
      rid: string
      body: Record<string, unknown>
    }) => api.request(`v1/node-relays/${rid}`, relaySchema, { method: 'PATCH', body }),
    onSuccess: () => {
      toast('中转线路已更新')
      closeModal()
      void invalidate()
    },
    onError: (e) => {
      fail(e, (fields) => setFormErrors(fields))
    },
  })

  // 启停切换
  const toggleMutation = useMutation({
    mutationFn: ({ rid, row_version, enabled }: { rid: string; row_version: number; enabled: boolean }) =>
      api.request(`v1/node-relays/${rid}`, relaySchema, { method: 'PATCH', body: { row_version, enabled } }),
    onSuccess: () => {
      toast('状态已切换')
      void invalidate()
    },
    onError: (e) => fail(e),
  })

  // 删除中转
  const deleteMutation = useMutation({
    mutationFn: ({ rid, row_version }: { rid: string; row_version: number }) =>
      api.delete(`v1/node-relays/${rid}`, deletedResponse, { body: { row_version } }),
    onSuccess: () => {
      toast('中转线路已删除')
      setDeletingRelay(null)
      void invalidate()
    },
    onError: (e) => fail(e),
  })

  const handleFormSubmit = () => {
    const port = Number(formPort.trim())
    const sortOrder = Number(formSortOrder.trim())

    const localErrors: Record<string, string> = {}
    if (!formName.trim()) localErrors.name = '请填写中转名称'
    if (!formHost.trim()) localErrors.server_host = '请填写中转地址'
    if (!Number.isInteger(port) || port < 1 || port > 65535) localErrors.server_port = '端口必须在 1 到 65535 之间'
    if (!Number.isInteger(sortOrder)) localErrors.sort_order = '排序须为整数'

    if (Object.keys(localErrors).length > 0) {
      setFormErrors(localErrors)
      return
    }

    const sni = formSni.trim() || null
    const hostHeader = formHostHeader.trim() || null

    if (modalMode === 'create') {
      createMutation.mutate({
        name: formName.trim(),
        server_host: formHost.trim(),
        server_port: port,
        sni,
        host_header: hostHeader,
        enabled: formEnabled,
        sort_order: sortOrder,
      })
    } else if (modalMode === 'edit' && editingRelay) {
      const body: Record<string, unknown> = { row_version: editingRelay.row_version }
      let changed = false

      if (formName.trim() !== editingRelay.name) {
        body.name = formName.trim()
        changed = true
      }
      if (formHost.trim() !== editingRelay.server_host) {
        body.server_host = formHost.trim()
        changed = true
      }
      if (port !== editingRelay.server_port) {
        body.server_port = port
        changed = true
      }
      if (sni !== editingRelay.sni) {
        body.sni = sni
        changed = true
      }
      if (hostHeader !== editingRelay.host_header) {
        body.host_header = hostHeader
        changed = true
      }
      if (formEnabled !== editingRelay.enabled) {
        body.enabled = formEnabled
        changed = true
      }
      if (sortOrder !== editingRelay.sort_order) {
        body.sort_order = sortOrder
        changed = true
      }

      if (!changed) {
        toast('没有改动')
        closeModal()
        return
      }

      updateMutation.mutate({ rid: editingRelay.id, body })
    }
  }

  const atMaxRelays = (relaysQuery.data?.length ?? 0) >= RELAYS_PER_NODE_MAX

  return (
    <Card>
      <div className={css.cardTitleRow}>
        <div className={css.sectionTitle}>中转</div>
        <Button
          size="sm"
          disabled={retired || atMaxRelays}
          title={atMaxRelays ? `最多添加 ${RELAYS_PER_NODE_MAX} 条中转线路` : undefined}
          onClick={openCreateModal}
        >
          添加中转
        </Button>
      </div>

      <div className={css.cardDesc}>
        中转是这条线路的另一个入口（中转机、IPLC 入口等）：只有自己的名称、地址、端口，可选 SNI 与 Host；协议、密钥、倍率、标签都继承本节点，订阅里紧跟在本节点后面。节点端不用改配置。它与「路由」里的链式出站不同：链式出站是节点出去时再经一跳，中转是用户进来时换个地址。
      </div>

      <QueryView
        query={relaysQuery}
        rows={2}
        isEmpty={(data) => data.length === 0}
        empty={<Empty bare title="还没有中转" description="添加后订阅里会紧跟本节点多出一条线路" />}
      >
        {(relays) => (
          <div className={css.relayList}>
            {relays.map((r) => (
              <div key={r.id} className={css.relayItem}>
                <div className={css.relayMain}>
                  <div className={css.relayName}>{r.name}</div>
                  <div className={css.relayAddr}>
                    {r.server_host}:{r.server_port}
                  </div>
                  {(r.sni || r.host_header) && (
                    <div className={css.relayExtra}>
                      {r.sni && <span>SNI: {r.sni} </span>}
                      {r.host_header && <span>Host: {r.host_header}</span>}
                    </div>
                  )}
                </div>
                <div className={css.relayControls}>
                  <Switch
                    checked={r.enabled}
                    aria-label={`启用 ${r.name}`}
                    disabled={retired || toggleMutation.isPending}
                    onChange={(e) =>
                      toggleMutation.mutate({ rid: r.id, row_version: r.row_version, enabled: e.target.checked })
                    }
                  />
                  <Button
                    size="xs"
                    variant="ghost"
                    disabled={retired}
                    onClick={() => openEditModal(r)}
                  >
                    编辑
                  </Button>
                  <Button
                    size="xs"
                    variant="ghost"
                    disabled={retired}
                    onClick={() => setDeletingRelay(r)}
                  >
                    删除
                  </Button>
                </div>
              </div>
            ))}
          </div>
        )}
      </QueryView>

      {/* 新建/编辑 Modal */}
      <Modal
        open={modalMode !== null}
        title={modalMode === 'create' ? '添加中转' : '编辑中转'}
        onClose={closeModal}
        actions={
          <>
            <Button size="dialog" variant="ghost" onClick={closeModal}>
              取消
            </Button>
            <Button
              size="dialog"
              variant="primary"
              busy={createMutation.isPending || updateMutation.isPending}
              onClick={handleFormSubmit}
            >
              保存
            </Button>
          </>
        }
      >
        <div className={css.modalForm}>
          <div>
            <Input
              label="名称"
              value={formName}
              placeholder="例如：香港入口 01"
              error={formErrors.name}
              onChange={(e) => {
                setFormName(e.target.value)
                if (formErrors.name) setFormErrors((prev) => ({ ...prev, name: '' }))
              }}
            />
          </div>

          <div className={css.formGrid}>
            <div>
              <Input
                label="中转地址"
                value={formHost}
                placeholder="域名或 IP"
                error={formErrors.server_host}
                onChange={(e) => {
                  setFormHost(e.target.value)
                  if (formErrors.server_host) setFormErrors((prev) => ({ ...prev, server_host: '' }))
                }}
              />
            </div>
            <div>
              <Input
                type="number"
                label="端口"
                value={formPort}
                placeholder="1–65535"
                error={formErrors.server_port}
                onChange={(e) => {
                  setFormPort(e.target.value)
                  if (formErrors.server_port) setFormErrors((prev) => ({ ...prev, server_port: '' }))
                }}
              />
            </div>
          </div>

          <div className={css.formGrid}>
            <div>
              <Input
                label="SNI（可选）"
                value={formSni}
                placeholder="留空用本节点的域名"
                error={formErrors.sni}
                onChange={(e) => {
                  setFormSni(e.target.value)
                  if (formErrors.sni) setFormErrors((prev) => ({ ...prev, sni: '' }))
                }}
              />
            </div>
            <div>
              <Input
                label="Host（可选）"
                value={formHostHeader}
                placeholder="留空用本节点的地址"
                error={formErrors.host_header}
                onChange={(e) => {
                  setFormHostHeader(e.target.value)
                  if (formErrors.host_header) setFormErrors((prev) => ({ ...prev, host_header: '' }))
                }}
              />
            </div>
          </div>

          <div className={css.formGrid}>
            <div>
              <Input
                type="number"
                label="排序"
                value={formSortOrder}
                placeholder="0"
                error={formErrors.sort_order}
                onChange={(e) => {
                  setFormSortOrder(e.target.value)
                  if (formErrors.sort_order) setFormErrors((prev) => ({ ...prev, sort_order: '' }))
                }}
              />
            </div>
            <div className={css.checkboxAlign}>
              <Checkbox
                checked={formEnabled}
                label="启用此中转"
                onChange={(e) => setFormEnabled(e.target.checked)}
              />
            </div>
          </div>
        </div>
      </Modal>

      {/* 删除确认 ConfirmModal */}
      <ConfirmModal
        open={deletingRelay !== null}
        title={`删除中转「${deletingRelay?.name}」？`}
        body="删除后订阅里立即去掉这条中转。"
        confirmLabel="删除"
        onConfirm={() => {
          if (!deletingRelay) return
          return deleteMutation
            .mutateAsync({ rid: deletingRelay.id, row_version: deletingRelay.row_version })
            .then(
              () => undefined,
              () => undefined,
            )
        }}
        onCancel={() => setDeletingRelay(null)}
      />
    </Card>
  )
}
