import { useState } from 'react'
import { formatCount } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, TextArea, useToast } from '../../../ui'
import { useFailure, useIntentKey } from '../../actions'
import { bulkBanSchema, useInvalidateUsers, type BulkFilter } from './api'
import { REASON_MAX, REASON_MIN } from './model'
import ops from './Ops.module.css'
import css from './Users.module.css'

const SKIPPED_LABELS: Record<string, string> = {
  self: '自己',
  admin: '管理员',
  already_banned: '已封禁',
  deleted: '已注销',
}
const SKIPPED_ORDER = ['self', 'admin', 'already_banned', 'deleted']

/** 格式化封禁跳过人数 */
export function banSkippedText(skipped: Record<string, number>): string {
  const entries = Object.entries(skipped).filter(([, count]) => count > 0)
  if (entries.length === 0) return ''

  entries.sort(([a], [b]) => {
    const ia = SKIPPED_ORDER.indexOf(a)
    const ib = SKIPPED_ORDER.indexOf(b)
    if (ia !== -1 && ib !== -1) return ia - ib
    if (ia !== -1) return -1
    if (ib !== -1) return 1
    return a.localeCompare(b)
  })

  const parts = entries.map(([key, count]) => `${SKIPPED_LABELS[key] ?? key} ${count}`)
  return `跳过：${parts.join('、')}`
}

/** 检查筛选条件是否至少有一个字段有值 */
export function hasAnyFilter(f: BulkFilter): boolean {
  return Boolean(f.status || f.group_id || f.plan_id || f.expires_within_days || f.sub_state)
}

export function BulkBanForm({ filter, total, onDone }: { filter: BulkFilter; total: number | null; onDone: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateUsers()

  const [reason, setReason] = useState('')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [confirming, setConfirming] = useState(false)

  const n = total ?? 0
  const hasFilter = hasAnyFilter(filter)
  // 与后端 BulkBanMax 一致
  const tooMany = n > 500
  const disabled = !hasFilter || !n || tooMany

  const check = () => {
    const len = [...reason.trim()].length
    if (len < REASON_MIN || len > REASON_MAX) {
      setErrors({ reason: `请写清原因，${REASON_MIN} 到 ${REASON_MAX} 个字` })
      return
    }
    setConfirming(true)
  }

  const run = async () => {
    const payload = { ...filter, expected_total: n, reason: reason.trim() }
    try {
      const r = await api.post('v1/users/bulk/ban', bulkBanSchema, {
        body: payload,
        idempotencyKey: intent.keyFor(payload),
      })
      intent.reset()
      const skip = banSkippedText(r.skipped)
      toast(`已封禁 ${formatCount(r.banned)} 位${skip ? `，${skip}` : ''}`)
      setReason('')
      await invalidate()
      onDone()
    } catch (e) {
      fail(e, { fields: setErrors, intent })
    }
    setConfirming(false)
  }

  const actionMsg =
    errors.filter ||
    errors.expected_total ||
    (!hasFilter ? '至少选一个筛选条件' : tooMany ? '一次最多封禁 500 人，请收窄条件' : null)

  return (
    <div className={ops.mailForm}>
      <TextArea
        aria-label="封禁原因"
        rows={3}
        placeholder="写清封禁原因，5 到 500 个字"
        value={reason}
        error={errors.reason}
        hint="写进每个账号的审计"
        onChange={(e) => {
          setReason(e.target.value)
          setErrors({})
        }}
        data-autofocus=""
      />
      <div className={ops.mailActions}>
        {actionMsg && <span className={css.tone_warn}>{actionMsg}</span>}
        <Button size="sm" variant="danger" disabled={disabled} onClick={check}>
          封禁 {formatCount(n)} 位用户
        </Button>
      </div>
      <ConfirmModal
        open={confirming}
        title={`封禁 ${formatCount(n)} 位用户？`}
        body="这些账号的登录会话会立即失效，之后无法登录；自己、管理员、已封禁与已注销的会跳过。要解封只能逐个在用户详情里启用。"
        confirmLabel="封禁"
        tone="danger"
        onConfirm={run}
        onCancel={() => setConfirming(false)}
      />
    </div>
  )
}
