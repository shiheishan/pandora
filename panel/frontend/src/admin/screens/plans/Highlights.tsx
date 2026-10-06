import { Button, Input, Switch } from '../../../ui'
import { HIGHLIGHT_CHARS, HIGHLIGHT_MAX } from './model'
import css from './Plans.module.css'

export function HighlightsField({
  highlights,
  recommended,
  onHighlights,
  onRecommended,
  errors,
}: {
  highlights: readonly string[]
  recommended: boolean
  onHighlights: (list: string[]) => void
  onRecommended: (on: boolean) => void
  errors: Record<string, string>
}) {
  const update = (i: number, text: string) => onHighlights(highlights.map((h, j) => (j === i ? text : h)))
  return (
    <fieldset className={`${css.stack} ${css.fieldset}`}>
      <legend className={css.legend}>卖点（最多 {HIGHLIGHT_MAX} 条）</legend>
      {highlights.map((h, i) => (
        <div key={i} className={css.highlightRow}>
          <Input
            aria-label={`第 ${i + 1} 条卖点`}
            placeholder={`如「流媒体解锁」，${HIGHLIGHT_CHARS} 字以内`}
            value={h}
            onChange={(e) => update(i, e.target.value)}
            error={errors[`highlights.${i}`]}
          />
          <Button variant="ghost" onClick={() => onHighlights(highlights.filter((_, j) => j !== i))}>
            移除
          </Button>
        </div>
      ))}
      {highlights.length < HIGHLIGHT_MAX && (
        <Button size="sm" variant="outline" className={css.alignStart} onClick={() => onHighlights([...highlights, ''])}>
          ＋ 添加卖点
        </Button>
      )}
      {errors.highlights && (
        <p className={css.fieldError} role="alert">
          {errors.highlights}
        </p>
      )}
      <p className={css.small}>门户套餐卡按这个顺序列出；流量、设备、重置与限速会自动显示，不用写进卖点。</p>
      <Switch label="标为推荐（门户套餐卡显示「推荐」）" checked={recommended} onChange={(e) => onRecommended(e.target.checked)} />
      {errors.recommended && (
        <p className={css.fieldError} role="alert">
          {errors.recommended}
        </p>
      )}
    </fieldset>
  )
}
