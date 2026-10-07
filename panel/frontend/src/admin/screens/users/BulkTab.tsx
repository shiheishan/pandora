import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { filenameFromDisposition, saveFile } from '../../../core/download'
import { formatCount, formatDateTime } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, Card, ConfirmModal, Empty, Input, QueryView, Select, Tag, TextArea, useToast } from '../../../ui'
import { useCan, useFailure, useIntentKey } from '../../actions'
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
import { JOB_STATUS_VIEW, jobFilename, jobFinished, jobPercent } from './jobs'
import {
  BULK_EXPIRY,
  BULK_STATUS,
  bulkFilter,
  EXPORT_MAX,
  expiryView,
  exportQuery,
  generateProblems,
  MAIL_MAX,
  type BulkExpiry,
  type BulkForm,
  type BulkStatus,
  type GenerateForm,
} from './model'
import ops from './Ops.module.css'
import css from './Users.module.css'

export function BulkTab({ now }: { now: Date }) {
  const can = useCan()
  const canGenerate = can('iam.user.write')
  return (
    <div className={canGenerate ? ops.splitBulk : undefined}>
      <FilterPanel now={now} />
      {canGenerate && <GeneratePanel />}
    </div>
  )
}

// ===========================================================================
// 按条件筛选：预览（iam.user.read）→ 导出 / 群发
// ===========================================================================
function FilterPanel({ now }: { now: Date }) {
  const can = useCan()
  const [form, setForm] = useState<BulkForm>({ plan: '', status: '', expiry: '', group: '' })
  const filter = bulkFilter(form)
  const preview = useBulkPreview(filter)
  // 套餐下拉读 GET v1/plans，要 catalog.read；没有就不给这个条件
  const plans = usePlanOptions(can('catalog.read'))
  const groups = useUserGroups()
  const [mailOpen, setMailOpen] = useState(false)
  const total = preview.data?.total ?? null
  const set = (next: Partial<BulkForm>) => setForm((f) => ({ ...f, ...next }))

  return (
    <Card
      flush
      title={
        <span className={ops.cardTitle}>
          按条件筛选用户 <span className={`${css.small} ${ops.titleNote}`}>预览后导出或群发邮件</span>
        </span>
      }
    >
      <div className={ops.filterGrid}>
        {can('catalog.read') && (
          <Select
            label="套餐"
            size="sm"
            emptyOption="不限"
            options={(plans.data ?? []).map((p) => ({ value: p.id, label: p.status === 'archived' ? `${p.name}（已归档）` : p.name }))}
            value={form.plan}
            onChange={(e) => set({ plan: e.target.value })}
          />
        )}
        <Select label="状态" size="sm" emptyOption="不限" options={BULK_STATUS.map(([value, label]) => ({ value, label }))} value={form.status} onChange={(e) => set({ status: e.target.value as BulkStatus })} />
        <Select label="到期时间" size="sm" emptyOption="不限" options={BULK_EXPIRY.map(([value, label]) => ({ value, label }))} value={form.expiry} onChange={(e) => set({ expiry: e.target.value as BulkExpiry })} />
        <Select
          label="用户组"
          size="sm"
          emptyOption="不限"
          options={(groups.data ?? []).map((g) => ({ value: g.id, label: g.name }))}
          value={form.group}
          onChange={(e) => set({ group: e.target.value })}
        />
      </div>
      <div className={ops.hitBar}>
        <span className={ops.hitCount}>{total === null ? '—' : formatCount(total)}</span>
        <span className={css.muted}>位用户命中</span>
        <span className={css.spacer} />
        {can('iam.user.write') && <ExportButton filter={filter} total={total} />}
        {can('ops.notification.write') && (
          <Button size="sm" variant="primary" aria-expanded={mailOpen} onClick={() => setMailOpen((v) => !v)}>
            群发邮件
          </Button>
        )}
      </div>
      {mailOpen && <MailForm filter={filter} total={total} onSent={() => setMailOpen(false)} />}
      <QueryView
        query={preview}
        rows={5}
        isEmpty={(d) => d.total === 0}
        empty={<Empty bare title="没有命中任何用户" description="换个条件再看看。" />}
      >
        {(d) => (
          <>
            <ul className={ops.previewList} aria-label="命中用户预览">
              {d.sample_rows.map((u) => (
                <li key={u.email} className={ops.previewRow}>
                  <span className={ops.ellipsis}>{u.email}</span>
                  <span className={css.muted}>{u.plan_name ?? '无订阅'}</span>
                  <span className={css.small}>{u.current_period_end ? expiryView(u.current_period_end, now).text : '—'}</span>
                </li>
              ))}
            </ul>
            {d.total > d.sample_rows.length && <p className={ops.previewFoot}>只预览最近注册的 {d.sample_rows.length} 位</p>}
          </>
        )}
      </QueryView>
    </Card>
  )
}

/** 导出：GET v1/users/bulk/export（iam.user.write + reauth，R9）。没有 cookie，只能 fetch 带 Bearer 取回再存文件 */
function ExportButton({ filter, total }: { filter: BulkFilter; total: number | null }) {
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
    <>
      <Button size="sm" disabled={!n} onClick={() => setOpen(true)}>
        导出 CSV
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
    </>
  )
}

/** 群发：POST v1/users/bulk/mail（ops.notification.write + reauth + 幂等 user_bulk_mail），筛选字段放在请求体顶层 */
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
    <div className={ops.mailForm}>
      <Input
        aria-label="邮件主题"
        placeholder="邮件主题"
        value={subject}
        error={errors.subject}
        onChange={(e) => {
          setSubject(e.target.value)
          setErrors({})
        }}
        data-autofocus=""
      />
      <TextArea
        aria-label="邮件正文"
        rows={5}
        placeholder="正文，可插入变量 $email、$plan、$expire"
        value={body}
        error={errors.body}
        hint="变量逐人替换：$plan 是当前套餐名，$expire 是到期日（YYYY-MM-DD），没有订阅时为空"
        onChange={(e) => {
          setBody(e.target.value)
          setErrors({})
        }}
      />
      <div className={ops.mailActions}>
        {tooMany && <span className={css.tone_warn}>一次最多群发 {formatCount(MAIL_MAX)} 人，请缩小范围分批发送</span>}
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
// 批量生成：POST v1/users/bulk/generate（iam.user.write + reauth + 幂等 user_bulk_generate）
// 是后台任务：提交即回，worker 逐个生成（一次只占 1 个 Argon2 名额，不挡登录）。
// 这里轮询进度画进度条；完成后从服务端下载结果 CSV（含初始口令，24 小时内、只有提交人能下，
// 要近期重认证，每次下载都进审计）
// ===========================================================================
const EMPTY_FORM: GenerateForm = { count: '10', prefix: '', domain: '', group: '', reason: '' }
// 后端字段名 → 表单字段名
const FIELD_KEYS: Record<string, keyof GenerateForm> = { count: 'count', email_prefix: 'prefix', email_domain: 'domain', group_id: 'group', reason: 'reason' }

function GeneratePanel() {
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

  // 任务一结束：用户列表与最近任务各刷新一次
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

  return (
    <Card title="批量生成用户">
      <form
        className={ops.formStack}
        onSubmit={(e) => {
          e.preventDefault()
          void submit()
        }}
      >
        <div className={ops.genRow}>
          <Input label="数量" mono inputMode="numeric" value={form.count} error={errors.count} onChange={(e) => set('count', e.target.value)} />
          <Input label="邮箱前缀" mono placeholder="dealer" value={form.prefix} error={errors.prefix} onChange={(e) => set('prefix', e.target.value)} />
        </div>
        <Input
          label="邮箱后缀"
          mono
          placeholder="example.com"
          value={form.domain}
          error={errors.domain}
          hint={`生成的邮箱形如 ${form.prefix.trim().toLowerCase() || '前缀'}-8 位随机@${form.domain.trim().toLowerCase() || '后缀'}`}
          onChange={(e) => set('domain', e.target.value)}
        />
        <Select
          label="用户组"
          emptyOption="不分组"
          options={(groups.data ?? []).map((g) => ({ value: g.id, label: g.name }))}
          value={form.group}
          error={errors.group}
          onChange={(e) => set('group', e.target.value)}
        />
        <TextArea label="生成原因" rows={2} value={form.reason} error={errors.reason} hint="写进审计日志，5 到 500 个字" onChange={(e) => set('reason', e.target.value)} />
        <p className={css.small}>生成的账号不带套餐；需要时到「订单与收款」为其人工开单。生成在后台进行，不影响用户登录。</p>
        <Button type="submit" variant="primary" busy={busy}>
          生成
        </Button>
      </form>
      {job.data && <JobProgress job={job.data} onDownload={(j) => void download(j)} onClose={() => setJobId(null)} />}
      <RecentJobs jobs={(jobs.data ?? []).filter((j) => j.id !== jobId)} onDownload={(j) => void download(j)} />
    </Card>
  )
}

function JobProgress({ job, onDownload, onClose }: { job: GenerationJob; onDownload: (j: GenerationJob) => void; onClose: () => void }) {
  const view = JOB_STATUS_VIEW[job.status]
  const pct = jobPercent(job)
  return (
    <div className={ops.genResult}>
      <div className={ops.genHead}>
        <Tag tone={view.tone}>{view.label}</Tag>
        <span>
          {formatCount(job.completed)} / {formatCount(job.total)}
          {job.failed > 0 ? `，${formatCount(job.failed)} 个未生成` : ''}
        </span>
        <span className={css.spacer} />
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
      <div className={ops.progressTrack} role="progressbar" aria-label="生成进度" aria-valuemin={0} aria-valuemax={100} aria-valuenow={pct}>
        <div className={ops.progressBar} style={{ width: `${pct}%` }} />
      </div>
      {job.error && <p className={ops.genWarn}>{job.error}</p>}
      {job.result_available && (
        <p className={ops.genWarn}>
          结果里有初始密码，只有提交人能下载{job.result_expires_at ? `，${formatDateTime(job.result_expires_at)} 之后自动清除` : '，任务结束 24 小时后自动清除'}。
        </p>
      )}
    </div>
  )
}

function RecentJobs({ jobs, onDownload }: { jobs: GenerationJob[]; onDownload: (j: GenerationJob) => void }) {
  if (jobs.length === 0) return null
  return (
    <div className={ops.genResult}>
      <div className={ops.genHead}>最近的生成任务</div>
      <ul className={ops.genList} aria-label="最近的生成任务">
        {jobs.slice(0, 5).map((j) => (
          <li key={j.id}>
            <span>
              {j.email_prefix}@{j.email_domain} · {formatCount(j.completed)}/{formatCount(j.total)} · {JOB_STATUS_VIEW[j.status].label}
            </span>
            <span className={css.muted}>
              {formatDateTime(j.created_at)}
              {j.result_available && (
                <>
                  {' '}
                  <Button size="xs" variant="link" onClick={() => onDownload(j)}>
                    下载
                  </Button>
                </>
              )}
            </span>
          </li>
        ))}
      </ul>
    </div>
  )
}
