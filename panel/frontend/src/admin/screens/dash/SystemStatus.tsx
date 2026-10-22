import { ArrowRight01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useState } from 'react'
import { relativeTime } from '../../../core/format'
import { Card, Tag } from '../../../ui'
import { StatusDot } from '../../kit'
import { offlineNodeNames, useDashboardTasks } from '../../tasks'
import { useSystemStatus } from './api'
import { BackupDrawer } from './BackupDrawer'
import css from './Ops.module.css'
import { CardError, PanelSkeleton, targetHref } from './parts'
import { systemView, type SystemRow } from './system'

export function SystemStatus() {
  const q = useSystemStatus()
  const [backupOpen, setBackupOpen] = useState(false)
  const tasks = useDashboardTasks()
  const view = q.data ? systemView(q.data, offlineNodeNames(tasks.data?.items)) : null

  // relativeTime：刚刚 / N 分钟 / N 小时 / MM-DD；只有前两种后面补「前」
  const updatedRaw = q.dataUpdatedAt ? relativeTime(new Date(q.dataUpdatedAt)) : ''
  const updatedText = /(分钟|小时)$/.test(updatedRaw) ? `${updatedRaw}前` : updatedRaw

  const footer = (
    <>
      <span className={css.footText}>{view ? `每分钟检查 · ${updatedText}` : '每分钟检查'}</span>
      <span className={css.footSpacer} />
      <button
        type="button"
        className={css.footLink}
        aria-haspopup="dialog"
        disabled={!q.data}
        onClick={() => setBackupOpen(true)}
      >
        备份详情
        <HugeiconsIcon icon={ArrowRight01Icon} size={14} strokeWidth={1.8} aria-hidden="true" />
      </button>
    </>
  )

  return (
    <>
      <Card
        flush
        title="系统状态"
        extra={view ? <Tag tone={view.tone}>{view.label}</Tag> : undefined}
        footer={footer}
      >
        {q.isError && !view ? (
          <CardError what="系统状态" error={q.error} onRetry={() => void q.refetch()} />
        ) : !view ? (
          <div className={css.skeletonWrap}>
            <PanelSkeleton rows={8} height={22} />
          </div>
        ) : (
          <div className={css.body}>
            <div className={css.headerRow} aria-hidden="true">
              <span>组件</span>
              <span>说明</span>
              <span className={css.alignRight}>指标</span>
            </div>
            <div className={css.list}>
              {view.rows.map((row) => (
                <SystemRowItem key={row.key} row={row} onOpenBackup={() => setBackupOpen(true)} />
              ))}
            </div>
          </div>
        )}
      </Card>

      {q.data && <BackupDrawer open={backupOpen} onClose={() => setBackupOpen(false)} backup={q.data.backup} />}
    </>
  )
}

function SystemRowItem({ row, onOpenBackup }: { row: SystemRow; onOpenBackup: () => void }) {
  const rowClass = [css.row, row.attention ? css.rowAttention : css.rowOk, row.action ? css.rowClickable : '']
    .filter(Boolean)
    .join(' ')

  const inner = (
    <>
      <StatusDot tone={row.tone}>{row.name}</StatusDot>
      <span className={css.why} title={row.why}>{row.why}</span>
      <span className={css.metric}>{row.metric}</span>
    </>
  )

  if (row.action?.kind === 'backup') {
    return (
      <button type="button" className={rowClass} aria-haspopup="dialog" onClick={onOpenBackup}>
        {inner}
      </button>
    )
  }

  if (row.action?.kind === 'go') {
    return (
      <a href={targetHref(row.action.target)} className={rowClass}>
        {inner}
      </a>
    )
  }

  return <div className={rowClass}>{inner}</div>
}
