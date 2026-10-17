import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { filenameFromDisposition, saveFile } from '../../../core/download'
import { formatCount, formatDateTime } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, Card, ConfirmModal, Empty, Input, QueryView, Select, Table, Tag, TextArea, useToast, type TableColumn } from '../../../ui'
import { useFailure, useIntentKey } from '../../actions'
import {
  bulkMailSchema,
  generationJobSchema,
  UK,
  useBulkPreview,
  useGenerationJob,
  useGenerationJobs,
  useInvalidateUsers,
  usePlanOptions,
  useUserGroups,
  type BulkFilter,
  type GenerationJob,
} from './api'
import { BulkBanForm } from './BulkBanForm'
import { JOB_STATUS_VIEW, jobFilename, jobFinished, jobPercent, jobResultView } from './jobs'
import {
  assignableGroups,
  BULK_EXPIRY,
  BULK_STATUS,
  bulkFilter,
  EXPORT_MAX,
  expiryView,
  exportQuery,
  generateProblems,
  groupOptionLabel,
  MAIL_MAX,
  type BulkExpiry,
  type BulkForm,
  type BulkStatus,
  type GenerateForm,
} from './model'
import ops from './Ops.module.css'

type BulkOp = 'csv' | 'mail' | 'ban' | null

export function BulkTab({ now }: { now: Date }) {
  return (
    <div className={ops.bulkRoot}>
      <FilterSection now={now} />
      <GenerateSection />
    </div>
  )
}

// ===========================================================================
// 批量操作用户：筛选 → 确认名单 → 选择操作
// ===========================================================================
function FilterSection({ now }: { now: Date }) {
  const [form, setForm] = useState<BulkForm>({ plan: '', status: '', expiry: '', group: '' })
  const filter = bulkFilter(form)
  const preview = useBulkPreview(filter)
  const plans = usePlanOptions()
  const groups = useUserGroups()
  const [op, setOp] = useState<BulkOp>(null)
  const total = preview.data?.total ?? null
  const set = (next: Partial<BulkForm>) => setForm((f) => ({ ...f, ...next }))
  const opsDisabled = total === null || total === 0

  const toggleOp = (next: Exclude<BulkOp, null>) => {
    setOp((cur) => (cur === next ? null : next))
  }

  const step2Title = () => {
    if (total === null) return <>命中 <span className={ops.num}>—</span> 位用户</>
    if (total === 0) return <>命中 <span className={ops.num}>0</span> 位用户</>
    const m = preview.data?.sample_rows.length ?? 0
    return (
      <>
        命中 <span className={ops.num}>{formatCount(total)}</span> 位用户
        <span className={ops.stepTitleNote}> · 预览前 {formatCount(m)} 位</span>
      </>
    )
  }

  const step3Title = () => {
    if (total === null) return <>对这 <span className={ops.num}>—</span> 位用户</>
    return <>对这 <span className={ops.num}>{formatCount(total)}</span> 位用户</>
  }

  return (
    <Card title="批量操作用户" subtitle="先筛选，确认名单，再执行">
      <div className={ops.steps}>
        <div className={ops.stp}>
          <span className={ops.stepBadge} aria-hidden="true">1</span>
          <div className={ops.stepBody}>
            <b className={ops.stepTitle}>筛选用户</b>
            <div className={ops.fieldGrid}>
              <Select
                label="套餐"
                size="sm"
                emptyOption="不限"
                options={(plans.data ?? []).map((p) => ({ value: p.id, label: p.status === 'archived' ? `${p.name}（已归档）` : p.name }))}
                value={form.plan}
                onChange={(e) => set({ plan: e.target.value })}
              />
              <Select label="状态" size="sm" emptyOption="不限" options={BULK_STATUS.map(([value, label]) => ({ value, label }))} value={form.status} onChange={(e) => set({ status: e.target.value as BulkStatus })} />
              <Select label="到期时间" size="sm" emptyOption="不限" options={BULK_EXPIRY.map(([value, label]) => ({ value, label }))} value={form.expiry} onChange={(e) => set({ expiry: e.target.value as BulkExpiry })} />
              <Select
                label="用户组"
                size="sm"
                emptyOption="不限"
                options={(groups.data ?? []).map((g) => ({ value: g.id, label: groupOptionLabel(g) }))}
                value={form.group}
                onChange={(e) => set({ group: e.target.value })}
              />
            </div>
          </div>
        </div>

        <div className={ops.stp}>
          <span className={ops.stepBadge} aria-hidden="true">2</span>
          <div className={ops.stepBody}>
            <b className={ops.stepTitle}>{step2Title()}</b>
            <div className={ops.previewTableWrap}>
              <QueryView
                query={preview}
                rows={5}
                isEmpty={(d) => d.total === 0}
                empty={<Empty bare title="没有命中任何用户" description="换个条件再看看。" />}
              >
                {(d) => (
                  <>
                    <Table
                      boxed
                      label="命中用户预览"
                      columns={previewColumns(now)}
                      rows={d.sample_rows}
                      rowKey={(u) => u.email}
                    />
                    {d.total > d.sample_rows.length && (
                      <p className={ops.hitPreviewFoot}>
                        仅展示最近注册的 {formatCount(d.sample_rows.length)} 位，操作会作用于全部 {formatCount(d.total)} 位
                      </p>
                    )}
                  </>
                )}
              </QueryView>
            </div>
          </div>
        </div>

        <div className={ops.stp}>
          <span className={ops.stepBadge} aria-hidden="true">3</span>
          <div className={ops.stepBody}>
            <b className={ops.stepTitle}>{step3Title()}</b>
            <div className={ops.opGrid}>
              <button
                type="button"
                className={`${ops.opCard} ${op === 'csv' ? ops.opCardActive : ''}`}
                disabled={opsDisabled}
                aria-pressed={op === 'csv'}
                onClick={() => toggleOp('csv')}
              >
                <span className={ops.opCardTitle}>导出 CSV</span>
                <span className={ops.opCardDesc}>邮箱、状态、分组、订单数与累计实付</span>
              </button>
              <button
                type="button"
                className={`${ops.opCard} ${op === 'mail' ? ops.opCardActive : ''}`}
                disabled={opsDisabled}
                aria-pressed={op === 'mail'}
                onClick={() => toggleOp('mail')}
              >
                <span className={ops.opCardTitle}>群发邮件</span>
                <span className={ops.opCardDesc}>给他们发一封通知</span>
              </button>
              <button
                type="button"
                className={`${ops.opCard} ${ops.opCardDanger} ${op === 'ban' ? ops.opCardDangerActive : ''}`}
                disabled={opsDisabled}
                aria-pressed={op === 'ban'}
                onClick={() => toggleOp('ban')}
              >
                <span className={ops.opCardTitle}>批量封禁</span>
                <span className={ops.opCardDesc}>登出并停止下发订阅</span>
              </button>
            </div>
            {op === 'csv' && <ExportOpPanel filter={filter} total={total} />}
            {op === 'mail' && <MailForm filter={filter} total={total} onSent={() => setOp(null)} />}
            {op === 'ban' && (
              <div className={ops.opForm}>
                <BulkBanForm filter={filter} total={total} onDone={() => setOp(null)} />
              </div>
            )}
          </div>
        </div>
      </div>
    </Card>
  )
}

function previewColumns(now: Date): TableColumn<{ email: string; plan_name: string | null; current_period_end: string | null }>[] {
  return [
    { key: 'email', header: '邮箱', render: (u) => u.email },
    { key: 'plan', header: '套餐', render: (u) => <span className={ops.dim}>{u.plan_name ?? '无订阅'}</span> },
    {
      key: 'exp',
      header: '到期',
      align: 'right',
      render: (u) => (
        <span className={`${ops.dim} ${ops.num}`}>{u.current_period_end ? expiryView(u.current_period_end, now).text : '—'}</span>
      ),
    },
  ]
}

/** 导出：GET v1/users/bulk/export（reauth，R9）。没有 cookie，只能 fetch 带 Bearer 取回再存文件 */
function ExportOpPanel({ filter, total, disabled }: { filter: BulkFilter; total: number | null; disabled?: boolean }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const [open, setOpen] = useState(false)
  const n = total ?? 0
  const run = async () => {
    try {
      const res = await api.requestRaw('v1/users/bulk/export', { query: exportQuery(filter) })
      saveFile(await res.blob(), filenameFromDisposition(res.headers.get('Content-Disposition'), 'users.csv'))
      toast(`已导出 ${formatCount(Math.min(n, EXPORT_MAX))} 位用户`)
    } catch (e) {
      fail(e)
    }
    setOpen(false)
  }
  return (
    <div className={ops.opForm}>
      <Button size="sm" disabled={disabled || !n} onClick={() => setOpen(true)}>
        导出 {formatCount(Math.min(n, EXPORT_MAX))} 行
      </Button>
      <ConfirmModal
        open={open}
        title="导出用户名单？"
        body={
          <>
            {n > EXPORT_MAX ? `命中 ${formatCount(n)} 人，只导出最近注册的 ${formatCount(EXPORT_MAX)} 人（上限）。` : `将导出 ${formatCount(n)} 人（上限 ${formatCount(EXPORT_MAX)}）。`}
            文件含邮箱、状态、分组、订单数与累计实付，不含 IP、设备与订阅地址；导出会记入审计，请妥善保管。
          </>
        }
        confirmLabel="导出"
        onConfirm={run}
        onCancel={() => setOpen(false)}
      />
    </div>
  )
}

/** 群发：POST v1/users/bulk/mail（reauth + 幂等 user_bulk_mail），筛选字段放在请求体顶层 */
function MailForm({ filter, total, onSent }: { filter: BulkFilter; total: number | null; onSent: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const [subject, setSubject] = useState('')
  const [body, setBody] = useState('')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [confirming, setConfirming] = useState(false)
  const n = total ?? 0
  const tooMany = n > MAIL_MAX

  const check = () => {
    const local: Record<string, string> = {}
    const s = [...subject.trim()].length
    const b = [...body.trim()].length
    if (s < 1 || s > 200) local.subject = '主题必填，不超过 200 字'
    if (b < 1 || b > 20000) local.body = '正文必填，不超过 20000 字'
    if (Object.keys(local).length) return setErrors(local)
    setConfirming(true)
  }
  const send = async () => {
    const payload = { ...filter, subject: subject.trim(), body: body.trim() }
    try {
      const r = await api.post('v1/users/bulk/mail', bulkMailSchema, { body: payload, idempotencyKey: intent.keyFor(payload) })
      intent.reset()
      toast(`已排队 ${formatCount(r.queued)} 封，跳过 ${formatCount(r.skipped)} 封（用户退订）`)
      setSubject('')
      setBody('')
      onSent()
    } catch (e) {
      fail(e, { fields: setErrors, intent })
    }
    setConfirming(false)
  }

  return (
    <div className={`${ops.opForm} ${ops.mailForm}`}>
      <Input
        label="主题"
        value={subject}
        error={errors.subject}
        onChange={(e) => {
          setSubject(e.target.value)
          setErrors({})
        }}
        data-autofocus=""
      />
      <TextArea
        label="正文"
        rows={5}
        placeholder="正文，可插入变量 $email、$plan、$expire"
        value={body}
        error={errors.body}
        onChange={(e) => {
          setBody(e.target.value)
          setErrors({})
        }}
      />
      <div className={ops.mailVars}>
        <span>可用变量</span>
        <code className={ops.mailVar}>$email</code>
        <code className={ops.mailVar}>$plan</code>
        <code className={ops.mailVar}>$expire</code>
      </div>
      <p className={ops.mailVarHint}>$plan 是当前套餐名，$expire 是到期日（YYYY-MM-DD），没有订阅时为空</p>
      <div className={ops.mailActions}>
        {tooMany && <span className={ops.toneWarn}>一次最多群发 {formatCount(MAIL_MAX)} 人，请缩小范围分批发送</span>}
        <Button size="sm" variant="primary" disabled={!n || tooMany} onClick={check}>
          发送给 {formatCount(n)} 位用户
        </Button>
      </div>
      <ConfirmModal
        open={confirming}
        title={`群发给 ${formatCount(n)} 位用户？`}
        body="邮件进入投递队列，发出后无法撤回；关闭了营销邮件的用户会被跳过。投递积压可在仪表盘查看。"
        confirmLabel="发送"
        onConfirm={send}
        onCancel={() => setConfirming(false)}
      />
    </div>
  )
}

// ===========================================================================
// 批量生成：POST v1/users/bulk/generate（reauth + 幂等 user_bulk_generate）
// ===========================================================================
const EMPTY_FORM: GenerateForm = { count: '10', prefix: '', domain: '', group: '', reason: '' }
const FIELD_KEYS: Record<string, keyof GenerateForm> = { count: 'count', email_prefix: 'prefix', email_domain: 'domain', group_id: 'group', reason: 'reason' }

function GenerateSection() {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateUsers()
  const client = useQueryClient()
  const groups = useUserGroups()
  const [form, setForm] = useState<GenerateForm>(EMPTY_FORM)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const [jobId, setJobId] = useState<string | null>(null)
  const job = useGenerationJob(jobId)
  const jobs = useGenerationJobs()
  const finished = job.data ? jobFinished(job.data) : false
  const recentJobs = (jobs.data ?? []).slice(0, 5)

  useEffect(() => {
    if (!finished) return
    void invalidate(true)
    void client.invalidateQueries({ queryKey: [...UK, 'generation-jobs'] })
  }, [finished, invalidate, client])

  const set = (key: keyof GenerateForm, value: string) => {
    setForm((f) => ({ ...f, [key]: value }))
    setErrors((e) => ({ ...e, [key]: '' }))
  }
  const submit = async () => {
    const local = generateProblems(form)
    if (Object.keys(local).length) return setErrors(local)
    const body = {
      count: Number(form.count.trim()),
      email_prefix: form.prefix.trim().toLowerCase(),
      email_domain: form.domain.trim().toLowerCase(),
      reason: form.reason.trim(),
      ...(form.group ? { group_id: form.group } : {}),
    }
    setBusy(true)
    try {
      const data = await api.post('v1/users/bulk/generate', generationJobSchema, { body, idempotencyKey: intent.keyFor(body) })
      intent.reset()
      setJobId(data.id)
      setForm((f) => ({ ...f, reason: '' }))
      toast(`已提交，正在后台生成 ${formatCount(data.total)} 个账号`)
      void client.invalidateQueries({ queryKey: [...UK, 'generation-jobs'] })
    } catch (e) {
      fail(e, { fields: (f) => setErrors(Object.fromEntries(Object.entries(f).map(([k, v]) => [FIELD_KEYS[k] ?? k, v]))), intent })
    } finally {
      setBusy(false)
    }
  }
  const download = async (j: GenerationJob) => {
    try {
      const res = await api.requestRaw(`v1/users/bulk/generate/jobs/${encodeURIComponent(j.id)}/result`)
      saveFile(await res.blob(), filenameFromDisposition(res.headers.get('Content-Disposition'), jobFilename(j)))
      toast('已下载。文件里有初始密码，交付后请删除本地副本')
    } catch (e) {
      fail(e)
    }
  }

  const emailHint = `${form.prefix.trim().toLowerCase() || '前缀'}-8 位随机@${form.domain.trim().toLowerCase() || '后缀'}`

  return (
    <Card title="批量生成用户" subtitle="账号不带套餐，需要时再人工开单">
      <div className={ops.steps}>
        <div className={ops.stp}>
          <span className={ops.stepBadge} aria-hidden="true">1</span>
          <div className={ops.stepBody}>
            <b className={ops.stepTitle}>生成多少、什么邮箱</b>
            <div className={ops.fieldGrid}>
              <Input label="数量" mono inputMode="numeric" value={form.count} error={errors.count} onChange={(e) => set('count', e.target.value)} />
              <Input label="邮箱前缀" mono placeholder="dealer" value={form.prefix} error={errors.prefix} onChange={(e) => set('prefix', e.target.value)} />
              <Input label="邮箱后缀" mono placeholder="example.com" value={form.domain} error={errors.domain} onChange={(e) => set('domain', e.target.value)} />
            </div>
            <span className={ops.blockHint}>
              生成的邮箱形如 <span className={ops.num}>{emailHint}</span>
            </span>
          </div>
        </div>

        <div className={ops.stp}>
          <span className={ops.stepBadge} aria-hidden="true">2</span>
          <div className={ops.stepBody}>
            <b className={ops.stepTitle}>归到哪里</b>
            <div className={`${ops.fieldGrid} ${ops.fieldGridWide}`}>
              <Select
                label="用户组"
                emptyOption="未分组（默认）"
                options={assignableGroups(groups.data ?? [], form.group ? [form.group] : []).map((g) => ({ value: g.id, label: groupOptionLabel(g) }))}
                value={form.group}
                error={errors.group}
                onChange={(e) => set('group', e.target.value)}
              />
              <div className={ops.reasonSpan}>
                <Input label="生成原因" value={form.reason} error={errors.reason} placeholder="必填，写入审计" onChange={(e) => set('reason', e.target.value)} />
              </div>
            </div>
            <span className={ops.blockHint}>写进审计日志，5 到 500 个字</span>
          </div>
        </div>

        <div className={ops.stp}>
          <span className={ops.stepBadge} aria-hidden="true">3</span>
          <div className={ops.stepBody}>
            <form
              onSubmit={(e) => {
                e.preventDefault()
                void submit()
              }}
            >
              <div className={ops.submitRow}>
                <Button type="submit" variant="primary" busy={busy}>
                  开始生成
                </Button>
                <span className={ops.submitHint}>在后台生成，不影响用户登录</span>
              </div>
            </form>
            {job.data && <JobProgress job={job.data} onDownload={(j) => void download(j)} onClose={() => setJobId(null)} />}
          </div>
        </div>
      </div>

      {recentJobs.length > 0 && (
        <>
          <div className={ops.recordsHead}>
            <h4 className={ops.recordsTitle}>生成记录</h4>
            <span className={ops.recordsSub}>最近 5 次</span>
          </div>
          <GenerationRecordsTable jobs={recentJobs} groups={groups.data ?? []} onDownload={(j) => void download(j)} />
        </>
      )}
    </Card>
  )
}

function JobProgress({ job, onDownload, onClose }: { job: GenerationJob; onDownload: (j: GenerationJob) => void; onClose: () => void }) {
  const view = JOB_STATUS_VIEW[job.status]
  const pct = jobPercent(job)
  return (
    <div className={ops.prog}>
      <div className={ops.progHead}>
        <span>{view.label} · {job.reason}</span>
        <span className={ops.num}>{formatCount(job.completed)} / {formatCount(job.total)}</span>
      </div>
      <div className={ops.progressTrack} role="progressbar" aria-label="生成进度" aria-valuemin={0} aria-valuemax={100} aria-valuenow={pct}>
        <div className={ops.progressBar} style={{ width: `${pct}%` }} />
      </div>
      {job.error && <p className={ops.genWarn}>{job.error}</p>}
      {job.result_available && (
        <p className={ops.genWarn}>
          结果里有初始密码，只有提交人能下载{job.result_expires_at ? `，${formatDateTime(job.result_expires_at)} 之后自动清除` : '，任务结束 24 小时后自动清除'}。
        </p>
      )}
      <div className={ops.genActions}>
        {job.result_available && (
          <Button size="xs" variant="link" onClick={() => onDownload(job)}>
            下载结果
          </Button>
        )}
        {jobFinished(job) && (
          <Button size="xs" variant="ghost" onClick={onClose}>
            关闭
          </Button>
        )}
      </div>
    </div>
  )
}

type UserGroupRow = { id: string; name: string }

function groupNameFor(groups: readonly UserGroupRow[], groupId: string | null): string {
  if (!groupId) return '—'
  return groups.find((g) => g.id === groupId)?.name ?? '—'
}

function GenerationRecordsTable({
  jobs,
  groups,
  onDownload,
}: {
  jobs: GenerationJob[]
  groups: readonly UserGroupRow[]
  onDownload: (j: GenerationJob) => void
}) {
  const columns: TableColumn<GenerationJob>[] = [
    {
      key: 'time',
      hideOnMobile: true,
      header: '时间',
      width: '140px',
      render: (j) => <span className={`${ops.dim} ${ops.num}`}>{formatDateTime(j.created_at)}</span>,
    },
    {
      key: 'reason',
      header: '原因',
      render: (j) => (
        <span className={ops.reasonCell}>
          {j.reason}
          <span className={ops.sub2}>{j.email_prefix}@{j.email_domain}</span>
          <span className={`${ops.sub2} ${ops.mOnly}`}>{formatDateTime(j.created_at)}</span>
        </span>
      ),
    },
    { key: 'total', header: '数量', align: 'right', mono: true, render: (j) => formatCount(j.total) },
    { key: 'group', hideOnMobile: true, header: '用户组', render: (j) => <span className={ops.dim}>{groupNameFor(groups, j.group_id)}</span> },
    {
      key: 'result',
      header: '结果',
      align: 'right',
      render: (j) => {
        const v = jobResultView(j)
        return <Tag tone={v.tone}>{v.label}</Tag>
      },
    },
    {
      key: 'dl',
      header: '下载',
      align: 'right',
      width: '72px',
      render: (j) =>
        j.result_available ? (
          <Button size="xs" variant="link" onClick={() => onDownload(j)}>
            下载
          </Button>
        ) : null,
    },
  ]
  return <Table label="生成记录" columns={columns} rows={jobs} rowKey={(j) => j.id} />
}
