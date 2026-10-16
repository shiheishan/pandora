import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Input, Modal, TextArea, useToast } from '../../../ui'
import {
  canDeleteServer,
  capacityMinimum,
  deleteServerNotice,
  nextServerStatuses,
  patchServerBody,
  SERVER_STATUS,
  serverFormFrom,
  serverMeters,
  validateServerForm,
  type Meter,
  type ServerForm,
} from './infra'
import x from './infra.module.css'
import css from './nodes.module.css'
import { useFailure, useInvalidateNodes } from './queries'
import { serverSchema, type Server } from './schemas'
import { useDeleteServer, useSetServerStatus } from './serverActions'

// ---------------------------------------------------------------------------
// 占用条：CPU、内存、磁盘
// ---------------------------------------------------------------------------
export function Meters({ meters }: { meters: readonly Meter[] }) {
  return (
    <div className={x.meters}>
      {meters.map((m) => (
        <div key={m.label}>
          <div className={x.meterHead}>
            <span>{m.label}</span>
            <span className={css.mono}>{m.percent === null ? '—' : `${m.percent}%`}</span>
          </div>
          <div className={x.meterTrack} role="meter" aria-label={m.label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={m.percent ?? undefined}>
            <span className={`${x.meterFill} ${m.level === 'normal' ? '' : x[`meter_${m.level}`]}`} style={{ width: `${m.percent ?? 0}%` }} />
          </div>
        </div>
      ))}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 表单字段：新建弹窗只用名称、容量、备注，编辑也只用这三项
// ---------------------------------------------------------------------------
type FieldKey = keyof ServerForm
const FIELD_META: Readonly<Record<FieldKey, { label: string; err: string; mono?: boolean; placeholder?: string; hint?: string }>> = {
  name: { label: '名称', err: 'name', mono: true, placeholder: 'hk-edge-02' },
  region: { label: '地区', err: 'region', placeholder: '留空则首次接入时按 IP 自动识别' },
  hostname: { label: '主机名', err: 'hostname', mono: true },
  publicIpv4: { label: '公网 IPv4', err: 'public_ipv4', mono: true },
  publicIpv6: { label: '公网 IPv6', err: 'public_ipv6', mono: true },
  privateIpv4: { label: '内网 IPv4', err: 'private_ipv4', mono: true },
  architecture: { label: '架构', err: 'architecture', mono: true, placeholder: 'amd64 / arm64' },
  osName: { label: '系统', err: 'os_name' },
  capacity: { label: '容量（最多几个节点）', err: 'capacity_nodes', mono: true, hint: '这台机器最多放几个节点；草稿、在役、停用的节点都占容量' },
  notes: { label: '备注', err: 'notes', hint: '最多 2000 个字符' },
}

export function ServerFields({
  form,
  errors,
  fields,
  disabled,
  onChange,
}: {
  form: ServerForm
  errors: Record<string, string>
  fields: readonly FieldKey[]
  disabled?: boolean
  onChange: (f: ServerForm) => void
}) {
  return (
    <div className={css.grid2}>
      {fields.map((k) => {
        const m = FIELD_META[k]
        return k === 'notes' ? (
          <TextArea
            key={k}
            label={m.label}
            rows={3}
            value={form[k]}
            disabled={disabled}
            error={errors[m.err]}
            hint={m.hint}
            fieldClassName={css.span2}
            onChange={(e) => onChange({ ...form, [k]: e.target.value })}
          />
        ) : (
          <Input
            key={k}
            label={m.label}
            mono={m.mono}
            inputMode={k === 'capacity' ? 'numeric' : undefined}
            placeholder={m.placeholder}
            value={form[k]}
            disabled={disabled}
            error={errors[m.err]}
            hint={m.hint}
            onChange={(e) => onChange({ ...form, [k]: e.target.value })}
          />
        )
      })}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 服务器详情弹窗：查看模式与编辑模式
// ---------------------------------------------------------------------------
export function ServerDetail({
  server,
  nodes,
  onClose,
}: {
  server: Server | null
  nodes: readonly string[]
  onClose: () => void
}) {
  const [editing, setEditing] = useState(false)
  const [confirm, setConfirm] = useState<'retire' | 'delete' | null>(null)
  const [prevId, setPrevId] = useState(server?.id)

  if (server && server.id !== prevId) {
    setPrevId(server.id)
    setEditing(false)
  }

  const status = useSetServerStatus()
  const remove = useDeleteServer(() => onClose())

  if (!server) return null

  const s = server
  const meters = serverMeters(s)
  const cpuPct = meters[0]?.percent
  const memPct = meters[1]?.percent
  const diskPct = meters[2]?.percent
  const allMetersNull = cpuPct === null && memPct === null && diskPct === null
  const metersStr = allMetersNull ? '—' : `${cpuPct ?? 0}% / ${memPct ?? 0}% / ${diskPct ?? 0}%`

  const canRetire = nextServerStatuses(s.status).includes('retired')
  const canDel = canDeleteServer(s)

  return (
    <>
      <Modal
        open={true}
        onClose={onClose}
        size={480}
        title={s.name}
        footerStart={
          !editing ? (
            <div className={x.detailFootActions}>
              {s.status !== 'retired' && (
                <Button
                  size="dialog"
                  variant="ghost"
                  className={canRetire ? x.dangerText : undefined}
                  disabled={!canRetire || status.isPending}
                  title={canRetire ? undefined : '服务中的服务器先标记维护再退役'}
                  onClick={() => setConfirm('retire')}
                >
                  退役
                </Button>
              )}
              <Button
                size="dialog"
                variant="ghost"
                className={canDel ? x.dangerText : undefined}
                disabled={!canDel || remove.isPending}
                title={canDel ? undefined : '只能删除草稿或已退役的服务器，先退役'}
                onClick={() => setConfirm('delete')}
              >
                删除
              </Button>
            </div>
          ) : null
        }
        actions={
          editing ? null : (
            <>
              <Button size="dialog" variant="ghost" onClick={() => setEditing(true)}>
                编辑
              </Button>
              <Button size="dialog" variant="primary" onClick={onClose}>
                关闭
              </Button>
            </>
          )
        }
      >
        {editing ? (
          <ServerDetailEdit server={s} onCancel={() => setEditing(false)} onSaved={() => setEditing(false)} />
        ) : (
          <div className={css.stack}>
            <div className={x.detailRows}>
              <div className={x.detailRow}>
                <span className={x.detailKey}>状态</span>
                <span className={x.detailVal}>
                  {SERVER_STATUS[s.status].label}
                  {s.status_reason && ` · ${s.status_reason}`}
                </span>
              </div>
              <div className={x.detailRow}>
                <span className={x.detailKey}>地区</span>
                <span className={x.detailVal}>{s.region || '地区待识别'}</span>
              </div>
              <div className={x.detailRow}>
                <span className={x.detailKey}>公网 IPv4</span>
                <span className={x.detailVal}>{s.public_ipv4 || '—'}</span>
              </div>
              <div className={x.detailRow}>
                <span className={x.detailKey}>Agent</span>
                <span className={x.detailVal}>{s.agent_version || '未连接'}</span>
              </div>
              <div className={x.detailRow}>
                <span className={x.detailKey}>CPU / 内存 / 磁盘</span>
                <span className={x.detailVal}>{metersStr}</span>
              </div>
              <div className={x.detailRow}>
                <span className={x.detailKey}>节点</span>
                <span className={x.detailVal}>{nodes.join('、') || '这台服务器上还没有节点'}</span>
              </div>
              <div className={x.detailRow}>
                <span className={x.detailKey}>在役 / 容量</span>
                <span className={x.detailVal}>{`${s.serving_node_count} / ${s.capacity_nodes}`}</span>
              </div>
              <div className={x.detailRow}>
                <span className={x.detailKey}>备注</span>
                <span className={x.detailVal}>{s.notes || '—'}</span>
              </div>
            </div>
            {s.never_seen_node_count > 0 && (
              <div className={x.neverSeen}>
                有 {s.never_seen_node_count} 个节点标了在役但从未心跳（多半没装 agent）
              </div>
            )}
          </div>
        )}
      </Modal>

      <ConfirmModal
        open={confirm === 'retire'}
        title={`退役「${s.name}」？`}
        body="退役后不能再改回其他状态，只能删除；名下节点会随之停止服务，请先把需要保留的节点换到别的服务器。"
        confirmLabel="退役"
        tone="danger"
        onConfirm={() => {
          setConfirm(null)
          status.mutate({ server: s, to: 'retired', done: '服务器已退役' })
        }}
        onCancel={() => setConfirm(null)}
      />
      <ConfirmModal
        open={confirm === 'delete'}
        title={`删除服务器「${s.name}」？`}
        body={deleteServerNotice(s)}
        confirmLabel="删除服务器"
        tone="danger"
        onConfirm={() => {
          setConfirm(null)
          remove.mutate(s)
        }}
        onCancel={() => setConfirm(null)}
      />
    </>
  )
}

function ServerDetailEdit({
  server,
  onCancel,
  onSaved,
}: {
  server: Server
  onCancel: () => void
  onSaved: () => void
}) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const [form, setForm] = useState(() => serverFormFrom(server))
  const [errors, setErrors] = useState<Record<string, string>>({})

  const save = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      api.request(`v1/servers/${server.id}`, serverSchema, { method: 'PATCH', body }),
    onSuccess: () => {
      toast('服务器信息已保存')
      void invalidate()
      onSaved()
    },
    onError: (error) => {
      const min = isApiError(error, 'conflict') ? capacityMinimum(error.fields) : null
      if (min !== null) return setErrors({ capacity_nodes: `不能低于当前占用的 ${min} 个节点` })
      if (isApiError(error, 'conflict') && error.fields.row_version) {
        toast('服务器已被其他管理员修改，已刷新到最新', 'danger')
        void invalidate()
        return
      }
      fail(error, setErrors)
    },
  })

  const submit = () => {
    const found = validateServerForm(form)
    setErrors(found)
    if (Object.keys(found).length) return
    const body = patchServerBody(server, form)
    if (!body) return toast('没有改动')
    save.mutate(body)
  }

  return (
    <div className={css.stack}>
      <ServerFields
        form={form}
        errors={errors}
        fields={['name', 'capacity', 'notes']}
        disabled={save.isPending}
        onChange={setForm}
      />
      <div className={x.editModalActions}>
        <Button size="dialog" variant="ghost" disabled={save.isPending} onClick={onCancel}>
          取消
        </Button>
        <Button size="dialog" variant="primary" busy={save.isPending} onClick={submit}>
          保存
        </Button>
      </div>
    </div>
  )
}
