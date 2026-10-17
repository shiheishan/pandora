import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Checkbox, Input, Modal, Select, useToast } from '../../../ui'
import { batchEditBody } from './logic'
import css from './nodes.module.css'
import { useFailure, useIntentKey, useInvalidateNodes, usePools } from './queries'
import { RateScheduleEditor } from './RateScheduleEditor'
import { okUpdated, type NodeRow, type RateRange } from './schemas'
import { dedupeTags, parseLimitGiB, parseResetDay, rangesFromDrafts, scheduleError, splitTags, tagsError, type RateRangeDraft } from './xb'

export function NodeBatchEdit({
  rows,
  open,
  onClose,
  onDone,
}: {
  rows: NodeRow[]
  open: boolean
  onClose: () => void
  onDone: () => void
}) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const pools = usePools()
  const intent = useIntentKey()

  // 勾选状态
  const [editPool, setEditPool] = useState(false)
  const [editRate, setEditRate] = useState(false)
  const [editTags, setEditTags] = useState(false)
  const [editLimit, setEditLimit] = useState(false)
  const [editResetDay, setEditResetDay] = useState(false)
  const [editSchedule, setEditSchedule] = useState(false)

  // 字段草稿值
  const [poolId, setPoolId] = useState<string>('')
  const [trafficRate, setTrafficRate] = useState<string>('1.00')
  const [tagsText, setTagsText] = useState<string>('')
  const [limitGiB, setLimitGiB] = useState<string>('')
  const [resetDay, setResetDay] = useState<string>('')
  const [scheduleDrafts, setScheduleDrafts] = useState<RateRangeDraft[]>([])
  const [reason, setReason] = useState<string>('')

  // 校验错误状态
  const [rateErr, setRateErr] = useState<string | null>(null)
  const [tagsErr, setTagsErr] = useState<string | null>(null)
  const [limitErr, setLimitErr] = useState<string | null>(null)
  const [resetDayErr, setResetDayErr] = useState<string | null>(null)
  const [scheduleErr, setScheduleErr] = useState<string | null>(null)

  const resetForm = () => {
    setEditPool(false)
    setEditRate(false)
    setEditTags(false)
    setEditLimit(false)
    setEditResetDay(false)
    setEditSchedule(false)
    setPoolId('')
    setTrafficRate('1.00')
    setTagsText('')
    setLimitGiB('')
    setResetDay('')
    setScheduleDrafts([])
    setReason('')
    setRateErr(null)
    setTagsErr(null)
    setLimitErr(null)
    setResetDayErr(null)
    setScheduleErr(null)
  }

  const handleClose = () => {
    resetForm()
    onClose()
  }

  const anyChecked = editPool || editRate || editTags || editLimit || editResetDay || editSchedule

  const mutateBatch = useMutation({
    mutationFn: (body: ReturnType<typeof batchEditBody>) =>
      api.post('v1/nodes/edit:batch', okUpdated, {
        body,
        idempotencyKey: intent.keyFor(body),
      }),
    onSuccess: (res) => {
      intent.reset()
      toast(`已修改 ${res.updated} 个节点，订阅与节点端几秒内生效`)
      void invalidate()
      onDone()
      handleClose()
    },
    onError: (e) => {
      fail(e, { intent })
      void invalidate()
    },
  })

  const handleSubmit = () => {
    if (!anyChecked || mutateBatch.isPending) return

    let hasClientErr = false
    const set: Record<string, unknown> = {}

    if (editPool) {
      set.pool_id = poolId === '' ? null : poolId
    }

    if (editRate) {
      const r = Number(trafficRate.trim())
      if (!Number.isFinite(r) || r < 0 || r > 9999.99 || Math.round(r * 100) / 100 !== r) {
        setRateErr('倍率要在 0 到 9999.99 之间，最多两位小数')
        hasClientErr = true
      } else {
        setRateErr(null)
        set.traffic_rate = r
      }
    }

    if (editTags) {
      const rawTags = splitTags(tagsText)
      const err = tagsError(rawTags)
      if (err) {
        setTagsErr(err)
        hasClientErr = true
      } else {
        setTagsErr(null)
        set.tags = dedupeTags(rawTags)
      }
    }

    if (editLimit) {
      const parsed = parseLimitGiB(limitGiB)
      if (parsed === 'invalid') {
        setLimitErr('必须是正数（GiB）；不限请留空')
        hasClientErr = true
      } else {
        setLimitErr(null)
        set.traffic_limit_bytes = parsed
      }
    }

    if (editResetDay) {
      const parsed = parseResetDay(resetDay)
      if (parsed === 'invalid') {
        setResetDayErr('必须是 1 到 31 之间的整数；不自动重置请留空（短月取月末）')
        hasClientErr = true
      } else {
        setResetDayErr(null)
        set.traffic_reset_day = parsed
      }
    }

    if (editSchedule) {
      const ranges: RateRange[] = rangesFromDrafts(scheduleDrafts)
      const err = scheduleError(ranges)
      if (err) {
        setScheduleErr(err)
        hasClientErr = true
      } else {
        setScheduleErr(null)
        set.traffic_rate_schedule = ranges
      }
    }

    if (hasClientErr) return

    const body = batchEditBody(rows, set, reason)
    mutateBatch.mutate(body)
  }

  const poolOptions = [
    ...(pools.data ?? []).map((p) => ({ value: p.id, label: p.name })),
    { value: '', label: '不划入节点池' },
  ]

  return (
    <Modal
      open={open}
      onClose={handleClose}
      size="md"
      title={`批量修改 ${rows.length} 个节点`}
      eyebrow="整批一起生效；任一节点不合法整批不改"
      dismissible={!mutateBatch.isPending}
      actions={
        <>
          <Button size="dialog" onClick={handleClose} disabled={mutateBatch.isPending}>
            取消
          </Button>
          <Button
            size="dialog"
            variant="primary"
            disabled={!anyChecked || mutateBatch.isPending}
            busy={mutateBatch.isPending}
            onClick={handleSubmit}
          >
            保存
          </Button>
        </>
      }
    >
      <div className={css.batchForm}>
        {/* 节点池 */}
        <div className={css.batchItem}>
          <div className={css.batchCheck}>
            <Checkbox
              label="节点池"
              checked={editPool}
              disabled={mutateBatch.isPending}
              onChange={(e) => setEditPool(e.target.checked)}
            />
          </div>
          <div className={css.batchField}>
            <Select
              options={poolOptions}
              value={poolId}
              aria-label="节点池"
              disabled={!editPool || mutateBatch.isPending}
              onChange={(e) => setPoolId(e.target.value)}
              hint="在役节点必须属于一个池"
            />
          </div>
        </div>

        {/* 基础倍率 */}
        <div className={css.batchItem}>
          <div className={css.batchCheck}>
            <Checkbox
              label="基础倍率"
              checked={editRate}
              disabled={mutateBatch.isPending}
              onChange={(e) => setEditRate(e.target.checked)}
            />
          </div>
          <div className={css.batchField}>
            <Input
              type="number"
              step="0.01"
              min="0"
              max="9999.99"
              value={trafficRate}
              aria-label="基础倍率"
              disabled={!editRate || mutateBatch.isPending}
              error={editRate ? rateErr : undefined}
              onChange={(e) => {
                setTrafficRate(e.target.value)
                setRateErr(null)
              }}
              placeholder="1.00"
            />
          </div>
        </div>

        {/* 标签 */}
        <div className={css.batchItem}>
          <div className={css.batchCheck}>
            <Checkbox
              label="标签（整体替换）"
              checked={editTags}
              disabled={mutateBatch.isPending}
              onChange={(e) => setEditTags(e.target.checked)}
            />
          </div>
          <div className={css.batchField}>
            <Input
              value={tagsText}
              aria-label="标签"
              disabled={!editTags || mutateBatch.isPending}
              error={editTags ? tagsErr : undefined}
              onChange={(e) => {
                setTagsText(e.target.value)
                setTagsErr(null)
              }}
              placeholder="逗号或顿号分隔多个标签"
              hint="会替换这些节点原有的标签；留空 = 清空标签"
            />
          </div>
        </div>

        {/* 流量上限 */}
        <div className={css.batchItem}>
          <div className={css.batchCheck}>
            <Checkbox
              label="流量上限（GiB）"
              checked={editLimit}
              disabled={mutateBatch.isPending}
              onChange={(e) => setEditLimit(e.target.checked)}
            />
          </div>
          <div className={css.batchField}>
            <Input
              value={limitGiB}
              aria-label="流量上限（GiB）"
              disabled={!editLimit || mutateBatch.isPending}
              error={editLimit ? limitErr : undefined}
              onChange={(e) => {
                setLimitGiB(e.target.value)
                setLimitErr(null)
              }}
              placeholder="留空表示不限"
              hint="空 = 不限"
            />
          </div>
        </div>

        {/* 每月重置日 */}
        <div className={css.batchItem}>
          <div className={css.batchCheck}>
            <Checkbox
              label="每月重置日"
              checked={editResetDay}
              disabled={mutateBatch.isPending}
              onChange={(e) => setEditResetDay(e.target.checked)}
            />
          </div>
          <div className={css.batchField}>
            <Input
              value={resetDay}
              aria-label="每月重置日"
              disabled={!editResetDay || mutateBatch.isPending}
              error={editResetDay ? resetDayErr : undefined}
              onChange={(e) => {
                setResetDay(e.target.value)
                setResetDayErr(null)
              }}
              placeholder="1–31 的整数，留空表示不自动重置"
              hint="空 = 不自动重置"
            />
          </div>
        </div>

        {/* 时段倍率 */}
        <div className={css.batchItem}>
          <div className={css.batchCheck}>
            <Checkbox
              label="时段倍率（整体替换）"
              checked={editSchedule}
              disabled={mutateBatch.isPending}
              onChange={(e) => setEditSchedule(e.target.checked)}
            />
          </div>
          <div className={css.batchField}>
            <RateScheduleEditor
              drafts={scheduleDrafts}
              onChange={(next) => {
                setScheduleDrafts(next)
                setScheduleErr(null)
              }}
              disabled={!editSchedule || mutateBatch.isPending}
              error={editSchedule ? scheduleErr : undefined}
            />
            <div className={css.fieldHint}>空表 = 清空时段</div>
          </div>
        </div>

        {/* 原因 */}
        <div className={css.batchItem}>
          <div className={css.batchCheck}>
            <span className={css.optionalLabel}>变更原因（选填）</span>
          </div>
          <div className={css.batchField}>
            <Input
              maxLength={500}
              value={reason}
              aria-label="变更原因"
              disabled={mutateBatch.isPending}
              onChange={(e) => setReason(e.target.value)}
              placeholder="记录到审计日志，最多 500 字"
            />
          </div>
        </div>
      </div>
    </Modal>
  )
}
