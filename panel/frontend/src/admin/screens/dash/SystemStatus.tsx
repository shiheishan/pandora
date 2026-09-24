/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../ui 的 Tag，依赖 ../../modules 的 Permissions，依赖 ./api 的 useSystemStatus，依赖 ./model 的 systemRows / SystemRow，依赖 ./BackupDrawer，依赖 ./parts，依赖 ./Dash.module.css
 * [OUTPUT]: 对外提供 SystemStatus
 * [POS]: 仪表盘「系统状态」面板：GET v1/system/status。总状态胶囊 + 7 行组件（components 待补·后端，未上之前只有数据库一行来自现有 database 字段）+ 待补·前端的第 8 行「数据库备份」，点开 BackupDrawer；邮件、Telegram、调度三行可点去对应模块
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { Tag } from '../../../ui'
import type { Permissions } from '../../modules'
import { useSystemStatus } from './api'
import { BackupDrawer } from './BackupDrawer'
import css from './Dash.module.css'
import { systemRows, type SystemRow, type Tone } from './model'
import { CardError, Dot, isForbidden, PanelSkeleton, targetHref } from './parts'

const STATE_TONE: Record<SystemRow['state'], Tone> = { ok: 'ok', warn: 'warn', down: 'danger', unknown: 'neutral' }

export function SystemStatus({ perms }: { perms: Permissions }) {
  const q = useSystemStatus(true)
  const [backupOpen, setBackupOpen] = useState(false)
  if (q.isError && isForbidden(q.error)) return null

  const view = q.data ? systemRows(q.data, perms) : null

  return (
    <section className={`${css.panel} ${css.narrow}`} aria-labelledby="dash-system">
      <div className={`${css.panelHead} ${css.panelHeadRule}`}>
        <h2 id="dash-system" className={css.panelTitle}>
          系统状态
        </h2>
        <div className={css.spacer} />
        {view && <Tag tone={view.tone}>{view.label}</Tag>}
      </div>
      {q.isError && !view ? (
        <CardError what="系统状态" error={q.error} onRetry={() => void q.refetch()} />
      ) : !view ? (
        <div className={css.sysSkeleton}>
          <PanelSkeleton rows={8} />
        </div>
      ) : (
        <div>
          {view.rows.map((row) => (
            <Row key={row.key} row={row} perms={perms} onBackup={() => setBackupOpen(true)} />
          ))}
        </div>
      )}
      {q.data && <BackupDrawer open={backupOpen} onClose={() => setBackupOpen(false)} backup={q.data.backup} />}
    </section>
  )
}

function Row({ row, perms, onBackup }: { row: SystemRow; perms: Permissions; onBackup: () => void }) {
  const tone = STATE_TONE[row.state]
  const metaClass = row.state === 'warn' ? `${css.sysMeta} ${css.sysMetaWarn}` : row.state === 'down' ? `${css.sysMeta} ${css.sysMetaDanger}` : css.sysMeta
  const body = (
    <>
      <Dot tone={tone} />
      <span className={css.sysName}>{row.name}</span>
      <span className={metaClass}>{row.meta}</span>
    </>
  )
  if (row.action?.kind === 'backup') {
    return (
      <button type="button" className={css.sysRow} onClick={onBackup} title={row.hint} aria-haspopup="dialog">
        {body}
      </button>
    )
  }
  if (row.action?.kind === 'go') {
    return (
      <a className={css.sysRow} href={targetHref(row.action.target, perms)} title={row.hint}>
        {body}
      </a>
    )
  }
  return (
    <div className={css.sysRow} title={row.hint}>
      {body}
    </div>
  )
}
