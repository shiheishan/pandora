/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/api 的 isApiError，依赖 ../../../core/format 的 formatBytes / formatCount / formatDateTime，依赖 ../../../core/router 的 navigate / useHashLocation，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / Empty / Input / Pager / QueryView / Segmented / StatStrip / Table / Tag / TextArea / useToast，依赖 ../../actions 的 useCan / useFailure / useIntentKey，依赖 ./api，依赖 ./dialogs 的 ActionModal，依赖 ./model，依赖 ./Users.module.css 与 ./Ops.module.css
 * [OUTPUT]: 对外提供 ResetsTab、ResetHistory、ResetDialog
 * [POS]: 流量重置（契约后台-03，metering.reset.*）：ResetsTab 是「流量重置」标签（#/users/resets?r=<原因>&o=<偏移>）——近 30 天四格统计、按邮箱手动重置、按原因筛选的分页日志；ResetHistory 是用户抽屉的「流量重置」标签（最近 50 条 + 立即重置本期）；两处都经 ResetDialog 确认，必填重置原因 5–500 字，POST v1/users/{id}/traffic-reset 要 reauth + 幂等 traffic_manual_reset。只清当前订阅的本期已用，挑哪条与后端同口径（status=active、到期最晚）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { formatBytes, formatCount, formatDateTime } from '../../../core/format'
import { navigate, useHashLocation } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Empty, Input, Pager, QueryView, Segmented, StatStrip, Table, Tag, TextArea, useToast, type TableColumn } from '../../../ui'
import { useCan, useFailure, useIntentKey } from '../../actions'
import {
  RESET_REASONS,
  RESETS_PAGE,
  resetDoneSchema,
  useFindUserByEmail,
  useInvalidateResets,
  useResetStats,
  useTrafficResets,
  useUser,
  useUserResets,
  type ResetLog,
  type ResetReason,
  type UserDetail,
} from './api'
import { ActionModal } from './dialogs'
import { NOTE_MAX, noteProblem, RESET_REASON_VIEW, resetActor, resettableSub, trafficQuota } from './model'
import ops from './Ops.module.css'
import css from './Users.module.css'

type ReasonFilter = ResetReason | 'all'
const REASON_FILTERS: ReadonlyArray<{ value: ReasonFilter; label: string }> = [{ value: 'all', label: '全部' }, ...RESET_REASONS.map((r) => ({ value: r, label: RESET_REASON_VIEW[r].filter }))]

function isReason(v: string | null): v is ResetReason {
  return RESET_REASONS.some((r) => r === v)
}

// ===========================================================================
// 「流量重置」标签
// ===========================================================================
export function ResetsTab() {
  const location = useHashLocation()
  const can = useCan()
  const r = location.query.get('r')
  const reason: ReasonFilter = isReason(r) ? r : 'all'
  const offset = Math.max(0, Number.parseInt(location.query.get('o') ?? '', 10) || 0)
  const logs = useTrafficResets(reason === 'all' ? '' : reason, offset)
  const stats = useResetStats()
  const go = (next: { reason?: ReasonFilter; offset?: number }) => {
    const nr = next.reason ?? reason
    const no = next.offset ?? offset
    navigate('/users/resets', { replace: true, query: { r: nr === 'all' ? undefined : nr, o: no || undefined } })
  }
  const s = stats.data

  return (
    <div className={css.list}>
      <StatStrip
        label="近 30 天流量重置"
        items={
          s && [
            { label: '近 30 天重置', value: formatCount(s.last_30_days) },
            { label: '自动 · 账单周期', value: formatCount((s.by_reason.renewal ?? 0) + (s.by_reason.cycle_roll ?? 0)) },
            { label: '手动', value: formatCount(s.manual_count) },
            { label: '重置前累计用量', value: formatBytes(s.freed_bytes) },
          ]
        }
      />
      <div className={css.toolbar}>
        {can('metering.reset.write') && <ManualReset />}
        <div className={css.spacer} />
        <Segmented size="sm" label="重置方式" options={REASON_FILTERS} value={reason} onChange={(v) => go({ reason: v, offset: 0 })} />
      </div>
      <div className={ops.plainTable}>
        <QueryView
          query={logs}
          rows={6}
          isEmpty={(d) => d.logs.length === 0}
          empty={<Empty bare title={reason === 'all' ? '还没有流量重置记录' : '这种方式还没有重置记录'} description="续费、周期滚动、礼品卡、变更套餐与手动重置都会记在这里。" />}
        >
          {(d) => <Table label="流量重置日志" columns={LOG_COLUMNS} rows={d.logs} rowKey={(l) => l.id} />}
        </QueryView>
      </div>
      <Pager total={logs.data?.total ?? 0} limit={RESETS_PAGE} offset={offset} onChange={(o) => go({ offset: o })} />
    </div>
  )
}

const LOG_COLUMNS: TableColumn<ResetLog>[] = [
  {
    key: 'user',
    header: '用户',
    render: (l) => (
      <span className={css.stack}>
        <span className={ops.ellipsis}>{l.user_email || '—'}</span>
        {l.plan_name && <span className={css.small}>{l.plan_name}</span>}
      </span>
    ),
  },
  { key: 'kind', header: '方式', width: '84px', render: (l) => <ReasonTag reason={l.reason} /> },
  { key: 'used', header: '重置前用量', width: '110px', align: 'right', mono: true, render: (l) => formatBytes(l.consumed_before) },
  { key: 'by', header: '操作人', render: (l) => <span className={`${css.muted} ${ops.ellipsis}`}>{resetActor(l)}</span> },
  { key: 'at', header: '时间', width: '150px', align: 'right', mono: true, render: (l) => <span className={css.muted}>{formatDateTime(l.created_at)}</span> },
]

function ReasonTag({ reason }: { reason: ResetReason }) {
  const v = RESET_REASON_VIEW[reason]
  return <Tag tone={v.tone}>{v.label}</Tag>
}

/** 按邮箱手动重置：先 GET v1/users?q= 找到邮箱完全相等的那条，找不到提示「用户不存在」 */
function ManualReset() {
  const find = useFindUserByEmail()
  const fail = useFailure()
  const [email, setEmail] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [target, setTarget] = useState<{ id: string; email: string } | null>(null)

  const lookup = async () => {
    if (!email.trim()) return setError('请输入用户邮箱')
    setBusy(true)
    try {
      const user = await find(email)
      if (user) setTarget({ id: user.id, email: user.email })
      else setError('用户不存在')
    } catch (e) {
      fail(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <form
        className={ops.manualReset}
        onSubmit={(e) => {
          e.preventDefault()
          void lookup()
        }}
      >
        <Input
          size="sm"
          type="email"
          aria-label="用户邮箱"
          placeholder="输入用户邮箱手动重置本期流量"
          value={email}
          error={error ?? undefined}
          fieldClassName={ops.manualInput}
          onChange={(e) => {
            setEmail(e.target.value)
            setError(null)
          }}
        />
        <Button size="sm" type="submit" variant="primary" busy={busy}>
          手动重置
        </Button>
      </form>
      {/* 对话框放在表单外：<dialog> 不走 portal，放里面会继承表单的回车提交 */}
      <ResetDialog
        user={target}
        onClose={() => setTarget(null)}
        onDone={() => {
          setEmail('')
          setTarget(null)
        }}
      />
    </>
  )
}

// ===========================================================================
// 抽屉「流量重置」标签：最近 50 条 + 立即重置本期
// ===========================================================================
export function ResetHistory({ d }: { d: UserDetail }) {
  const can = useCan()
  const history = useUserResets(d.id)
  const [open, setOpen] = useState(false)
  const sub = resettableSub(d.subscriptions)
  return (
    <>
      <div className={ops.historyHead}>
        <span className={css.muted}>此用户的流量重置历史</span>
        {can('metering.reset.write') && (
          <Button size="xs" disabled={!sub} title={sub ? undefined : '没有生效中的订阅（试用订阅不能手动重置）'} onClick={() => setOpen(true)}>
            立即重置本期
          </Button>
        )}
      </div>
      <QueryView query={history} rows={3} isEmpty={(h) => h.logs.length === 0} empty={<Empty bare title="还没有重置记录" description="续费或周期滚动时会自动重置，手动重置也记在这里。" />}>
        {(h) => (
          <ul className={css.orders}>
            {h.logs.map((l) => (
              <li key={l.id} className={ops.historyRow}>
                <ReasonTag reason={l.reason} />
                <span className={css.stack}>
                  <span className={css.mono}>重置前 {formatBytes(l.consumed_before)}</span>
                  <span className={`${css.small} ${ops.ellipsis}`}>{resetActor(l)}</span>
                </span>
                <span className={`${css.small} ${css.mono}`}>{formatDateTime(l.created_at)}</span>
              </li>
            ))}
          </ul>
        )}
      </QueryView>
      <ResetDialog user={open ? { id: d.id, email: d.email } : null} onClose={() => setOpen(false)} onDone={() => setOpen(false)} />
    </>
  )
}

// ===========================================================================
// 确认框：POST v1/users/{id}/traffic-reset（metering.reset.write + reauth + 幂等 traffic_manual_reset）
// ===========================================================================
export function ResetDialog({ user, onClose, onDone }: { user: { id: string; email: string } | null; onClose: () => void; onDone: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateResets()
  // 详情在抽屉里已有缓存；按邮箱重置时这里现取，用来说清「清掉多少」
  const detail = useUser(user?.id ?? null)
  const sub = detail.data ? resettableSub(detail.data.subscriptions) : undefined
  const used = sub ? trafficQuota(sub.quotas)?.consumed : undefined
  const noSub = detail.data !== undefined && sub === undefined
  const [note, setNote] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const close = () => {
    setNote('')
    setError(null)
    onClose()
  }
  const submit = async () => {
    if (!user) return
    const problem = noteProblem(note)
    if (problem) return setError(problem)
    const body = { note: note.trim() }
    setBusy(true)
    try {
      const r = await api.post(`v1/users/${encodeURIComponent(user.id)}/traffic-reset`, resetDoneSchema, { body, idempotencyKey: intent.keyFor([user.id, body]) })
      intent.reset()
      toast(`已重置 ${user.email} 的本期流量，清零 ${formatBytes(r.freed_bytes)}`)
      void invalidate()
      setNote('')
      setError(null)
      onDone()
    } catch (e) {
      // 没有生效订阅 / 没有流量配额是 422 且没有 fields，原文放进框里
      if (isApiError(e, 'validation_failed') && Object.keys(e.fields).length === 0) setError(e.message)
      else fail(e, (f) => setError(f.note ?? Object.values(f)[0] ?? null))
    } finally {
      setBusy(false)
    }
  }

  return (
    <ActionModal open={user !== null} title="立即重置本期流量？" busy={busy} confirm="重置" tone="danger" disabled={noSub} onCancel={close} onConfirm={() => void submit()}>
      <p className={css.dialogText}>
        {noSub
          ? `${user?.email ?? ''} 没有生效中的订阅（试用订阅不能手动重置）。`
          : `${user?.email ?? ''}${sub ? ` 的「${sub.plan_name}」` : ''} 本期已用${used !== undefined ? ` ${formatBytes(used)}` : '流量'}将清零，并记录在重置历史中。流量包余额不受影响。`}
      </p>
      <TextArea
        label="重置原因"
        rows={2}
        value={note}
        maxLength={NOTE_MAX * 2}
        onChange={(e) => {
          setNote(e.target.value)
          setError(null)
        }}
        error={error ?? undefined}
        hint="写进审计与重置历史，5 到 500 个字"
        data-autofocus=""
      />
    </ActionModal>
  )
}
