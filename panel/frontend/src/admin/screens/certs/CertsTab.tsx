import { useState } from 'react'
import { formatDateTime } from '../../../core/format'
import { href, navigate } from '../../../core/router'
import { Button, Empty, QueryView, Table, Tag, type TableColumn } from '../../../ui'
import { CertDrawer } from './CertDrawer'
import { CertFormModal } from './CertFormModal'
import { errorCodeLabel, EXPIRY_VIEW, remainingText, statusView, summaryBanner } from './logic'
import { useCan, useCertificates } from './queries'
import type { Certificate } from './schemas'
import css from './certs.module.css'

const columns: TableColumn<Certificate>[] = [
  {
    key: 'name',
    header: '名称',
    width: '18%',
    render: (c) => (
      <span className={css.inline}>
        <span className={css.strong}>{c.name}</span>
        {c.wildcard && <Tag tone="outline">通配符</Tag>}
      </span>
    ),
  },
  {
    key: 'ids',
    header: '域名',
    render: (c) => (
      <span className={`${css.mono} ${css.ellipsis}`} title={c.identifiers.join('\n')}>
        {c.identifiers.join(', ')}
      </span>
    ),
  },
  {
    key: 'status',
    header: '状态',
    width: '140px',
    render: (c) => {
      const v = statusView(c)
      return <Tag tone={v.tone}>{v.label}</Tag>
    },
  },
  {
    key: 'expiry',
    header: '到期',
    width: '150px',
    render: (c) => {
      const v = EXPIRY_VIEW[c.expiry_level]
      return (
        <span className={css.cellStack} title={c.not_after ? formatDateTime(c.not_after) : undefined}>
          <Tag tone={v.tone}>{v.label}</Tag>
          {c.not_after && <span className={css.faint}>{remainingText(c.not_after)}</span>}
        </span>
      )
    },
  },
  {
    key: 'renew',
    header: '下次续期',
    width: '150px',
    render: (c) => <span className={css.faint}>{c.renew_after ? formatDateTime(c.renew_after) : '—'}</span>,
  },
  {
    key: 'error',
    header: '最近错误',
    width: '18%',
    render: (c) =>
      c.last_error_code ? (
        <span className={`${css.dangerText} ${css.ellipsis}`} title={c.last_error ?? undefined}>
          {errorCodeLabel(c.last_error_code)}
        </span>
      ) : (
        <span className={css.faint}>—</span>
      ),
  },
]

export function CertsTab({ rest }: { rest: string[] }) {
  const list = useCertificates()
  const can = useCan()
  const [creating, setCreating] = useState(false)
  const selected = rest[0] ?? null
  const writable = can('node.certificate.write')

  return (
    <div className={css.stack}>
      <div className={css.toolbar}>
        <span className={css.faint}>面板用 DNS-01 向 CA 集中签发与自动续期；节点拿到的私钥端到端加密（节点接入在后续版本）。</span>
        <span className={css.spacer} />
        {writable && (
          <Button variant="primary" onClick={() => setCreating(true)}>
            新建证书
          </Button>
        )}
      </div>
      <QueryView
        query={list}
        rows={5}
        isEmpty={(d) => d.items.length === 0}
        empty={
          <Empty
            title="还没有证书"
            description={
              <>
                先在 <a href={href('/certs/dns')}>DNS 凭据</a> 里添加域名所在 DNS 服务商的凭据，再新建证书；通配符证书一张可以给多台服务器用。
              </>
            }
          />
        }
      >
        {(data) => {
          const banner = summaryBanner(data.summary)
          return (
            <>
              {banner && <div className={css.alarm}>{banner}</div>}
              <div className={css.panel}>
                <Table columns={columns} rows={data.items} rowKey={(c) => c.id} label="证书" onRowClick={(c) => navigate(`/certs/list/${c.id}`)} />
              </div>
            </>
          )
        }}
      </QueryView>
      {creating && (
        <CertFormModal
          onClose={() => setCreating(false)}
          onSaved={(c) => {
            setCreating(false)
            navigate(`/certs/list/${c.id}`)
          }}
        />
      )}
      {selected && <CertDrawer id={selected} onClose={() => navigate('/certs/list', { replace: true })} />}
    </div>
  )
}
