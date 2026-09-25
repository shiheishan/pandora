/**
 * [INPUT]: 依赖 react 的 useMemo / useState，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../core/format 的 formatDateTime，依赖 ../../../core/router 的 navigate，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./infra 的服务器纯函数，依赖 ./queries、./schemas、./serverActions，依赖 ./ServerDrawer 的 ServerDrawer / ServerFields / Meters，依赖 ./nodes.module.css 与 ./infra.module.css
 * [OUTPUT]: 对外提供 ServersTab（节点与服务器 · 服务器标签）
 * [POS]: admin/screens/nodes 的服务器卡片网格（设计稿 t_servers）：圆点（ready 且有心跳绿、心跳断红、其余灰）、名称 / 地区 · IP / agent、CPU · 内存 · 磁盘三条占用、节点标签（GET v1/nodes 按 server_id 分组）、在役 / 容量与从未心跳提示（契约待补·前端）；卡片三个动作：安装令牌（顶部深色命令条，令牌与命令分开、仅此一次）、标记维护 / 恢复服务 / 投入服务（映射到合法边）、删除（仅草稿或已退役，文案按后端级联静默改写）。「添加服务器」改为弹窗收名称、容量、备注，建成后紧接着签发安装令牌。点卡片名进详情抽屉，服务器 id 与抽屉标签记在 rest
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { formatDateTime } from '../../../core/format'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Empty, Modal, QueryView, Tag, useToast } from '../../../ui'
import { canDeleteServer, createServerBody, deleteServerNotice, emptyServerForm, nodesByServer, quickToggle, SERVER_STATUS, serverDot, serverMeters, validateServerForm } from './infra'
import x from './infra.module.css'
import css from './nodes.module.css'
import { useCan, useFailure, useInvalidateNodes, useNodes, useServers } from './queries'
import { serverSchema, type Server } from './schemas'
import { Meters, SERVER_TABS, ServerDrawer, ServerFields, type ServerTab } from './ServerDrawer'
import { useDeleteServer, useIssueServerToken, useSetServerStatus, type InstallSecret } from './serverActions'

export function ServersTab({ rest }: { rest: string[] }) {
  const can = useCan()
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
  const tab = (SERVER_TABS.find(([k]) => k === rest[1])?.[0] ?? 'overview') as ServerTab
  const open = servers.data?.find((s) => s.id === openId) ?? null

  return (
    <div className={css.stack}>
      <div className={x.bar}>
        <span>{servers.data ? `${servers.data.length} 台服务器 · 每台可承载多个协议节点` : '服务器'}</span>
        <span className={css.spacer} />
        {can('node.write') && (
          <Button variant="primary" onClick={() => setAdding(true)}>
            添加服务器
          </Button>
        )}
      </div>

      {install && <InstallBar secret={install} onClose={() => setInstall(null)} />}

      <QueryView
        query={servers}
        rows={4}
        isEmpty={(d) => d.length === 0}
        empty={<Empty title="还没有服务器" description={can('node.write') ? '添加一台服务器，拿到安装命令后在机器上执行即可接入。' : '有节点编辑权限的同事可以在这里添加服务器。'} />}
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
                onToken={can('node.provision') && s.status !== 'retired' ? () => token.mutate(s) : null}
                onToggle={
                  can('node.lifecycle')
                    ? (t) => {
                        status.mutate({ server: s, to: t.to, done: t.done })
                      }
                    : null
                }
                onRemove={can('node.lifecycle') ? () => setRemoving(s) : null}
              />
            ))}
          </div>
        )}
      </QueryView>

      <ServerDrawer server={open} tab={tab} onClose={() => navigate('/nodes/servers')} />
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
  onToggle: ((t: NonNullable<ReturnType<typeof quickToggle>>) => void) | null
  onRemove: (() => void) | null
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
      {(onToken || (onToggle && toggle) || onRemove) && (
        <div className={x.cardActions}>
          {onToken && (
            <Button size="xs" disabled={busy} onClick={onToken}>
              安装令牌
            </Button>
          )}
          {onToggle && toggle && (
            <Button size="xs" disabled={busy} onClick={() => onToggle(toggle)}>
              {toggle.label}
            </Button>
          )}
          <span className={css.spacer} />
          {onRemove && (
            <Button size="xs" variant="ghost" className={canDeleteServer(s) ? x.dangerText : undefined} disabled={!canDeleteServer(s) || busy} title={canDeleteServer(s) ? undefined : '只能删除草稿或已退役的服务器，先在详情「操作」里退役'} onClick={onRemove}>
              删除
            </Button>
          )}
        </div>
      )}
    </section>
  )
}

/** 顶部深色命令条：令牌与命令分两行、各自复制；关掉就再也看不到 */
function InstallBar({ secret, onClose }: { secret: InstallSecret; onClose: () => void }) {
  const toast = useToast()
  const copy = (value: string, what: string) =>
    navigator.clipboard.writeText(value).then(
      () => toast(`${what}已复制`),
      () => toast('浏览器不允许写剪贴板，请手动选中复制', 'danger'),
    )
  return (
    <div className={x.installBar} role="region" aria-label="安装命令">
      <div className={x.installHead}>
        <span className={x.installTitle}>{secret.title}</span>
        <span className={x.installWarn}>令牌 {formatDateTime(secret.expiresAt)} 前有效 · 仅显示一次</span>
        <span className={css.spacer} />
        <button type="button" className={x.barClose} aria-label="关闭安装命令" onClick={onClose}>
          ×
        </button>
      </div>
      <div className={x.installRow}>
        <span className={x.installLabel}>命令</span>
        <code className={x.installCode}>{secret.command}</code>
        <button type="button" className={x.barButton} onClick={() => void copy(secret.command, '安装命令')}>
          复制
        </button>
      </div>
      <div className={x.installRow}>
        <span className={x.installLabel}>令牌</span>
        <code className={x.installCode}>{secret.token}</code>
        <button type="button" className={x.barButton} onClick={() => void copy(secret.token, '令牌')}>
          复制
        </button>
      </div>
      <div className={x.installNote}>在机器上执行命令，按提示粘贴令牌（令牌不进命令行，不留在 shell 历史里）。</div>
    </div>
  )
}

/** 添加服务器：契约改设计「点一下生成 new-edge-N」为弹窗，只收名称、容量、备注；成功后由调用方签发安装令牌 */
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
