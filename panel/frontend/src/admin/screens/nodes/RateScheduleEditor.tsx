import { Button, Input } from '../../../ui'
import { NODE_RATE_SCHEDULE_MAX, type RateRangeDraft } from './xb'
import css from './xb.module.css'

export function RateScheduleEditor({
  drafts,
  onChange,
  disabled,
  error,
}: {
  drafts: RateRangeDraft[]
  onChange: (next: RateRangeDraft[]) => void
  disabled?: boolean
  error?: string | null
}) {
  const atMax = drafts.length >= NODE_RATE_SCHEDULE_MAX

  const updateDraft = (index: number, patch: Partial<RateRangeDraft>) => {
    const next = drafts.map((d, i) => (i === index ? { ...d, ...patch } : d))
    onChange(next)
  }

  const removeRow = (index: number) => {
    onChange(drafts.filter((_, i) => i !== index))
  }

  const addRow = () => {
    if (atMax) return
    onChange([...drafts, { start: '', end: '', rate: '1' }])
  }

  return (
    <div>
      <div className={css.scheduleList}>
        {drafts.map((d, i) => {
          const num = i + 1
          return (
            <div key={i} className={css.scheduleRow}>
              <div className={css.timeInput}>
                <Input
                  value={d.start}
                  placeholder="08:00"
                  aria-label={`第 ${num} 段开始`}
                  disabled={disabled}
                  onChange={(e) => updateDraft(i, { start: e.target.value })}
                />
              </div>
              <span className={css.sep}>至</span>
              <div className={css.timeInput}>
                <Input
                  value={d.end}
                  placeholder="12:00"
                  aria-label={`第 ${num} 段结束`}
                  disabled={disabled}
                  onChange={(e) => updateDraft(i, { end: e.target.value })}
                />
              </div>
              <span className={css.sep}>倍率</span>
              <div className={css.rateInput}>
                <Input
                  type="number"
                  step="0.01"
                  value={d.rate}
                  placeholder="1.0"
                  aria-label={`第 ${num} 段倍率`}
                  disabled={disabled}
                  onChange={(e) => updateDraft(i, { rate: e.target.value })}
                />
              </div>
              <Button
                size="sm"
                variant="ghost"
                aria-label={`删除第 ${num} 段`}
                disabled={disabled}
                onClick={() => removeRow(i)}
              >
                删除
              </Button>
            </div>
          )
        })}
      </div>
      <div className={css.actionRow}>
        <Button
          size="sm"
          disabled={disabled || atMax}
          title={atMax ? `最多添加 ${NODE_RATE_SCHEDULE_MAX} 个时段` : undefined}
          onClick={addRow}
        >
          添加时段
        </Button>
      </div>
      {error && <div className={css.errorText}>{error}</div>}
    </div>
  )
}
