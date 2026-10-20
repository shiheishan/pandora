import { useMutation } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { MoreHorizontalIcon, PlusSignIcon } from '@hugeicons-pro/core-stroke-rounded'
import { formatDateTime } from '../../../core/format'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Checkbox, ConfirmModal, Empty, IconButton, Modal, QueryView, Tag, useToast } from '../../../ui'
import { BulkAction, BulkBar, PageActions, StatusDot } from '../../kit'
import {
  createServerBody,
  deleteServerNotice,
  emptyServerForm,
  lastUpgradeLine,
  nodesByServer,
  pruneUpgradePicks,
  quickToggle,
  SERVER_STATUS,
  serverDeleteBlock,
  serverMeters,
  serverNotes,
  serverSubtitle,
  upgradeBlockNote,
  upgradeConfirmBody,
  upgradeOffer,
  upgradeSelectable,
  upgradeTarget,
  validateServerForm,
} from './infra'
import x from './infra.module.css'
import css from './nodes.module.css'
import { useFailure, useInvalidateNodes, useNodes, useServers } from './queries'
import { serverSchema, type Server } from './schemas'
import { Meters, serverStatusTone, ServerFields } from './ServerDetailPage'
import { useDeleteServer, useIssueServerToken, useSetServerStatus, useUpgradeServers, type InstallSecret } from './serverActions'

export function ServersTab() {
  const servers = useServers()
  const nodes = useNodes()
  const [adding, setAdding] = useState(false)
  const [install, setInstall] = useState<InstallSecret | null>(null)
  const [removing, setRemoving] = useState<Server | null>(null)
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [confirmUpgrade, setConfirmUpgrade] = useState(false)
  const token = useIssueServerToken(setInstall)
  const status = useSetServerStatus()
  const upgrade = useUpgradeServers(() => setPicked(new Set()))
  // 删掉的服务器，它的安装命令也没用了
  const remove = useDeleteServer((gone) => setInstall((cur) => (cur?.serverId === gone.id ? null : cur)))
  const tags = useMemo(() => nodesByServer(nodes.data?.nodes ?? []), [nodes.data])
  const list = useMemo(() => servers.data ?? [], [servers.data])
  const target = useMemo(() => upgradeTarget(list), [list])
  const live = useMemo(() => pruneUpgradePicks(picked, list), [picked, list])
  const chosen = useMemo(() => list.filter((s) => live.has(s.id)), [list, live])

  const setPick = (id: string, on: boolean) => {
    setPicked((prev) => {
      const next = new Set(prev)
      if (on) next.add(id)
      else next.delete(id)
      return next
    })
  }

  return (
    <div className={css.stack}>
      <PageActions>
        <Button variant="primary" icon={PlusSignIcon} onClick={() => setAdding(true)}>
          添加服务器
        </Button>
      </PageActions>
      {target && live.size > 0 ? (
        <BulkBar count={live.size} onClear={() => setPicked(new Set())}>
          <BulkAction busy={upgrade.isPending} onClick={() => setConfirmUpgrade(true)}>
            升级到 {target}（{live.size} 台）
          </BulkAction>
        </BulkBar>
      ) : (
        <div className={x.bar}>
          <span>{servers.data ? `${servers.data.length} 台服务器 · 每台可承载多个协议节点` : '服务器'}</span>
        </div>
      )}

      <QueryView
        query={servers}
        rows={4}
        isEmpty={(d) => d.length === 0}
        empty={<Empty title="还没有服务器" description="添加一台服务器，拿到安装命令后在机器上执行即可接入。" />}
      >
        {(list) => (
          <div className={x.cards}>
            {list.map((s) => {
              const canPick = target !== null && upgradeSelectable(s)
              return (
                <ServerCard
                  key={s.id}
                  server={s}
                  nodes={tags.get(s.id) ?? []}
                  picked={live.has(s.id)}
                  onPick={canPick ? (on) => setPick(s.id, on) : undefined}
                  busy={(token.isPending && token.variables?.id === s.id) || (status.isPending && status.variables?.server.id === s.id)}
                  onOpen={() => navigate(`/nodes/servers/${s.id}`)}
                  onToken={() => token.mutate(s)}
                  onToggle={(t) => status.mutate({ server: s, to: t.to, done: t.done })}
                  onRemove={() => setRemoving(s)}
                />
              )
            })}
          </div>
        )}
      </QueryView>

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
      <ConfirmModal
        open={confirmUpgrade && chosen.length > 0 && !!target}
        title={`升级 ${chosen.length} 台服务器到 ${target ?? ''}？`}
        body={target ? upgradeConfirmBody(chosen.length, target) : ''}
        confirmLabel="开始升级"
        onConfirm={() => {
          setConfirmUpgrade(false)
          upgrade.mutate(chosen)
        }}
        onCancel={() => setConfirmUpgrade(false)}
      />
    </div>
  )
}

function ServerCard({
  server: s,
  nodes,
  picked,
  onPick,
  busy,
  onOpen,
  onToken,
  onToggle,
  onRemove,
}: {
  server: Server
  nodes: readonly string[]
  picked: boolean
  onPick?: (on: boolean) => void
  busy: boolean
  onOpen: () => void
  onToken: () => void
  onToggle: (t: NonNullable<ReturnType<typeof quickToggle>>) => void
  onRemove: () => void
}) {
  const toggle = quickToggle(s.status)
  const status = SERVER_STATUS[s.status]
  const meters = serverMeters(s)
  const allMetersNull = meters.every((m) => m.percent === null)
  const notes = serverNotes(s)
  const blockNote = upgradeBlockNote(s)
  const noteLines = blockNote ? [...notes, blockNote] : notes
  const offer = upgradeOffer(s)
  const upgradeLine = lastUpgradeLine(s)
  const block = serverDeleteBlock(s)
  return (
    <section className={`${x.card} ${s.status === 'retired' ? x.cardRetired : ''}`} aria-label={`服务器 ${s.name}`}>
      {/* 头：状态点包等宽名称 + 状态标签 + 右上角「…」打开详情（整头不再是按钮） */}
      <div className={x.ch}>
        {onPick ? (
          <span className={x.pick}>
            <Checkbox small aria-label={`选择 ${s.name} 升级`} checked={picked} onChange={(e) => onPick(e.target.checked)} />
          </span>
        ) : null}
        <StatusDot tone={serverStatusTone(s)}>
          <span className={css.mono}>{s.name}</span>
        </StatusDot>
        {s.status !== 'ready' && <Tag tone={status.tone}>{status.label}</Tag>}
        <span className={css.spacer} />
        <IconButton label={`${s.name} 详情`} icon={MoreHorizontalIcon} size="sm" onClick={onOpen} />
      </div>
      <div className={x.cs}>{serverSubtitle(s)}</div>
      {offer ? (
        <div className={x.cs}>
          <Tag tone="info">{offer}</Tag>
        </div>
      ) : null}
      {allMetersNull ? <div className={css.faint}>装好 agent 后这里显示占用</div> : <Meters meters={meters} />}
      <div className={x.chips}>
        {nodes.length > 0 ? (
          nodes.map((n) => <Tag key={n}>{n}</Tag>)
        ) : (
          <span className={css.faint}>没有在役节点</span>
        )}
      </div>
      {noteLines.map((n) => (
        <div key={n} className={x.note}>
          {n}
        </div>
      ))}
      {upgradeLine ? (
        <div className={x.note}>
          <Tag tone={upgradeLine.tone}>{upgradeLine.label}</Tag> {upgradeLine.text}
        </div>
      ) : null}
      <div className={x.cf}>
        <span className={css.mono}>
          在役 {s.serving_node_count} / 容量 {s.capacity_nodes}
        </span>
        <span className={css.spacer} />
        {s.status !== 'retired' && (
          <Button size="sm" variant="secondary" disabled={busy} onClick={onToken}>
            安装令牌
          </Button>
        )}
        {toggle && (
          <Button size="sm" variant="secondary" disabled={busy} onClick={() => onToggle(toggle)}>
            {toggle.label}
          </Button>
        )}
        <Button size="sm" variant="secondary" disabled={block !== null || busy} title={block ?? undefined} onClick={onRemove}>
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
