import { useMutation } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { formatDateTime } from '../../../core/format'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Empty, Modal, QueryView, Tag, useToast } from '../../../ui'
import { canDeleteServer, createServerBody, deleteServerNotice, emptyServerForm, nodesByServer, quickToggle, SERVER_STATUS, serverDot, serverMeters, validateServerForm } from './infra'
import x from './infra.module.css'
import css from './nodes.module.css'
import { useFailure, useInvalidateNodes, useNodes, useServers } from './queries'
import { serverSchema, type Server } from './schemas'
import { Meters, ServerDetail, ServerFields } from './ServerDrawer'
import { useDeleteServer, useIssueServerToken, useSetServerStatus, type InstallSecret } from './serverActions'

export function ServersTab({ rest }: { rest: string[] }) {
  const servers = useServers()
  const nodes = useNodes()
  const [adding, setAdding] = useState(false)
  const [install, setInstall] = useState<InstallSecret | null>(null)
  const [removing, setRemoving] = useState<Server | null>(null)
  const token = useIssueServerToken(setInstall)
  const status = useSetServerStatus()
  // 删掉的服务器，它的安装命令也没用了
  const remove = useDeleteServer((gone) => setInstall((cur) => (cur?.serverId === gone.id ? null : cur)))
  const tags = useMemo(() => nodesByServer(nodes.data?.nodes ?? []), [nodes.data])

  const openId = rest[0] ?? null
  const open = servers.data?.find((s) => s.id === openId) ?? null

  return (
    <div className={css.stack}>
      <div className={x.bar}>
        <span>{servers.data ? `${servers.data.length} 台服务器 · 每台可承载多个协议节点` : '服务器'}</span>
        <span className={css.spacer} />
        <Button variant="primary" onClick={() => setAdding(true)}>
          添加服务器
        </Button>
      </div>

      <QueryView
        query={servers}
        rows={4}
        isEmpty={(d) => d.length === 0}
        empty={<Empty title="还没有服务器" description="添加一台服务器，拿到安装命令后在机器上执行即可接入。" />}
      >
        {(list) => (
          <div className={x.cards}>
            {list.map((s) => (
              <ServerCard
                key={s.id}
                server={s}
                nodes={tags.get(s.id) ?? []}
                busy={(token.isPending && token.variables?.id === s.id) || (status.isPending && status.variables?.server.id === s.id)}
                onOpen={() => navigate(`/nodes/servers/${s.id}`)}
                onToken={s.status !== 'retired' ? () => token.mutate(s) : null}
                onToggle={(t) => status.mutate({ server: s, to: t.to, done: t.done })}
                onRemove={() => setRemoving(s)}
              />
            ))}
          </div>
        )}
      </QueryView>

      <ServerDetail server={open} nodes={open ? tags.get(open.id) ?? [] : []} onClose={() => navigate('/nodes/servers')} />
      {install && <InstallModal secret={install} onClose={() => setInstall(null)} />}
      <AddServer
        open={adding}
        onClose={() => setAdding(false)}
        onCreated={(s) => {
          setAdding(false)
          token.mutate(s)
        }}
      />
      <ConfirmModal
        open={removing !== null}
        title={`删除服务器「${removing?.name ?? ''}」？`}
        body={removing ? deleteServerNotice(removing) : ''}
        confirmLabel="删除服务器"
        tone="danger"
        onConfirm={() => {
          if (removing) remove.mutate(removing)
          setRemoving(null)
        }}
        onCancel={() => setRemoving(null)}
      />
    </div>
  )
}

function ServerCard({
  server: s,
  nodes,
  busy,
  onOpen,
  onToken,
  onToggle,
  onRemove,
}: {
  server: Server
  nodes: readonly string[]
  busy: boolean
  onOpen: () => void
  onToken: (() => void) | null
  onToggle: (t: NonNullable<ReturnType<typeof quickToggle>>) => void
  onRemove: () => void
}) {
  const toggle = quickToggle(s.status)
  const status = SERVER_STATUS[s.status]
  return (
    <section className={`${x.card} ${s.status === 'retired' ? x.cardRetired : ''}`} aria-label={`服务器 ${s.name}`}>
      <button type="button" className={x.cardHead} onClick={onOpen} aria-label={`打开服务器 ${s.name} 详情`}>
        <span className={`${x.dot8} ${css[`dot_${serverDot(s)}`]}`} aria-hidden="true" />
        <span className={x.cardTitle}>
          <span className={`${x.cardName} ${css.ellipsis}`}>{s.name}</span>
          <span className={`${css.faint} ${css.ellipsis}`}>
            {s.region ?? '地区待识别'} · {s.public_ipv4 ?? '—'}
          </span>
        </span>
        {s.status !== 'ready' && <Tag tone={status.tone}>{status.label}</Tag>}
        <span className={x.agent}>{s.agent_version ?? '未连接'}</span>
      </button>
      <Meters meters={serverMeters(s)} />
      {nodes.length > 0 && (
        <div className={x.chips}>
          {nodes.map((n) => (
            <span key={n} className={x.chip}>
              {n}
            </span>
          ))}
        </div>
      )}
      <div className={x.capacity}>
        <span>
          在役 {s.serving_node_count} / 容量 {s.capacity_nodes}
        </span>
        {s.never_seen_node_count > 0 && <span className={x.neverSeen}>有 {s.never_seen_node_count} 个节点标了在役但从未心跳（多半没装 agent）</span>}
      </div>
      <div className={x.cardActions}>
        {onToken && (
          <Button size="xs" disabled={busy} onClick={onToken}>
            安装令牌
          </Button>
        )}
        {toggle && (
          <Button size="xs" disabled={busy} onClick={() => onToggle(toggle)}>
            {toggle.label}
          </Button>
        )}
        <span className={css.spacer} />
        <Button size="xs" variant="ghost" className={canDeleteServer(s) ? x.dangerText : undefined} disabled={!canDeleteServer(s) || busy} title={canDeleteServer(s) ? undefined : '只能删除草稿或已退役的服务器，先在详情「操作」里退役'} onClick={onRemove}>
          删除
        </Button>
      </div>
    </section>
  )
}

/** 安装 agent 弹窗：一条安装命令（含一次性令牌，脚本不再另要令牌）；关掉就再也看不到 */
function InstallModal({ secret, onClose }: { secret: InstallSecret; onClose: () => void }) {
  const toast = useToast()
  const [copied, setCopied] = useState(false)

  const copy = () =>
    navigator.clipboard.writeText(secret.command).then(
      () => {
        setCopied(true)
        toast('安装命令已复制')
      },
      () => toast('浏览器不允许写剪贴板，请手动选中复制', 'danger'),
    )

  return (
    <Modal
      open={true}
      onClose={onClose}
      size={560}
      eyebrow="安装 agent · 接入面板"
      title={secret.title}
      footerStart={
        <span className={x.installFooterNotice}>
          命令 {formatDateTime(secret.expiresAt)} 前有效，关闭后不再显示；过期或丢失可重新签发。
        </span>
      }
      actions={
        <Button size="dialog" variant="primary" onClick={onClose}>
          完成
        </Button>
      }
    >
      <div className={x.installSteps}>
        <div className={x.installStep}>
          <span className={x.installNo}>1</span>
          <div className={x.installContent}>
            <span className={x.installStepTitle}>登录服务器，用 root 执行安装命令</span>
            <div className={x.installBox}>
              <code className={x.installCode}>{secret.command}</code>
              <button type="button" className={`${x.installCopyBtn} ${copied ? x.installCopied : ''}`} onClick={() => void copy()}>
                {copied ? '已复制 ✓' : '复制'}
              </button>
            </div>
            <span className={x.installHint}>
              脚本会下载 agent、注册为系统服务，并用命令里的一次性令牌接入面板（接入一次即作废）。接入后地区、IP、系统与规格自动回填。
            </span>
          </div>
        </div>
      </div>
    </Modal>
  )
}

/** 添加服务器：只收名称、容量、备注；成功后由调用方签发安装令牌 */
function AddServer({ open, onClose, onCreated }: { open: boolean; onClose: () => void; onCreated: (s: Server) => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const [form, setForm] = useState(emptyServerForm)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const create = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.post('v1/servers', serverSchema, { body }),
    onSuccess: (s) => {
      setForm(emptyServerForm())
      setErrors({})
      toast(`服务器 ${s.name} 已添加为草稿，正在签发安装令牌`)
      void invalidate()
      onCreated(s)
    },
    onError: (error) => fail(error, setErrors),
  })
  const submit = () => {
    const found = validateServerForm(form)
    setErrors(found)
    if (!Object.keys(found).length) create.mutate(createServerBody(form))
  }
  return (
    <Modal
      open={open}
      onClose={onClose}
      size="md"
      title="添加服务器"
      eyebrow="建成草稿，接着签发安装令牌"
      actions={
        <>
          <Button size="dialog" variant="ghost" onClick={onClose}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={create.isPending} onClick={submit}>
            添加并生成安装命令
          </Button>
        </>
      }
    >
      <div className={css.stack}>
        <ServerFields form={form} errors={errors} fields={['name', 'capacity', 'notes']} disabled={create.isPending} onChange={setForm} />
        <div className={css.faint}>地区、IP、系统与规格在机器接入后由 agent 自动回填。生成安装令牌需要重新验证身份。</div>
      </div>
    </Modal>
  )
}
