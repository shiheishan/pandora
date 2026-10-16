import { RefreshIcon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useState } from 'react'
import { relativeTime } from '../../../core/format'
import { useSystemStatus } from './api'
import { BackupDrawer } from './BackupDrawer'
import { systemRows, type SystemRow } from './model'
import css from './Ops.module.css'
import { CardError, Dot, PanelHead, PanelSkeleton, targetHref } from './parts'

export function SystemStatus() {
  const q = useSystemStatus()
  const [backupOpen, setBackupOpen] = useState(false)
  const view = q.data ? systemRows(q.data) : null

  const updatedText = q.dataUpdatedAt ? relativeTime(new Date(q.dataUpdatedAt)) : ''
  const updatedDisplay = updatedText === '刚刚' ? '刚刚更新' : `${updatedText}前更新`

  return (
    <section className={css.systemSection} aria-labelledby="dash-system">
      <PanelHead id="dash-system" title="服务健康">
        <button
          type="button"
          onClick={() => void q.refetch()}
          className={css.refreshBtn}
          title="重新检查各项服务"
          aria-busy={q.isFetching}
        >
          <HugeiconsIcon
            icon={RefreshIcon}
            size={13}
            strokeWidth={2}
            className={q.isFetching ? css.spin : undefined}
            aria-hidden="true"
          />
          刷新
        </button>
      </PanelHead>

      {q.isError && !view ? (
        <CardError what="服务健康" error={q.error} onRetry={() => void q.refetch()} />
      ) : !view ? (
        <PanelSkeleton rows={8} />
      ) : (
        <>
          <div className={css.statusRow}>
            <Dot tone={view.tone} />
            <span className={`${css.statusLabel} ${view.tone === 'ok' ? css.statusLabelOk : css.statusLabelWarn}`}>
              {view.label}
            </span>
            <span className={css.statusDotSep}>·</span>
            <span className={css.statusTime}>{`${updatedDisplay} · 每分钟自动刷新`}</span>
          </div>

          {view.alert && (
            <div role="alert" className={css.alertBox}>
              <div className={css.alertTitle}>{view.alert.title}</div>
              <p className={css.alertBody}>{view.alert.body}</p>
            </div>
          )}

          {view.attention.length > 0 && (
            <div>
              <h4 className={css.groupSubTitle}>需要关注</h4>
              {view.attention.map((row) => (
                <AttentionRowItem key={row.key} row={row} onOpenBackup={() => setBackupOpen(true)} />
              ))}
            </div>
          )}

          {view.healthy.length > 0 && (
            <div className={css.healthyGroup}>
              <h4 className={css.groupSubTitle}>运行正常</h4>
              <dl className={css.healthyDl}>
                {view.healthy.map((row, index) => {
                  const isLast = index === view.healthy.length - 1
                  return (
                    <div key={row.key} className={`${css.healthyRow} ${isLast ? css.healthyRowLast : ''}`}>
                      <dt className={css.healthyDt}>
                        {row.key === 'backup' ? (
                          <button
                            type="button"
                            className={css.plainBtn}
                            onClick={() => setBackupOpen(true)}
                            aria-haspopup="dialog"
                          >
                            {row.name}
                          </button>
                        ) : (
                          row.name
                        )}
                      </dt>
                      <dd className={css.healthyDd} title={row.hint}>
                        {row.meta}
                      </dd>
                    </div>
                  )
                })}
              </dl>
            </div>
          )}

          <div className={css.systemFooter}>{view.footer}</div>
        </>
      )}

      {q.data && <BackupDrawer open={backupOpen} onClose={() => setBackupOpen(false)} backup={q.data.backup} />}
    </section>
  )
}

function AttentionRowItem({ row, onOpenBackup }: { row: SystemRow; onOpenBackup: () => void }) {
  const metaColorClass =
    row.state === 'warn' ? css.metaWarn : row.state === 'down' ? css.metaDown : css.metaUnknown

  const content = (
    <>
      <span className={css.attentionName}>{row.name}</span>
      <span className={`${css.attentionMeta} ${metaColorClass}`}>{row.meta}</span>
    </>
  )

  if (row.action?.kind === 'backup') {
    return (
      <button
        type="button"
        className={`${css.attentionRow} ${css.clickableRow}`}
        onClick={onOpenBackup}
        title={row.hint}
        aria-haspopup="dialog"
      >
        {content}
      </button>
    )
  }

  if (row.action?.kind === 'go') {
    return (
      <a
        href={targetHref(row.action.target)}
        className={`${css.attentionRow} ${css.clickableRow}`}
        title={row.hint}
      >
        {content}
      </a>
    )
  }

  return (
    <div className={css.attentionRow} title={row.hint}>
      {content}
    </div>
  )
}
