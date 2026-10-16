import { formatDateTime } from '../../../core/format'
import { Button, Modal, QueryView, Tag, useToast } from '../../../ui'
import css from './detail.module.css'
import { useNodeIdentity } from './queries'
import type { NodeRow } from './schemas'

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
  const toast = useToast()
  const identity = useNodeIdentity(node.id)

  const copy = (value: string, key: string) =>
    navigator.clipboard.writeText(value).then(
      () => toast(`已复制${key}`),
      () => toast('浏览器不允许写剪贴板，请手动选中复制', 'danger'),
    )

  return (
    <section className={css.section}>
      <h4 className={css.sectionTitle}>身份与令牌</h4>
      <QueryView query={identity} rows={3} isEmpty={() => false} empty={null}>
        {(d) => (
          <div className={css.rowTable}>
            <div className={css.infoRow}>
              <span className={css.infoKey}>节点 ID</span>
              <span className={`${css.mono} ${css.breakAll}`}>{node.id}</span>
              <button type="button" className={css.copyBtn} onClick={() => void copy(node.id, '节点 ID')}>
                复制
              </button>
            </div>
            <div className={css.infoRow}>
              <span className={css.infoKey}>节点身份</span>
              <div className={css.infoVal}>
                {d.identity ? (
                  <>
                    <span className={css.valLine}>
                      <span className={css.mono}>#{d.identity.serial}</span>
                      <Tag tone={IDENTITY_STATUS[d.identity.status]?.[1] ?? 'neutral'}>{IDENTITY_STATUS[d.identity.status]?.[0] ?? d.identity.status}</Tag>
                    </span>
                    <span className={css.faint}>
                      签发 {formatDateTime(d.identity.issued_at)} · 到期 {formatDateTime(d.identity.expires_at)}
                      {d.identity.revoked_at ? ` · 吊销 ${formatDateTime(d.identity.revoked_at)}` : ''}
                    </span>
                  </>
                ) : (
                  '还没有身份（节点从未接入）'
                )}
              </div>
              <span />
            </div>
            <div className={css.infoRow}>
              <span className={css.infoKey}>证书指纹</span>
              <span className={`${css.mono} ${css.breakAll}`}>{d.identity?.fingerprint_sha256 ?? '—'}</span>
              {d.identity?.fingerprint_sha256 ? (
                <button type="button" className={css.copyBtn} onClick={() => void copy(d.identity!.fingerprint_sha256, '证书指纹')}>
                  复制
                </button>
              ) : (
                <span />
              )}
            </div>
            <div className={css.infoRow}>
              <span className={css.infoKey}>服务器待用安装令牌</span>
              <span>{d.bootstrap_tokens_pending} 个未用未过期</span>
              <span />
            </div>
          </div>
        )}
      </QueryView>
      <p className={css.sectionNote}>安装令牌按服务器签发：到「服务器」页给这台节点所在的服务器签发。</p>
    </section>
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
      eyebrow={<span style={{ color: 'var(--warn)' }}>仅此一次可见</span>}
      title={secret?.title ?? ''}
      actions={
        <Button size="dialog" variant="primary" onClick={onClose} data-autofocus="">
          已保存，关闭
        </Button>
      }
    >
      {secret && (
        <div className={css.stack}>
          <div className={css.faint}>{secret.hint}</div>
          <div>
            <div className={css.valLine}>
              <span>令牌</span>
              <Button size="xs" onClick={() => void copy(secret.token, '令牌')}>
                复制
              </Button>
            </div>
            <code className={`${css.mono} ${css.breakAll}`}>{secret.token}</code>
          </div>
          <div>
            <div className={css.valLine}>
              <span>安装命令（执行后粘贴上面的令牌）</span>
              <Button size="xs" onClick={() => void copy(secret.command, '命令')}>
                复制
              </Button>
            </div>
            <code className={`${css.mono} ${css.breakAll}`}>{secret.command}</code>
          </div>
        </div>
      )}
    </Modal>
  )
}
