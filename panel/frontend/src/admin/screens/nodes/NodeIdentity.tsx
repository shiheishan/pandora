import { useMutation } from '@tanstack/react-query'
import { useState, type ReactNode } from 'react'
import { formatDateTime } from '../../../core/format'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Modal, QueryView, Tag, useToast } from '../../../ui'
import css from './nodes.module.css'
import { useFailure, useInvalidateNodes, useNodeIdentity } from './queries'
import { okResponse, type NodeRow } from './schemas'

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
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const identity = useNodeIdentity(node.id)
  const [confirm, setConfirm] = useState<'revoke' | null>(null)

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
            <dt>服务器待用安装令牌</dt>
            <dd>{d.bootstrap_tokens_pending} 个未用未过期</dd>
          </dl>
        )}
      </QueryView>

      {node.server_id ? (
        <Action title="安装令牌" detail="安装令牌按服务器签发：到「服务器」页打开这台节点所在的服务器签发，一台服务器接入一次，上面的节点都跟着服务。">
          <Button size="sm" onClick={() => navigate(`/nodes/servers/${node.server_id}`)}>
            去服务器
          </Button>
        </Action>
      ) : (
        // 没有服务器（如所在服务器被删、节点被摘下）：编辑表单里服务器只读，要到「操作」里迁移（草稿）或复制到一台服务器
        <Action title="安装令牌" detail="节点还没放到服务器上：到「操作」里迁移（仅草稿）或复制到一台服务器，再到那台服务器签发安装令牌。">
          <Button size="sm" onClick={() => navigate(`/nodes/nodes/${node.id}/ops`)}>
            去操作
          </Button>
        </Action>
      )}
      <Action title="吊销节点身份" detail="节点会被拒绝接入，需要重新注册。用于机器被入侵或转让。">
        <Button size="sm" className={css.dangerButton} busy={revoke.isPending} onClick={() => setConfirm('revoke')}>
          吊销
        </Button>
      </Action>

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
    </div>
  )
}

function Action({ title, detail, disabledReason, children }: { title: string; detail: string; disabledReason?: string | null; children: ReactNode }) {
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
