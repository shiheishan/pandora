/**
 * [INPUT]: 依赖 ../../../core/format 的 relativeTime，依赖 ../../../ui 的 Drawer / Empty / Table / Tag，依赖 ./api 的 BackupStatus / BackupFile，依赖 ./model 的 backupSummary / formatBytes / formatDateTime，依赖 ./Dash.module.css
 * [OUTPUT]: 对外提供 BackupDrawer
 * [POS]: 仪表盘系统状态第 8 行「数据库备份」的抽屉（待补·前端，后端有、设计缺）：原样展示 GET v1/system/status 的 backup 段——目录、可读性、份数与总量、最近一份、过期、缺校验、解密私钥、异地，以及 recent 列表；message / identity_hint 是后端写给运维的原文，照登不改
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { relativeTime } from '../../../core/format'
import { Drawer, Empty, Table, Tag, type TableColumn } from '../../../ui'
import type { BackupFile, BackupStatus } from './api'
import css from './Dash.module.css'
import { backupSummary, formatBytes, formatDateTime } from './model'

const yesNo = (v: boolean | undefined, yes: string, no: string) => (v === undefined ? '—' : v ? yes : no)

const COLUMNS: TableColumn<BackupFile>[] = [
  { key: 'name', header: '文件', mono: true, render: (f) => f.name },
  { key: 'size', header: '大小', align: 'right', render: (f) => formatBytes(f.size) },
  { key: 'at', header: '时间', render: (f) => <span title={formatDateTime(f.created_at)}>{relativeTime(f.created_at)}</span> },
  { key: 'sum', header: '校验', render: (f) => (f.has_checksum ? <Tag tone="ok">有</Tag> : <Tag tone="danger">缺失</Tag>) },
]

export function BackupDrawer({ open, onClose, backup }: { open: boolean; onClose: () => void; backup: BackupStatus }) {
  const summary = backupSummary(backup)
  const tone = summary.state === 'ok' ? 'ok' : summary.state === 'unknown' ? 'neutral' : 'warn'
  return (
    <Drawer open={open} onClose={onClose} width={640} title="数据库备份" subtitle={<Tag tone={tone}>{summary.meta}</Tag>}>
      <div className={css.drawerStack}>
        {backup.message && <p className={css.notice}>{backup.message}</p>}
        {backup.identity_hint && <p className={css.notice}>{backup.identity_hint}</p>}
        <dl className={css.facts}>
          <dt>备份目录</dt>
          <dd className={css.mono}>{backup.dir}</dd>
          <dt>面板能否读取</dt>
          <dd>{backup.readable ? '可读' : '读不到'}</dd>
          {backup.readable && (
            <>
              <dt>份数</dt>
              <dd>{backup.count ?? 0} 份{backup.total_bytes !== undefined ? ` · 合计 ${formatBytes(backup.total_bytes)}` : ''}</dd>
              <dt>最近一份</dt>
              <dd>
                {backup.latest ? (
                  <>
                    <span className={css.mono}>{backup.latest.name}</span>
                    <br />
                    {formatDateTime(backup.latest.created_at)}
                    {backup.latest_age_hours !== undefined ? `（${backup.latest_age_hours} 小时前）` : ''} · {formatBytes(backup.latest.size)}
                  </>
                ) : (
                  '还没有'
                )}
              </dd>
              <dt>是否过期</dt>
              <dd>{yesNo(backup.stale, '已过期（超过 48 小时没有新备份）', '正常')}</dd>
              <dt>缺校验文件</dt>
              <dd>{backup.missing_checksum === undefined ? '—' : backup.missing_checksum > 0 ? `${backup.missing_checksum} 份` : '无'}</dd>
              <dt>解密私钥</dt>
              <dd>{yesNo(backup.identity_configured, '已配置', '未配置')}</dd>
              <dt>异地备份</dt>
              <dd>{yesNo(backup.offsite_configured, '已配置 WebDAV', '未配置，只在本机')}</dd>
            </>
          )}
        </dl>
        {backup.readable && (
          <div>
            <h3 className={css.drawerTitle}>最近 5 份</h3>
            <Table
              label="最近 5 份备份"
              columns={COLUMNS}
              rows={backup.recent ?? []}
              rowKey={(f) => f.name}
              empty={<Empty bare title="还没有备份文件" description="备份定时器跑过一次后，这里会列出最近的几份。" />}
            />
          </div>
        )}
      </div>
    </Drawer>
  )
}
