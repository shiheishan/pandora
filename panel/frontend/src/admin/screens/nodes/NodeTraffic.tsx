import { useMutation } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { formatDateTime } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Input, Tag, useToast } from '../../../ui'
import { useFailure, useIntentKey, useInvalidateNodes } from './queries'
import { RateScheduleEditor } from './RateScheduleEditor'
import { adminNodeSchema, okResponse, type NodeRow } from './schemas'
import {
  dedupeTags,
  draftsFromRanges,
  limitToGiBText,
  parseLimitGiB,
  parseResetDay,
  rangesFromDrafts,
  scheduleError,
  splitTags,
  tagsError,
  trafficPercent,
  trafficUsageLabel,
  type RateRangeDraft,
} from './xb'
import css from './xb.module.css'

export function NodeTraffic({ node, baseRate }: { node: NodeRow; baseRate: number | null }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const resetTrafficIntent = useIntentKey()

  const retired = node.serving_status === 'retired'

  const [tagText, setTagText] = useState(() => node.tags.join('，'))
  const [limitText, setLimitText] = useState(() => limitToGiBText(node.traffic.limit_bytes))
  const [resetDayText, setResetDayText] = useState(() => (node.traffic.reset_day === null ? '' : String(node.traffic.reset_day)))
  const [drafts, setDrafts] = useState<RateRangeDraft[]>(() => draftsFromRanges(node.traffic_rate_schedule))

  const [errors, setErrors] = useState<{
    tags?: string
    traffic_limit_bytes?: string
    traffic_reset_day?: string
    traffic_rate_schedule?: string
  }>({})

  const [resetModalOpen, setResetModalOpen] = useState(false)
  const [resetReason, setResetReason] = useState('')

  const percent = trafficPercent(node.traffic)

  const saveMutation = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      api.request(`v1/nodes/${node.id}`, adminNodeSchema, { method: 'PATCH', body }),
    onSuccess: () => {
      toast('已保存，订阅与节点端几秒内生效')
      void invalidate()
    },
    onError: (e) => {
      fail(e, (fields) => {
        setErrors((prev) => ({
          ...prev,
          tags: fields.tags,
          traffic_limit_bytes: fields.traffic_limit_bytes,
          traffic_reset_day: fields.traffic_reset_day,
          traffic_rate_schedule: fields.traffic_rate_schedule,
        }))
      })
    },
  })

  const resetMutation = useMutation({
    mutationFn: (body: { reason: string }) =>
      api.post(`v1/nodes/${node.id}/traffic/reset`, okResponse, {
        body,
        idempotencyKey: resetTrafficIntent.keyFor([node.id, body]),
      }),
    onSuccess: () => {
      resetTrafficIntent.reset()
      toast('本周期用量已清零')
      setResetModalOpen(false)
      setResetReason('')
      void invalidate()
    },
    onError: (e) => {
      fail(e, { intent: resetTrafficIntent })
    },
  })

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (retired) return

    const newErrors: typeof errors = {}

    // 1. 标签校验
    const parsedTags = dedupeTags(splitTags(tagText))
    const tErr = tagsError(parsedTags)
    if (tErr) newErrors.tags = tErr

    // 2. 流量上限与重置日校验
    const parsedLimit = parseLimitGiB(limitText)
    if (parsedLimit === 'invalid') newErrors.traffic_limit_bytes = '必须是正数（GiB）；不限请留空'

    const parsedDay = parseResetDay(resetDayText)
    if (parsedDay === 'invalid') newErrors.traffic_reset_day = '必须是 1 到 31 之间的整数；不自动重置请留空（短月取月末）'

    // 3. 时段倍率校验
    const parsedRanges = rangesFromDrafts(drafts)
    const sErr = scheduleError(parsedRanges)
    if (sErr) newErrors.traffic_rate_schedule = sErr

    if (Object.keys(newErrors).length > 0) {
      setErrors(newErrors)
      return
    }
    setErrors({})

    // 差量比对
    const body: Record<string, unknown> = { row_version: node.row_version }
    let changed = false

    // tags 比对
    const tagsChanged =
      parsedTags.length !== node.tags.length || parsedTags.some((t, i) => t !== node.tags[i])
    if (tagsChanged) {
      body.tags = parsedTags
      changed = true
    }

    // traffic_limit_bytes 比对
    if (parsedLimit !== node.traffic.limit_bytes) {
      body.traffic_limit_bytes = parsedLimit
      changed = true
    }

    // traffic_reset_day 比对
    if (parsedDay !== node.traffic.reset_day) {
      body.traffic_reset_day = parsedDay
      changed = true
    }

    // traffic_rate_schedule 比对
    if (JSON.stringify(parsedRanges) !== JSON.stringify(node.traffic_rate_schedule)) {
      body.traffic_rate_schedule = parsedRanges
      changed = true
    }

    if (!changed) {
      toast('没有改动')
      return
    }

    saveMutation.mutate(body)
  }

  return (
    <form onSubmit={handleSubmit}>
      {/* 标签 */}
      <section className={css.section}>
        <div className={css.sectionTitle}>标签</div>
        <div className={css.sectionDesc}>逗号或顿号分隔，最多 10 个、每个最多 16 个字；门户的节点列表与订阅里会显示</div>
        <Input
          label="节点标签"
          value={tagText}
          placeholder="例如：专线，流媒体，香港"
          disabled={retired}
          error={errors.tags}
          onChange={(e) => {
            setTagText(e.target.value)
            if (errors.tags) setErrors((prev) => ({ ...prev, tags: undefined }))
          }}
        />
      </section>

      {/* 流量上限 */}
      <section className={css.section}>
        <div className={css.sectionTitle}>流量上限</div>
        <div className={css.sectionDesc}>限制该节点每个计费周期的流量总额；超限将暂停下发</div>
        <div className={css.limitRow}>
          <div className={css.limitField}>
            <Input
              label="每周期上限（GiB）"
              value={limitText}
              placeholder="不限"
              disabled={retired}
              error={errors.traffic_limit_bytes}
              onChange={(e) => {
                setLimitText(e.target.value)
                if (errors.traffic_limit_bytes) setErrors((prev) => ({ ...prev, traffic_limit_bytes: undefined }))
              }}
            />
          </div>
          <div className={css.limitField}>
            <Input
              label="每月重置日"
              hint="1–31，按站点时区零点；短月取月末"
              value={resetDayText}
              placeholder="不自动重置"
              disabled={retired}
              error={errors.traffic_reset_day}
              onChange={(e) => {
                setResetDayText(e.target.value)
                if (errors.traffic_reset_day) setErrors((prev) => ({ ...prev, traffic_reset_day: undefined }))
              }}
            />
          </div>
        </div>

        <div className={css.usageRow}>
          <div className={css.usageText}>
            <span>本周期用量：{trafficUsageLabel(node.traffic)}</span>
            {percent !== null && <span>{percent}%</span>}
          </div>
          {percent !== null && (
            <div className={css.loadBar}>
              <span
                className={`${css.loadFill} ${percent >= 90 ? css.hot : percent >= 75 ? css.warm : ''}`}
                style={{ width: `${percent}%` }}
              />
            </div>
          )}
          <div>本周期起点：{formatDateTime(node.traffic.reset_at)}</div>
        </div>

        {node.traffic.exceeded_at && (
          <div className={css.exceededBox}>
            <Tag tone="danger">已超限，已停止下发</Tag>
            <div className={css.exceededHint}>
              调高上限、清掉上限或立即重置后恢复下发
            </div>
          </div>
        )}

        <div className={css.actionRow}>
          <Button
            size="sm"
            type="button"
            variant="ghost"
            disabled={retired}
            onClick={() => setResetModalOpen(true)}
          >
            立即重置用量
          </Button>
        </div>
      </section>

      {/* 时段倍率 */}
      <section className={css.section}>
        <div className={css.sectionTitle}>时段倍率</div>
        <div className={css.sectionDesc}>
          按站点时区；不在任何时段时用基础倍率 {baseRate !== null ? baseRate : '—'}；跨零点请拆成两段；用户流量按上报到达时所在时段计
        </div>
        <RateScheduleEditor
          drafts={drafts}
          onChange={(next) => {
            setDrafts(next)
            if (errors.traffic_rate_schedule) setErrors((prev) => ({ ...prev, traffic_rate_schedule: undefined }))
          }}
          disabled={retired}
          error={errors.traffic_rate_schedule}
        />
      </section>

      <div className={css.formFooter}>
        <Button type="submit" busy={saveMutation.isPending} disabled={retired}>
          保存
        </Button>
      </div>

      <ConfirmModal
        open={resetModalOpen}
        title="立即重置节点用量？"
        body={
          <div>
            <p className={css.modalNotice}>重置后该节点当前周期的已用流量将立即清零，超限状态也将解除。操作需要重新验证身份。</p>
            <Input
              label="原因（可选）"
              maxLength={500}
              value={resetReason}
              placeholder="可选：填写重置原因"
              onChange={(e) => setResetReason(e.target.value)}
            />
          </div>
        }
        confirmLabel="确认重置"
        onConfirm={() =>
          resetMutation.mutateAsync({ reason: resetReason.trim() }).then(
            () => undefined,
            () => undefined,
          )
        }
        onCancel={() => {
          setResetModalOpen(false)
          setResetReason('')
        }}
      />
    </form>
  )
}
