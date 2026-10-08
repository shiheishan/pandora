import { useId } from 'react'
import { Button, Tag } from '../../../ui'
import css from './Billing.module.css'
import { badgeLabel, placementResult, placementTitle } from './placement'
import type { Placement } from './schemas'

/**
 * 「这单落到哪一份」：服务端给的选项一项一行，每行写明选了会发生什么。
 * 只有一个选项时不让选，直接把结果写出来；没有默认值时一个都不选，由管理员自己点。
 */
export function PlacementPicker({
  options,
  selected,
  targetPlan,
  settlement,
  entrySubscriptionId,
  onPick,
}: {
  options: readonly Placement[]
  selected: string
  /** 这张单开的套餐名，写进「换成…」「另开一份…」 */
  targetPlan: string
  settlement: 'grant' | 'pending' | 'offline'
  /** 从订阅行点「给这份开单」进来时的那份，在对应选项上标「你点的这份」 */
  entrySubscriptionId: string | null
  onPick: (key: string) => void
}) {
  const prefix = useId()
  if (options.length === 1) {
    const only = options[0]!
    return (
      <div className={css.stack} role="group" aria-label="这单落到哪一份">
        <span className={css.fieldLabel}>这单落到哪一份</span>
        <div className={`${css.placeItem} ${css.placeItemOn}`}>
          <div className={css.placeText}>
            <span className={css.placeTitle}>{placementTitle(only, targetPlan)}</span>
            <span className={css.placeResult}>{placementResult(only, settlement)}</span>
          </div>
        </div>
      </div>
    )
  }
  return (
    <fieldset className={`${css.stack} ${css.fieldset}`}>
      <legend className={css.fieldLabel}>这单落到哪一份（必选）</legend>
      <div className={css.placeList}>
        {options.map((o) => {
          const on = o.key === selected
          const badge = badgeLabel(o.badge)
          // 名字取标题、说明取结果：读屏与浏览器检查工具都读得到每一项写了什么（不是内部的 key）
          const titleId = `${prefix}-${o.key}-t`
          const resultId = `${prefix}-${o.key}-r`
          return (
            <label key={o.key} className={on ? `${css.placeItem} ${css.placeItemOn}` : css.placeItem}>
              <input
                type="radio"
                name="manual-placement"
                className={css.placeRadio}
                value={o.key}
                checked={on}
                aria-labelledby={titleId}
                aria-describedby={resultId}
                onChange={() => onPick(o.key)}
              />
              <span className={css.placeText}>
                <span id={titleId} className={css.placeTitle}>
                  {placementTitle(o, targetPlan)}
                  {badge && <Tag tone="info">{badge}</Tag>}
                  {entrySubscriptionId !== null && o.subscription_id === entrySubscriptionId && <Tag tone="outline">你点的这份</Tag>}
                </span>
                <span id={resultId} className={css.placeResult}>
                  {placementResult(o, settlement)}
                </span>
              </span>
            </label>
          )
        })}
      </div>
    </fieldset>
  )
}

/** 落点读取失败：写明原因，给一个重试按钮（按钮灰着时管理员才知道为什么） */
export function PlacementError({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <div className={css.placeError} role="alert">
      <span>落点读取失败：{message}</span>
      <Button size="xs" onClick={onRetry}>
        重试
      </Button>
    </div>
  )
}
