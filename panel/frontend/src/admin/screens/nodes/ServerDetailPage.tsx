import { PencilEdit01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { href, navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Empty, Input, Modal, QueryView, TextArea, useToast } from '../../../ui'
import {
  Callout,
  DangerRow,
  DetailPage,
  Row,
  RowList,
  SectionTitle,
  SideFact,
  SideFacts,
  StatusDot,
  type StatusTone,
} from '../../kit'
import {
  SERVER_STATUS,
  capacityMinimum,
  deleteServerNotice,
  nextServerStatuses,
  patchServerBody,
  serverDeleteBlock,
  serverDot,
  serverFormFrom,
  serverMeters,
  serverNodeLines,
  serverNotes,
  serverSubtitle,
  validateServerForm,
  type Meter,
  type ServerForm,
} from './infra'
import x from './infra.module.css'
import { addressLabel, heartbeatLabel, nodeState, protocolLabel, type Tone } from './logic'
import css from './nodes.module.css'
import { useFailure, useInvalidateNodes, useNodes, useServers } from './queries'
import { serverSchema, type NodeRow, type Server } from './schemas'
import { useDeleteServer, useSetServerStatus } from './serverActions'

// ---------------------------------------------------------------------------
// 占用条：CPU、内存、磁盘（样稿 .meters / .meter 三行）
// ---------------------------------------------------------------------------
export function Meters({ meters }: { meters: readonly Meter[] }) {
  return (
    <div className={x.meters}>
      {meters.map((m) => (
        <div key={m.label} className={x.meter}>
          <span>{m.label}</span>
          <span
            className={x.meterTrack}
            role="meter"
            aria-label={m.label}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={m.percent ?? undefined}
          >
            <i
              className={`${x.meterFill} ${m.level === 'normal' ? '' : x[`meter_${m.level}`]}`}
              style={{ width: `${m.percent ?? 0}%` }}
            />
          </span>
          <span className={css.mono}>{m.percent === null ? '—' : `${m.percent}%`}</span>
        </div>
      ))}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 表单字段
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

/** 状态点色：心跳绿 / 红优先（serverDot），其余按状态——维护两态黄，草稿、已退役灰 */
export function serverStatusTone(s: Server): StatusTone {
  const dot = serverDot(s)
  if (dot === 'ok') return 'ok'
  if (dot === 'danger') return 'bad'
  if (s.status === 'draining' || s.status === 'maintenance') return 'warn'
  return 'off'
}

function nodeStatusTone(tone: Tone): StatusTone {
  if (tone === 'ok') return 'ok'
  if (tone === 'danger') return 'bad'
  return 'off'
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

// ---------------------------------------------------------------------------
// 整页详情
// ---------------------------------------------------------------------------
export function ServerDetailPage({ id }: { id: string }) {
  const servers = useServers()
  const nodes = useNodes()
  const server = servers.data?.find((s) => s.id === id) ?? null
  const back = { href: href('/nodes/servers'), label: '服务器' }

  if (!server) {
    return (
      <DetailPage back={back} title="服务器详情">
        <QueryView
          query={servers}
          rows={4}
          isEmpty={(d) => !d.some((s) => s.id === id)}
          empty={<Empty title="不存在或已删除" description="这台服务器可能刚被删除，回到列表刷新看看。" />}
        >
          {() => null}
        </QueryView>
      </DetailPage>
    )
  }

  return <ServerDetailBody server={server} nodes={nodes.data?.nodes ?? []} back={back} />
}

function ServerDetailBody({
  server: s,
  nodes,
  back,
}: {
  server: Server
  nodes: readonly NodeRow[]
  back: { href: string; label: string }
}) {
  const [editing, setEditing] = useState(false)
  const [confirm, setConfirm] = useState<'retire' | 'delete' | null>(null)
  const status = useSetServerStatus()
  const remove = useDeleteServer(() => navigate('/nodes/servers'))

  const meters = serverMeters(s)
  const allMetersNull = meters.every((m) => m.percent === null)
  const lines = serverNodeLines(nodes, s.id)
  const canRetire = nextServerStatuses(s.status).includes('retired')
  const notes = serverNotes(s)

  return (
    <>
      <DetailPage
        back={back}
        title={<span className={css.mono}>{s.name}</span>}
        meta={
          <>
            <StatusDot tone={serverStatusTone(s)}>{SERVER_STATUS[s.status].label}</StatusDot>
            <span>{serverSubtitle(s)}</span>
          </>
        }
        actions={
          s.status !== 'retired' ? (
            <Button size="sm" icon={PencilEdit01Icon} onClick={() => setEditing(true)}>
              编辑
            </Button>
          ) : undefined
        }
        side={
          <SideFacts label="服务器信息">
            <SideFact label="状态">
              <div>{SERVER_STATUS[s.status].label}</div>
              {s.status_reason ? <div className={css.faint}>原因：{s.status_reason}</div> : null}
            </SideFact>
            <SideFact label="地区">{s.region || '地区待识别'}</SideFact>
            <SideFact label="公网 IPv4">
              <span className={css.mono}>{s.public_ipv4 || '—'}</span>
            </SideFact>
            {s.public_ipv6 ? (
              <SideFact label="公网 IPv6">
                <span className={css.mono}>{s.public_ipv6}</span>
              </SideFact>
            ) : null}
            <SideFact label="Agent">{s.agent_version ?? '未连接'}</SideFact>
            <SideFact label="最近心跳">{heartbeatLabel(s.last_heartbeat_at)}</SideFact>
            {s.os_name ? <SideFact label="系统">{s.os_name}</SideFact> : null}
            {s.architecture ? <SideFact label="架构">{s.architecture}</SideFact> : null}
            <SideFact label="在役 / 容量">
              <span className={css.mono}>{`${s.serving_node_count} / ${s.capacity_nodes}`}</span>
            </SideFact>
            <SideFact label="备注">{s.notes || '—'}</SideFact>
          </SideFacts>
        }
      >
        <div className={css.stack}>
          <SectionTitle>占用</SectionTitle>
          {allMetersNull ? <Callout>装好 agent 后这里显示占用</Callout> : <Meters meters={meters} />}
          {notes.map((note) => (
            <Callout key={note}>{note}</Callout>
          ))}
          <SectionTitle hint={`在役 ${s.serving_node_count} / 容量 ${s.capacity_nodes}`}>节点</SectionTitle>
          {lines.length === 0 ? (
            <div className={css.faint}>没有在役节点</div>
          ) : (
            <RowList label="这台服务器上的节点">
              {lines.map((n) => {
                const state = nodeState(n)
                return (
                  <Row
                    key={n.id}
                    lead={<span className={x.cc}>{n.country_code ?? '—'}</span>}
                    title={n.name}
                    meta={`${protocolLabel(n.node_type)} · ${addressLabel(n)}`}
                    value={<StatusDot tone={nodeStatusTone(state.tone)}>{state.label}</StatusDot>}
                    onClick={() => navigate(`/nodes/nodes/${n.id}`)}
                  />
                )
              })}
            </RowList>
          )}
          {s.status !== 'retired' && (
            <DangerRow
              title="退役"
              description={
                canRetire
                  ? '退役后不能再改回其他状态，只能删除；名下节点会随之停止服务。'
                  : '服务中的服务器先在卡片上「标记维护」，再退役。'
              }
              actionLabel="退役"
              disabled={!canRetire}
              busy={status.isPending}
              onAction={() => setConfirm('retire')}
            />
          )}
          <DangerRow
            title="删除服务器"
            description={serverDeleteBlock(s) ?? deleteServerNotice(s)}
            actionLabel="删除"
            disabled={serverDeleteBlock(s) !== null}
            busy={remove.isPending}
            onAction={() => setConfirm('delete')}
          />
        </div>
      </DetailPage>

      <Modal open={editing} onClose={() => setEditing(false)} size="md" title={`编辑服务器 · ${s.name}`}>
        <ServerDetailEdit server={s} onCancel={() => setEditing(false)} onSaved={() => setEditing(false)} />
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
