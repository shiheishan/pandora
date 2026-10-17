import { formatDateTime } from '../../../core/format'
import { QueryView } from '../../../ui'
import { CopyButton, KvList, KvRow, SectionTitle } from '../../kit'
import css from './detail.module.css'
import { useNodeIdentity } from './queries'
import type { NodeRow } from './schemas'

const IDENTITY_STATUS: Readonly<Record<string, [string, 'ok' | 'warn' | 'danger' | 'neutral']>> = {
  active: ['有效', 'ok'],
  rotating: ['轮换中', 'warn'],
  revoked: ['已吊销', 'danger'],
  expired: ['已过期', 'neutral'],
}

export function NodeIdentity({ node }: { node: NodeRow }) {
  const identity = useNodeIdentity(node.id)

  return (
    <div className={css.stack}>
      <SectionTitle>身份</SectionTitle>
      <QueryView query={identity} rows={3} isEmpty={() => false} empty={null}>
        {(d) => (
          <KvList boxed labelWidth={128}>
            <KvRow
              label="节点 ID"
              action={<CopyButton value={node.id} label="复制节点 ID" />}
            >
              <span className={css.mono}>{node.id}</span>
            </KvRow>
            <KvRow label="身份">
              {d.identity ? (
                <span className={css.infoVal}>
                  <span>
                    <span className={IDENTITY_STATUS[d.identity.status]?.[1] === 'ok' ? css.okText : undefined}>
                      {IDENTITY_STATUS[d.identity.status]?.[0] ?? d.identity.status}
                    </span>
                    {` · #${d.identity.serial}`}
                  </span>
                  <span className={css.faint}>
                    签发 {formatDateTime(d.identity.issued_at)} · 到期 {formatDateTime(d.identity.expires_at)}
                    {d.identity.revoked_at ? ` · 吊销 ${formatDateTime(d.identity.revoked_at)}` : ''}
                  </span>
                </span>
              ) : (
                '还没有身份（节点从未接入）'
              )}
            </KvRow>
            <KvRow
              label="证书指纹"
              action={d.identity?.fingerprint_sha256 ? <CopyButton value={d.identity.fingerprint_sha256} label="复制证书指纹" /> : undefined}
            >
              <span className={`${css.mono} ${css.breakAll}`}>{d.identity?.fingerprint_sha256 ?? '—'}</span>
            </KvRow>
            <KvRow label="服务器待用令牌">
              <span className={css.infoVal}>
                {`${d.bootstrap_tokens_pending} 个`}
                <span className={css.faint}>安装令牌按服务器签发，到「服务器」页给所在服务器签发</span>
              </span>
            </KvRow>
          </KvList>
        )}
      </QueryView>
    </div>
  )
}
