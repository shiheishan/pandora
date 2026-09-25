/**
 * [INPUT]: 依赖 react 的 useState，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../core/format 的 formatDateTime，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./queries、./schemas，依赖 ./nodes.module.css
 * [OUTPUT]: 对外提供 NodeIdentity（节点抽屉「身份与令牌」标签）、SecretModal（一次性令牌展示框）
 * [POS]: admin/screens/nodes 抽屉的身份页（设计稿 d_identity）：上半 GET v1/nodes/{id}/identity（R46：mTLS 身份、服务端令牌是否签发与签发人、待用安装令牌数；令牌只存哈希，显示不了 srv_•••1a2b 前缀）；下半三个动作——签发一键安装令牌（POST bootstrap-token，30 分钟，令牌与命令分两块显示：命令从终端读令牌，不进 argv / history）、重签服务端令牌（POST server-token，旧令牌立即失效）、吊销身份（POST revoke-identity，不改服务状态，心跳停后自然离线）。三者都要 reauth，签发类带幂等键
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useState, type ReactNode } from 'react'
import { formatDateTime } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Modal, QueryView, Tag, useToast } from '../../../ui'
import css from './nodes.module.css'
import { useCan, useFailure, useIntentKey, useInvalidateNodes, useNodeIdentity } from './queries'
import { bootstrapTokenResponse, okResponse, serverTokenResponse, type NodeRow } from './schemas'

interface Secret {
  title: string
  token: string
  command: string
  hint: string
}

const IDENTITY_STATUS: Readonly<Record<string, [string, 'ok' | 'warn' | 'danger' | 'neutral']>> = {
  active: ['有效', 'ok'],
  rotating: ['轮换中', 'warn'],
  revoked: ['已吊销', 'danger'],
  expired: ['已过期', 'neutral'],
}

export function NodeIdentity({ node }: { node: NodeRow }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const identity = useNodeIdentity(node.id)
  const bootstrapIntent = useIntentKey()
  const serverIntent = useIntentKey()
  const [secret, setSecret] = useState<Secret | null>(null)
  const [confirm, setConfirm] = useState<'server' | 'revoke' | null>(null)
  const retired = node.serving_status === 'retired'

  const bootstrap = useMutation({
    mutationFn: () => {
      const body = { node_name: node.name, ttl_minutes: 30, ...(node.server_id ? { server_id: node.server_id } : {}) }
      return api.post('v1/nodes/bootstrap-token', bootstrapTokenResponse, { body, idempotencyKey: bootstrapIntent.keyFor([node.id, 'bootstrap']) })
    },
    onSuccess: (r) => {
      bootstrapIntent.reset()
      void invalidate()
      setSecret({ title: '一键安装令牌', token: r.token, command: r.install_command, hint: `在新机器上执行命令，按提示粘贴上面的令牌。${formatDateTime(r.expires_at)} 前有效，只显示这一次。` })
    },
    onError: (error) => fail(error, { intent: bootstrapIntent }),
  })

  const serverToken = useMutation({
    mutationFn: () => api.post(`v1/nodes/${node.id}/server-token`, serverTokenResponse, { idempotencyKey: serverIntent.keyFor([node.id, 'server-token']) }),
    onSuccess: (r) => {
      serverIntent.reset()
      void invalidate()
      setSecret({ title: '服务端令牌（UniProxy）', token: r.token, command: r.install_command, hint: `${r.hint}（面板地址 ${r.panel_url}，协议 ${r.node_type}）` })
    },
    onError: (error) => fail(error, { intent: serverIntent }),
  })

  const revoke = useMutation({
    mutationFn: () => api.post(`v1/nodes/${node.id}/revoke-identity`, okResponse),
    onSuccess: () => {
      toast('节点身份已吊销，心跳停止后会显示离线')
      void invalidate()
    },
    onError: (error) => fail(error),
  })

  return (
    <div className={css.stackLg}>
      <QueryView query={identity} rows={2} isEmpty={() => false} empty={null}>
        {(d) => (
          <dl className={css.facts}>
            <dt>节点身份</dt>
            <dd>
              {d.identity ? (
                <>
                  <span className={css.mono}>#{d.identity.serial}</span> <Tag tone={IDENTITY_STATUS[d.identity.status]?.[1] ?? 'neutral'}>{IDENTITY_STATUS[d.identity.status]?.[0] ?? d.identity.status}</Tag>
                  <div className={`${css.mono} ${css.faint} ${css.breakAll}`}>{d.identity.spiffe_id}</div>
                  <div className={css.faint}>
                    {formatDateTime(d.identity.issued_at)} 签发 · {formatDateTime(d.identity.expires_at)} 到期{d.identity.revoked_at ? ` · ${formatDateTime(d.identity.revoked_at)} 吊销` : ''}
                  </div>
                </>
              ) : (
                '还没有身份（节点从未接入）'
              )}
            </dd>
            <dt>服务端令牌</dt>
            <dd>{d.server_token.present ? '已签发（仅签发时可见）' : '未签发'}</dd>
            <dt>签发时间</dt>
            <dd>{d.server_token.issued_at ? `${formatDateTime(d.server_token.issued_at)} · ${d.server_token.issued_by_name ?? '经安装流程取得'}` : '—'}</dd>
            <dt>待用安装令牌</dt>
            <dd>{d.bootstrap_tokens_pending} 个未用未过期</dd>
          </dl>
        )}
      </QueryView>

      <Action title="签发一键安装令牌" detail="生成一条在新机器上执行的安装命令，30 分钟内有效。" disabledReason={retired ? '已退役的节点不能签发' : !can('node.provision') ? '需要节点部署权限' : null}>
        <Button size="sm" busy={bootstrap.isPending} onClick={() => bootstrap.mutate()}>
          签发
        </Button>
      </Action>
      <Action title="重签服务端令牌" detail="旧令牌立即失效，节点要换上新令牌才能继续上报。" disabledReason={retired ? '已退役的节点不能签发' : !can('node.provision') ? '需要节点部署权限' : null}>
        <Button size="sm" busy={serverToken.isPending} onClick={() => setConfirm('server')}>
          重签
        </Button>
      </Action>
      <Action title="吊销节点身份" detail="节点会被拒绝接入，需要重新注册。用于机器被入侵或转让。" disabledReason={!can('node.identity.revoke') ? '需要吊销身份权限' : null}>
        <Button size="sm" className={css.dangerButton} busy={revoke.isPending} onClick={() => setConfirm('revoke')}>
          吊销
        </Button>
      </Action>

      <ConfirmModal
        open={confirm === 'server'}
        title="重签服务端令牌？"
        body="旧令牌立即失效，节点在换上新令牌之前无法上报，会短暂离线。新令牌只显示一次。"
        confirmLabel="重签"
        onConfirm={() => {
          setConfirm(null)
          serverToken.mutate()
        }}
        onCancel={() => setConfirm(null)}
      />
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
      <SecretModal secret={secret} onClose={() => setSecret(null)} />
    </div>
  )
}

function Action({ title, detail, disabledReason, children }: { title: string; detail: string; disabledReason: string | null; children: ReactNode }) {
  return (
    <div className={css.action}>
      <div className={css.actionText}>
        <div className={css.actionTitle}>{title}</div>
        <div className={css.faint}>{disabledReason ?? detail}</div>
      </div>
      {!disabledReason && children}
    </div>
  )
}

/** 一次性令牌：令牌与命令分两块、各自可复制；关掉就再也看不到 */
export function SecretModal({ secret, onClose }: { secret: Secret | null; onClose: () => void }) {
  const toast = useToast()
  const copy = (value: string, what: string) =>
    navigator.clipboard.writeText(value).then(
      () => toast(`${what}已复制`),
      () => toast('浏览器不允许写剪贴板，请手动选中复制', 'danger'),
    )
  return (
    <Modal
      open={secret !== null}
      onClose={onClose}
      size="md"
      eyebrow={<span className={css.eyebrowWarn}>仅此一次可见</span>}
      title={secret?.title ?? ''}
      actions={
        <Button size="dialog" variant="primary" onClick={onClose} data-autofocus="">
          已保存，关闭
        </Button>
      }
    >
      {secret && (
        <div className={css.stack}>
          <div className={css.muted}>{secret.hint}</div>
          <div className={css.secretBlock}>
            <div className={css.secretHead}>
              <span>令牌</span>
              <Button size="xs" onClick={() => void copy(secret.token, '令牌')}>
                复制
              </Button>
            </div>
            <code className={css.code}>{secret.token}</code>
          </div>
          <div className={css.secretBlock}>
            <div className={css.secretHead}>
              <span>安装命令（执行后粘贴上面的令牌）</span>
              <Button size="xs" onClick={() => void copy(secret.command, '命令')}>
                复制
              </Button>
            </div>
            <code className={css.code}>{secret.command}</code>
          </div>
        </div>
      )}
    </Modal>
  )
}
