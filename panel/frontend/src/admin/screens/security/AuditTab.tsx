/**
 * [INPUT]: 依赖 react 的 useEffect / useState，依赖 ../../../core/download 的 saveFile / filenameFromDisposition，依赖 ../../../core/format 的 formatCount / formatDateTime，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./logic 的审计函数，依赖 ./queries（含导出后失效审计列表），依赖 ./schemas 的枚举，依赖 ./security.module.css
 * [OUTPUT]: 对外提供 AuditTab（安全与运维 · 审计日志标签）
 * [POS]: admin/screens/security 的审计日志（设计稿 t_audit）：搜索框「操作人、动作或对象」（300ms 防抖送 q）+ 契约待补·前端的三个筛选（动作前缀、操作者类型、结果），表格六列（时间、操作人、动作 + 非成功结果标签、对象 + 原因提示、来源 IP、认证方式；固定布局、最小 840，960 下面板内横滚），按 total 分页。
 *        导出（security.audit.read + ops.export + reauth，R44）：弹窗选可选的起止日期，与当前筛选一起送 GET v1/audit/export，经 requestRaw 取 CSV 存文件；日期先按后端同一套规则校验，超过 5 万行的 422 原样提示。存量行（00080 之前）没有认证方式与来源 IP，显示「—」
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useEffect, useState } from 'react'
import { filenameFromDisposition, saveFile } from '../../../core/download'
import { formatCount, formatDateTime } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, Empty, Input, Modal, Pager, QueryView, Select, Table, Tag, useToast, type TableColumn } from '../../../ui'
import { ACTOR_LABEL, actorLabel, AUDIT_PAGE, authLabel, clockTime, EMPTY_AUDIT_FILTER, exportQuery, hasAuditFilter, objectLabel, OUTCOME_LABEL, OUTCOME_TONE, validateExportRange, type AuditFilter } from './logic'
import { useAudit, useCan, useFailure, useInvalidateSecurity } from './queries'
import { ACTOR_KINDS, OUTCOMES, type ActorKind, type AuditEvent, type Outcome } from './schemas'
import css from './security.module.css'

const columns: TableColumn<AuditEvent>[] = [
  {
    key: 'at',
    header: '时间',
    width: '128px',
    render: (e) => (
      <span className={css.time} title={formatDateTime(e.occurred_at)}>
        {clockTime(e.occurred_at)}
      </span>
    ),
  },
  {
    key: 'who',
    header: '操作人',
    width: '20%',
    render: (e) => (
      <span className={`${css.ellipsis} ${css.cellWho}`} title={`${ACTOR_LABEL[e.actor_kind]}${e.actor_email ? ` · ${e.actor_email}` : ''}`}>
        {actorLabel(e)}
      </span>
    ),
  },
  {
    key: 'action',
    header: '动作',
    render: (e) => (
      <span className={css.actionCell}>
        <span className={`${css.mono} ${css.ellipsis}`} title={e.action}>
          {e.action}
        </span>
        {e.outcome !== 'success' && <Tag tone={OUTCOME_TONE[e.outcome]}>{OUTCOME_LABEL[e.outcome]}</Tag>}
      </span>
    ),
  },
  {
    key: 'object',
    header: '对象',
    render: (e) => (
      <span className={`${css.ellipsis} ${css.cellObject} ${css.muted}`} title={e.reason ? `原因：${e.reason}` : (e.resource_id ?? undefined)}>
        {objectLabel(e)}
      </span>
    ),
  },
  { key: 'ip', header: '来源 IP', width: '120px', render: (e) => <span className={css.time}>{e.source_ip ?? '—'}</span> },
  {
    key: 'auth',
    header: '认证',
    width: '80px',
    render: (e) => {
      const a = authLabel(e.auth_context)
      return <span className={`${css.nowrap} ${a.strong ? css.warnText : css.faint}`}>{a.label}</span>
    },
  },
]

export function AuditTab() {
  const can = useCan()
  const [search, setSearch] = useState('')
  const [filter, setFilter] = useState<AuditFilter>(EMPTY_AUDIT_FILTER)
  const [offset, setOffset] = useState(0)
  const [exporting, setExporting] = useState(false)
  const audit = useAudit(filter, offset)

  // 搜索框防抖：停手 300ms 再查，免得每个字一次请求
  useEffect(() => {
    const timer = setTimeout(() => {
      setFilter((f) => (f.q === search ? f : { ...f, q: search }))
      setOffset(0)
    }, 300)
    return () => clearTimeout(timer)
  }, [search])

  const patch = (next: Partial<AuditFilter>) => {
    setFilter((f) => ({ ...f, ...next }))
    setOffset(0)
  }
  const clear = () => {
    setSearch('')
    setFilter(EMPTY_AUDIT_FILTER)
    setOffset(0)
  }
  const filtered = hasAuditFilter(filter)

  return (
    <div className={css.stack}>
      <div className={css.toolbar}>
        <Input size="sm" aria-label="搜索审计日志" fieldClassName={css.search} placeholder="操作人、动作或对象" value={search} onChange={(e) => setSearch(e.target.value)} />
        <Input size="sm" mono aria-label="动作前缀" fieldClassName={css.prefix} placeholder="动作前缀，如 order." value={filter.action} onChange={(e) => patch({ action: e.target.value })} />
        <Select size="sm" aria-label="操作者类型" emptyOption="全部操作者" value={filter.actor} options={ACTOR_KINDS.map((k) => ({ value: k, label: ACTOR_LABEL[k] }))} onChange={(e) => patch({ actor: e.target.value as ActorKind | '' })} />
        <Select size="sm" aria-label="结果" emptyOption="全部结果" value={filter.outcome} options={OUTCOMES.map((o) => ({ value: o, label: OUTCOME_LABEL[o] }))} onChange={(e) => patch({ outcome: e.target.value as Outcome | '' })} />
        {filtered && (
          <Button size="sm" variant="ghost" onClick={clear}>
            清空筛选
          </Button>
        )}
        {can('ops.export') && (
          <Button size="sm" className={css.push} onClick={() => setExporting(true)}>
            导出
          </Button>
        )}
      </div>

      <div className={`${css.panel} ${css.fixedTable}`}>
        <QueryView
          query={audit}
          rows={8}
          isEmpty={(d) => d.events.length === 0}
          empty={
            filtered ? (
              <Empty bare title="没有符合条件的审计记录" description="换个关键词，或清空筛选再看。" action={<Button size="sm" onClick={clear}>清空筛选</Button>} />
            ) : (
              <Empty bare title="还没有审计记录" description="后台与门户的写操作、登录和系统任务都会记在这里。" />
            )
          }
        >
          {(page) => (
            <>
              <Table label="审计日志" columns={columns} rows={page.events} rowKey={(e) => e.id} />
              <Pager className={css.pager} total={page.total} limit={AUDIT_PAGE} offset={offset} onChange={setOffset} />
            </>
          )}
        </QueryView>
      </div>

      <ExportModal open={exporting} filter={filter} total={audit.data?.total ?? null} onClose={() => setExporting(false)} />
    </div>
  )
}

function ExportModal({ open, filter, total, onClose }: { open: boolean; filter: AuditFilter; total: number | null; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateSecurity()
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)

  const close = () => {
    if (busy) return
    setErrors({})
    onClose()
  }
  const run = async () => {
    const errs = validateExportRange(from, to)
    setErrors(errs)
    if (Object.keys(errs).length > 0) return
    setBusy(true)
    try {
      const res = await api.requestRaw('v1/audit/export', { query: exportQuery(filter, from, to) })
      saveFile(await res.blob(), filenameFromDisposition(res.headers.get('Content-Disposition'), 'audit.csv'))
      toast('审计日志已导出')
      // 导出本身记了一条 audit.export
      void invalidate('audit')
      setBusy(false)
      setErrors({})
      onClose()
    } catch (e) {
      setBusy(false)
      fail(e, setErrors)
    }
  }

  const conditions = [filter.q.trim() && `搜索「${filter.q.trim()}」`, filter.action.trim() && `动作以 ${filter.action.trim()} 开头`, filter.actor && `操作者为${ACTOR_LABEL[filter.actor]}`, filter.outcome && `结果为${OUTCOME_LABEL[filter.outcome]}`].filter(Boolean)
  return (
    <Modal
      open={open}
      onClose={close}
      dismissible={!busy}
      title="导出审计日志"
      actions={
        <>
          <Button size="dialog" disabled={busy} onClick={close}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={busy} onClick={() => void run()}>
            导出 CSV
          </Button>
        </>
      }
    >
      <div className={css.form}>
        <p className={css.lead}>
          {conditions.length ? `按当前筛选（${conditions.join('，')}）导出` : '导出全部审计记录'}
          {total !== null && `，列表里共 ${formatCount(total)} 条`}。起止日期可以不填，结束日期含当天。
        </p>
        <div className={css.pair}>
          <Input label="开始日期" type="date" value={from} error={errors.from} disabled={busy} onChange={(e) => setFrom(e.target.value)} />
          <Input label="结束日期" type="date" value={to} error={errors.to} disabled={busy} onChange={(e) => setTo(e.target.value)} />
        </div>
        <p className={css.note}>文件含明文来源 IP，需要二次认证；一次最多 5 万行，超过请缩小时间范围。导出本身也会记一条审计。</p>
      </div>
    </Modal>
  )
}
