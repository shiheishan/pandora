/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/download 的 filenameFromDisposition / saveFile / toCsv，依赖 ../../../core/format 的 formatCount，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / Card / ConfirmModal / Empty / Input / QueryView / Select / TextArea / useToast，依赖 ../../actions 的 useCan / useFailure / useIntentKey，依赖 ./api，依赖 ./model，依赖 ./Users.module.css 与 ./Ops.module.css
 * [OUTPUT]: 对外提供 BulkTab
 * [POS]: 用户页「批量运营」标签（#/users/bulk）：左「按条件筛选用户」（套餐 / 状态 / 到期 / 用户组 → 预览命中数与前 10 位，导出 CSV、群发邮件），右「批量生成用户」（数量、邮箱前缀与后缀、用户组、生成原因，结果只显示一次、本地拼 CSV 下载）。契约后台-03 与 R9：导出与生成要 iam.user.write + reauth，群发要 ops.notification.write + reauth；生成与群发带幂等键。D-B-7 未决前不出现「开通套餐」
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { filenameFromDisposition, saveFile, toCsv } from '../../../core/download'
import { formatCount } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, Card, ConfirmModal, Empty, Input, QueryView, Select, TextArea, useToast } from '../../../ui'
import { useCan, useFailure, useIntentKey } from '../../actions'
import { bulkMailSchema, generatedSchema, useBulkPreview, useInvalidateUsers, usePlanOptions, useUserGroups, type BulkFilter, type Generated } from './api'
import {
  BULK_EXPIRY,
  BULK_STATUS,
  bulkFilter,
  EXPORT_MAX,
  expiryView,
  exportQuery,
  generatedRows,
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
      fail(e, setErrors)
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
// 口令明文只回这一次：结果留在本页，下载在本地拼 CSV，不再请求服务器
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
  const groups = useUserGroups()
  const [form, setForm] = useState<GenerateForm>(EMPTY_FORM)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<{ data: Generated; prefix: string } | null>(null)

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
      const data = await api.post('v1/users/bulk/generate', generatedSchema, { body, idempotencyKey: intent.keyFor(body) })
      intent.reset()
      setResult({ data, prefix: body.email_prefix })
      setForm((f) => ({ ...f, reason: '' }))
      toast(`已生成 ${formatCount(data.count)} 个账号`)
      void invalidate(true)
    } catch (e) {
      fail(e, (f) => setErrors(Object.fromEntries(Object.entries(f).map(([k, v]) => [FIELD_KEYS[k] ?? k, v]))))
    } finally {
      setBusy(false)
    }
  }
  const download = () => {
    if (!result) return
    const day = new Date().toISOString().slice(0, 10)
    saveFile(toCsv(generatedRows(result.data.users)), `users-${result.prefix}-${day}.csv`)
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
        <p className={css.small}>生成的账号不带套餐；需要时到「订单与收款」为其人工开单。</p>
        <Button type="submit" variant="primary" busy={busy}>
          生成
        </Button>
      </form>
      {result && (
        <div className={ops.genResult}>
          <div className={ops.genHead}>
            <span>已生成 {formatCount(result.data.count)} 个账号，初始密码只显示这一次</span>
            <span className={css.spacer} />
            <Button size="xs" variant="link" onClick={download}>
              下载
            </Button>
            <Button size="xs" variant="ghost" onClick={() => setResult(null)}>
              关闭
            </Button>
          </div>
          <ul className={ops.genList} aria-label="生成的账号">
            {result.data.users.slice(0, 5).map((u) => (
              <li key={u.email}>
                <span>{u.email}</span>
                <span className={css.muted}>{u.password}</span>
              </li>
            ))}
          </ul>
          <p className={ops.genWarn}>
            {result.data.users.length > 5 ? `另外 ${formatCount(result.data.users.length - 5)} 个在下载的文件里。` : ''}
            {result.data.warning}
          </p>
        </div>
      )}
    </Card>
  )
}
