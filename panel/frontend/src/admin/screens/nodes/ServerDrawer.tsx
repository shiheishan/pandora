/**
 * [INPUT]: 依赖 react 的 useState，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../core/api 的 isApiError，依赖 ../../../core/format 的 formatDateTime，依赖 ../../../core/router 的 navigate，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./infra 的服务器纯函数，依赖 ./logic 的 heartbeatLabel / protocolLabel，依赖 ./NodeIdentity 的 SecretModal，依赖 ./queries、./schemas、./serverActions，依赖 ./nodes.module.css 与 ./infra.module.css
 * [OUTPUT]: 对外提供 ServerDrawer、SERVER_TABS、ServerTab、ServerFields（新建弹窗与编辑共用的表单字段）、Meters（三条占用）
 * [POS]: admin/screens/nodes 的服务器详情抽屉（契约待补·前端：设计缺详情）：概览（规格、探针、状态、心跳、节点占用、从未心跳提示、备注）、节点（GET v1/servers/{id}/nodes，含已退役与已销毁，点行跳节点抽屉）、编辑（PATCH 只发改了的字段，"" 清空，容量不能低于占用）、操作（完整状态下拉按合法边、安装令牌、删除仅草稿或已退役）；标签记在 #/nodes/servers/<服务器 id>/<标签>
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { formatDateTime } from '../../../core/format'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Drawer, Empty, Input, QueryView, Select, Tabs, Tag, TextArea, useToast } from '../../../ui'
import { canDeleteServer, capacityMinimum, deleteServerNotice, nextServerStatuses, patchServerBody, SERVER_STATUS, serverDot, serverFormFrom, serverMeters, validateServerForm, type Meter, type ServerForm } from './infra'
import css from './nodes.module.css'
import { heartbeatLabel, protocolLabel } from './logic'
import { SecretModal } from './NodeIdentity'
import x from './infra.module.css'
import { useCan, useFailure, useInvalidateNodes, useServerNodes } from './queries'
import { serverSchema, type Server, type ServerStatus } from './schemas'
import { useDeleteServer, useIssueServerToken, useSetServerStatus, type InstallSecret } from './serverActions'

export const SERVER_TABS = [
  ['overview', '概览'],
  ['nodes', '节点'],
  ['edit', '编辑'],
  ['ops', '操作'],
] as const
export type ServerTab = (typeof SERVER_TABS)[number][0]

export function ServerDrawer({ server, tab, onClose }: { server: Server | null; tab: ServerTab; onClose: () => void }) {
  const status = server ? SERVER_STATUS[server.status] : null
  return (
    <Drawer
      open={server !== null}
      onClose={onClose}
      width={600}
      title={server && <span className={css.mono}>{server.name}</span>}
      subtitle={
        server &&
        status && (
          <span className={css.drawerSub}>
            <span className={css.state}>
              <span className={`${css.dot} ${css[`dot_${serverDot(server)}`]}`} aria-hidden="true" />
              <Tag tone={status.tone}>{status.label}</Tag>
            </span>
            <span className={css.faint}>
              {server.region ?? '地区未知'} · {server.public_ipv4 ?? '—'} · 心跳 {heartbeatLabel(server.last_heartbeat_at)}
            </span>
          </span>
        )
      }
      toolbar={server && <Tabs label="服务器详情" items={SERVER_TABS.map(([value, label]) => ({ value, label }))} value={tab} onChange={(t) => navigate(`/nodes/servers/${server.id}/${t}`, { replace: true })} />}
    >
      {server && (
        <div key={`${server.id}-${tab}`}>
          {tab === 'overview' && <Overview server={server} />}
          {tab === 'nodes' && <ServerNodes serverId={server.id} />}
          {tab === 'edit' && <EditServer key={server.row_version} server={server} />}
          {tab === 'ops' && <ServerOps server={server} onGone={onClose} />}
        </div>
      )}
    </Drawer>
  )
}

// ---------------------------------------------------------------------------
// 概览
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

const dash = (v: string | number | null) => (v === null || v === '' ? '—' : String(v))

function Overview({ server: s }: { server: Server }) {
  const spec = [s.cpu_cores !== null && `${s.cpu_cores} 核`, s.memory_mb !== null && `${Math.round(s.memory_mb / 1024)} GB 内存`, s.disk_gb !== null && `${s.disk_gb} GB 磁盘`].filter(Boolean).join(' · ')
  return (
    <div className={css.stackLg}>
      <Meters meters={serverMeters(s)} />
      <dl className={css.facts}>
        <dt>状态</dt>
        <dd>
          {SERVER_STATUS[s.status].label}
          {s.status_reason && <span className={css.faint}> · {s.status_reason}</span>}
        </dd>
        <dt>心跳</dt>
        <dd>
          {heartbeatLabel(s.last_heartbeat_at)}
          {s.status === 'ready' && <span className={css.faint}> · {s.heartbeat_online ? '在线' : '已超过 90 秒没有心跳'}</span>}
        </dd>
        <dt>节点</dt>
        <dd>
          {s.node_count} 个，在役 {s.active_node_count}、实际服务 {s.serving_node_count}；容量 {s.capacity_nodes}
          {s.never_seen_node_count > 0 && <div className={x.neverSeen}>有 {s.never_seen_node_count} 个节点标了在役但从未心跳（多半没装 agent），不会下发给用户</div>}
        </dd>
        <dt>地区</dt>
        <dd>{dash(s.region)}</dd>
        <dt>公网地址</dt>
        <dd className={css.mono}>
          {dash(s.public_ipv4)}
          {s.public_ipv6 && <div>{s.public_ipv6}</div>}
        </dd>
        <dt>内网地址</dt>
        <dd className={css.mono}>{dash(s.private_ipv4)}</dd>
        <dt>主机名</dt>
        <dd className={`${css.mono} ${css.breakAll}`}>{dash(s.hostname)}</dd>
        <dt>系统</dt>
        <dd>{[s.os_name, s.architecture].filter(Boolean).join(' · ') || '—'}</dd>
        <dt>Agent</dt>
        <dd className={css.mono}>{s.agent_version ?? '未连接'}</dd>
        <dt>规格</dt>
        <dd>{spec || '—（接入后由 agent 回填）'}</dd>
        <dt>探针</dt>
        <dd>{s.metrics_at ? formatDateTime(s.metrics_at) : '还没有上报'}</dd>
        <dt>备注</dt>
        <dd className={css.breakAll}>{dash(s.notes)}</dd>
        <dt>创建</dt>
        <dd>
          {formatDateTime(s.created_at)} <span className={css.faint}>· 更新于 {formatDateTime(s.updated_at)}</span>
        </dd>
      </dl>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 下属节点
// ---------------------------------------------------------------------------
const SERVING_LABEL: Readonly<Record<string, string>> = { draft: '草稿', active: '在役', draining: '排空中', disabled: '已停用', retired: '已退役' }

function ServerNodes({ serverId }: { serverId: string }) {
  const nodes = useServerNodes(serverId)
  return (
    <QueryView query={nodes} rows={3} isEmpty={(d) => d.length === 0} empty={<Empty bare title="这台服务器上还没有节点" description="在「节点」标签新建节点时选这台服务器。" />}>
      {(rows) => (
        <div className={x.nodeList}>
          {rows.map((n) => {
            const gone = n.status === 'destroyed'
            const body = (
              <>
                <span>
                  <span>{n.name}</span>
                  {n.display_name && <span className={css.faint}> · {n.display_name}</span>}
                  <div className={`${css.mono} ${css.faint}`}>{n.server_host ? `${n.server_host}${n.server_port ? `:${n.server_port}` : ''}` : '未设置地址'}</div>
                </span>
                <span className={css.muted}>{protocolLabel(n.node_type)}</span>
                <span className={css.faint}>{gone ? '已销毁' : (SERVING_LABEL[n.serving_status] ?? n.serving_status)}</span>
              </>
            )
            return gone ? (
              <div key={n.id} className={`${x.nodeItem} ${x.nodeGone}`}>
                {body}
              </div>
            ) : (
              <button key={n.id} type="button" className={x.nodeItem} onClick={() => navigate(`/nodes/nodes/${n.id}`)} aria-label={`打开节点 ${n.name}`}>
                {body}
              </button>
            )
          })}
        </div>
      )}
    </QueryView>
  )
}

// ---------------------------------------------------------------------------
// 表单字段：新建弹窗只用名称、容量、备注（其余接入后回填），编辑用全部
// ---------------------------------------------------------------------------
type FieldKey = keyof ServerForm
const FIELD_META: Readonly<Record<FieldKey, { label: string; err: string; mono?: boolean; placeholder?: string }>> = {
  name: { label: '名称', err: 'name', mono: true, placeholder: '如 hk-hkg-edge-2' },
  region: { label: '地区', err: 'region', placeholder: '留空则首次接入时按 IP 自动识别' },
  hostname: { label: '主机名', err: 'hostname', mono: true },
  publicIpv4: { label: '公网 IPv4', err: 'public_ipv4', mono: true },
  publicIpv6: { label: '公网 IPv6', err: 'public_ipv6', mono: true },
  privateIpv4: { label: '内网 IPv4', err: 'private_ipv4', mono: true },
  architecture: { label: '架构', err: 'architecture', mono: true, placeholder: 'amd64 / arm64' },
  osName: { label: '系统', err: 'os_name' },
  capacity: { label: '容量（最多几个节点）', err: 'capacity_nodes', mono: true },
  notes: { label: '备注', err: 'notes' },
}

export function ServerFields({ form, errors, fields, disabled, onChange }: { form: ServerForm; errors: Record<string, string>; fields: readonly FieldKey[]; disabled?: boolean; onChange: (f: ServerForm) => void }) {
  return (
    <div className={css.grid2}>
      {fields.map((k) => {
        const m = FIELD_META[k]
        return k === 'notes' ? (
          <TextArea key={k} label={m.label} rows={3} value={form[k]} disabled={disabled} error={errors[m.err]} fieldClassName={css.span2} onChange={(e) => onChange({ ...form, [k]: e.target.value })} />
        ) : (
          <Input key={k} label={m.label} mono={m.mono} inputMode={k === 'capacity' ? 'numeric' : undefined} placeholder={m.placeholder} value={form[k]} disabled={disabled} error={errors[m.err]} onChange={(e) => onChange({ ...form, [k]: e.target.value })} />
        )
      })}
    </div>
  )
}

function EditServer({ server }: { server: Server }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const [form, setForm] = useState(() => serverFormFrom(server))
  const [errors, setErrors] = useState<Record<string, string>>({})
  const writable = can('node.write')

  const save = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.request(`v1/servers/${server.id}`, serverSchema, { method: 'PATCH', body }),
    onSuccess: () => {
      toast('服务器信息已保存')
      void invalidate()
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
    <div className={css.form}>
      <ServerFields form={form} errors={errors} disabled={!writable || save.isPending} onChange={setForm} fields={['name', 'region', 'publicIpv4', 'publicIpv6', 'privateIpv4', 'hostname', 'architecture', 'osName', 'capacity', 'notes']} />
      <div className={css.faint}>留空的字段会被清空（名称除外）；地区、地址、系统等接入后由 agent 回填，一般不用手填。</div>
      {writable ? (
        <div className={css.formActions}>
          <Button variant="primary" busy={save.isPending} onClick={submit}>
            保存
          </Button>
        </div>
      ) : (
        <div className={css.faint}>当前账号只能查看，修改需要节点编辑权限。</div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 操作：状态、安装令牌、删除
// ---------------------------------------------------------------------------
function ServerOps({ server: s, onGone }: { server: Server; onGone: () => void }) {
  const can = useCan()
  const next = nextServerStatuses(s.status)
  const [to, setTo] = useState<ServerStatus | ''>('')
  const [reason, setReason] = useState('')
  const [confirm, setConfirm] = useState<'retire' | 'delete' | null>(null)
  const [secret, setSecret] = useState<InstallSecret | null>(null)
  const status = useSetServerStatus(() => {
    setTo('')
    setReason('')
  })
  const token = useIssueServerToken(setSecret)
  const remove = useDeleteServer(() => onGone())
  const lifecycle = can('node.lifecycle')

  const change = () => {
    if (!to) return
    if (to === 'retired') return setConfirm('retire')
    status.mutate({ server: s, to, reason, done: `状态已改为「${SERVER_STATUS[to].label}」` })
  }

  return (
    <div className={css.stackLg}>
      <section className={css.formSection}>
        <div className={css.actionTitle}>改状态</div>
        {!lifecycle ? (
          <div className={css.faint}>需要节点生命周期权限。</div>
        ) : next.length === 0 ? (
          <div className={css.faint}>已退役的服务器不能再改状态，只能删除。</div>
        ) : (
          <>
            <div className={x.statusForm}>
              <Select label="改为" placeholder="选择状态" value={to} onChange={(e) => setTo(e.target.value as ServerStatus)} options={next.map((v) => ({ value: v, label: SERVER_STATUS[v].label }))} />
              <Input label="原因（可选）" value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} placeholder="写进审计与状态说明" />
              <Button busy={status.isPending} disabled={!to} onClick={change}>
                改状态
              </Button>
            </div>
            <div className={css.faint}>「维护中」停止分配新连接、已有连接保持；进入「服务中」需要至少一个协议校验通过的在役节点。</div>
          </>
        )}
      </section>

      <div className={css.action}>
        <div className={css.actionText}>
          <div className={css.actionTitle}>安装令牌</div>
          <div className={css.faint}>{s.status === 'retired' ? '已退役的服务器不能签发' : !can('node.provision') ? '需要节点部署权限' : '生成在这台机器上执行的安装命令，30 分钟内有效，只显示一次。'}</div>
        </div>
        {s.status !== 'retired' && can('node.provision') && (
          <Button size="sm" busy={token.isPending} onClick={() => token.mutate(s)}>
            签发
          </Button>
        )}
      </div>

      <div className={css.action}>
        <div className={css.actionText}>
          <div className={css.actionTitle}>删除服务器</div>
          <div className={css.faint}>{!lifecycle ? '需要节点生命周期权限' : canDeleteServer(s) ? deleteServerNotice(s) : '只能删除草稿或已退役的服务器，请先把状态改为「已退役」。名下节点会随服务器一起下线。'}</div>
        </div>
        {lifecycle && canDeleteServer(s) && (
          <Button size="sm" className={css.dangerButton} busy={remove.isPending} onClick={() => setConfirm('delete')}>
            删除
          </Button>
        )}
      </div>

      <ConfirmModal
        open={confirm === 'retire'}
        title={`退役「${s.name}」？`}
        body="退役后不能再改回其他状态，只能删除；名下节点不会自动迁走，请先把需要保留的节点复制到别的服务器。"
        confirmLabel="退役"
        tone="danger"
        onConfirm={() => {
          setConfirm(null)
          status.mutate({ server: s, to: 'retired', reason, done: '服务器已退役' })
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
      <SecretModal secret={secret && { title: secret.title, token: secret.token, command: secret.command, hint: `在这台机器上执行命令，按提示粘贴上面的令牌。${formatDateTime(secret.expiresAt)} 前有效，只显示这一次。` }} onClose={() => setSecret(null)} />
    </div>
  )
}
