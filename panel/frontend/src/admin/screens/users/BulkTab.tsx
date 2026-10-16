import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { filenameFromDisposition, saveFile } from '../../../core/download'
import { formatCount, formatDateTime } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Empty, Input, QueryView, Select, Tag, TextArea, useToast } from '../../../ui'
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
import { JOB_STATUS_VIEW, jobFilename, jobFinished, jobPercent } from './jobs'
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
import css from './Users.module.css'

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
  const [mailOpen, setMailOpen] = useState(false)
  const [banOpen, setBanOpen] = useState(false)
  const total = preview.data?.total ?? null
  const set = (next: Partial<BulkForm>) => setForm((f) => ({ ...f, ...next }))
  const opsDisabled = total === null || total === 0

  return (
    <section className={ops.bulkSection} aria-labelledby="bulk-users-heading">
      <div className={ops.sectionHead}>
        <h3 id="bulk-users-heading" className={ops.sectionTitle}>
          批量操作用户 <span className={ops.sectionNote}>先筛选，确认名单，再执行</span>
        </h3>
      </div>

      <div className={ops.stepHead}>
        <span className={ops.stepBadge} aria-hidden="true">1</span>
        <span className={ops.stepTitle}>设定筛选条件</span>
        <span className={ops.stepNote}>不选即为不限</span>
      </div>
      <div className={ops.stepBody}>
        <div className={ops.filterGrid}>
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

      <div className={ops.stepHead}>
        <span className={ops.stepBadge} aria-hidden="true">2</span>
        <span className={ops.stepTitle}>确认命中名单</span>
        <span className={ops.stepSpacer} />
        <span className={ops.hitCountRow}>
          <span className={ops.hitNumber}>{total === null ? '—' : formatCount(total)}</span>
          <span className={ops.hitUnit}>位用户命中</span>
        </span>
      </div>
      <div className={ops.stepBody}>
        <div className={ops.previewBox}>
          <QueryView
            query={preview}
            rows={5}
            isEmpty={(d) => d.total === 0}
            empty={<Empty bare title="没有命中任何用户" description="换个条件再看看。" />}
          >
            {(d) => (
              <>
                <div className={ops.previewHead} aria-hidden="true">
                  <span>邮箱</span>
                  <span>套餐</span>
                  <span className={ops.textRight}>到期</span>
                </div>
                <ul className={ops.previewList} aria-label="命中用户预览">
                  {d.sample_rows.map((u) => (
                    <li key={u.email} className={ops.previewRow}>
                      <span className={ops.ellipsis}>{u.email}</span>
                      <span className={ops.previewSub}>{u.plan_name ?? '无订阅'}</span>
                      <span className={ops.previewExp}>{u.current_period_end ? expiryView(u.current_period_end, now).text : '—'}</span>
                    </li>
                  ))}
                </ul>
                {d.total > d.sample_rows.length && (
                  <p className={ops.previewFoot}>
                    仅展示最近注册的 {formatCount(d.sample_rows.length)} 位，操作会作用于全部 {formatCount(d.total)} 位
                  </p>
                )}
              </>
            )}
          </QueryView>
        </div>
      </div>

      <div className={ops.stepHead}>
        <span className={ops.stepBadge} aria-hidden="true">3</span>
        <span className={ops.stepTitle}>选择操作</span>
      </div>
      <div className={ops.stepBody}>
        <div className={ops.opGrid}>
          <ExportOpCard filter={filter} total={total} disabled={opsDisabled} />
          <button
            type="button"
            className={`${ops.opCard} ${mailOpen ? ops.opCardActive : ''}`}
            disabled={opsDisabled}
            aria-expanded={mailOpen}
            onClick={() => {
              setMailOpen((v) => !v)
              setBanOpen(false)
            }}
          >
            <span className={ops.opCardTitle}>群发邮件</span>
            <span className={ops.opCardDesc}>给他们发一封通知</span>
          </button>
          <button
            type="button"
            className={`${ops.opCard} ${ops.opCardDanger} ${banOpen ? ops.opCardDangerActive : ''}`}
            disabled={opsDisabled}
            aria-expanded={banOpen}
            onClick={() => {
              setBanOpen((v) => !v)
              setMailOpen(false)
            }}
          >
            <span className={ops.opCardTitle}>批量封禁</span>
            <span className={ops.opCardDesc}>停止登录与连接</span>
          </button>
        </div>
        {mailOpen && <MailForm filter={filter} total={total} onSent={() => setMailOpen(false)} />}
        {banOpen && <BulkBanForm filter={filter} total={total} onDone={() => setBanOpen(false)} />}
      </div>
    </section>
  )
}

/** 导出：GET v1/users/bulk/export（reauth，R9）。没有 cookie，只能 fetch 带 Bearer 取回再存文件 */
function ExportOpCard({ filter, total, disabled }: { filter: BulkFilter; total: number | null; disabled: boolean }) {
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
      <button type="button" className={ops.opCard} disabled={disabled} onClick={() => setOpen(true)}>
        <span className={ops.opCardTitle}>导出 CSV</span>
        <span className={ops.opCardDesc}>下载这份名单</span>
      </button>
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
    <section className={ops.bulkSection} aria-labelledby="bulk-generate-heading">
      <div className={ops.sectionHead}>
        <h3 id="bulk-generate-heading" className={ops.sectionTitle}>
          批量生成用户 <span className={ops.sectionNote}>账号不带套餐，需要时再人工开单</span>
        </h3>
      </div>

      <div className={ops.stepHead}>
        <span className={ops.stepBadge} aria-hidden="true">1</span>
        <span className={ops.stepTitle}>账号格式</span>
        <span className={ops.stepNote}>生成多少个、邮箱长什么样</span>
      </div>
      <div className={ops.stepBody}>
        <div className={ops.formStack}>
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
        </div>
      </div>

      <div className={ops.stepHead}>
        <span className={ops.stepBadge} aria-hidden="true">2</span>
        <span className={ops.stepTitle}>归属与原因</span>
      </div>
      <div className={ops.stepBody}>
        <div className={ops.formStack}>
          <Select
            label="用户组"
            emptyOption="未分组（默认）"
            options={assignableGroups(groups.data ?? [], form.group ? [form.group] : []).map((g) => ({ value: g.id, label: groupOptionLabel(g) }))}
            value={form.group}
            error={errors.group}
            onChange={(e) => set('group', e.target.value)}
          />
          <TextArea label="生成原因" rows={2} value={form.reason} error={errors.reason} hint="写进审计日志，5 到 500 个字" onChange={(e) => set('reason', e.target.value)} />
        </div>
      </div>

      <div className={ops.stepHead}>
        <span className={ops.stepBadge} aria-hidden="true">3</span>
        <span className={ops.stepTitle}>提交生成</span>
      </div>
      <div className={ops.stepBody}>
        <form
          className={ops.formStack}
          onSubmit={(e) => {
            e.preventDefault()
            void submit()
          }}
        >
          <div className={ops.submitRow}>
            <span className={ops.submitHint}>在后台生成，不影响用户登录</span>
            <Button type="submit" variant="primary" busy={busy}>
              开始生成
            </Button>
          </div>
        </form>
        {job.data && (
          <div className={ops.jobBox}>
            <JobProgress job={job.data} onDownload={(j) => void download(j)} onClose={() => setJobId(null)} />
          </div>
        )}
        <RecentJobs jobs={(jobs.data ?? []).filter((j) => j.id !== jobId)} onDownload={(j) => void download(j)} />
      </div>
    </section>
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
    <div className={ops.recentJobsSection}>
      <div className={ops.recentJobsHead}>生成记录</div>
      <div className={ops.genResult}>
        <ul className={ops.genList} aria-label="生成记录">
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
    </div>
  )
}
