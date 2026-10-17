import { HugeiconsIcon, type IconSvgElement } from '@hugeicons/react'
import { useId, useRef, type KeyboardEvent, type ReactNode } from 'react'
import { Switch } from '../../ui'
import { cx } from './cx'
import css from './Settings.module.css'

export interface SettingRowProps {
  /** 左侧方块图标（Hugeicons Pro stroke-rounded 的图标对象）；不传时不画图标列 */
  icon?: IconSvgElement
  title: ReactNode
  description?: ReactNode
  checked: boolean
  onChange: (checked: boolean) => void
  disabled?: boolean
  /** danger：打开时开关是红色 */
  tone?: 'default' | 'danger'
  /** 打开后在行下方展开的配置（样稿 .rv）；关着时不渲染 */
  children?: ReactNode
}

/** 对应样稿 .srow + .rv */
export function SettingRow({
  icon,
  title,
  description,
  checked,
  onChange,
  disabled,
  tone = 'default',
  children,
}: SettingRowProps) {
  const titleId = useId()

  return (
    <div className={css.item}>
      <div className={css.row}>
        {icon != null && (
          <span className={css.icon}>
            <HugeiconsIcon icon={icon} size={16} strokeWidth={1.6} aria-hidden="true" />
          </span>
        )}
        <div className={css.text}>
          <b className={css.title} id={titleId}>{title}</b>
          {description != null && <small className={css.description}>{description}</small>}
        </div>
        <Switch
          checked={checked}
          disabled={disabled}
          tone={tone}
          aria-labelledby={titleId}
          onChange={(e) => onChange(e.target.checked)}
          className={css.switch}
        />
      </div>
      {checked && children != null && <div className={css.reveal}>{children}</div>}
    </div>
  )
}

export interface RadioCardOption<T extends string> {
  value: T
  title: ReactNode
  description?: ReactNode
  disabled?: boolean
}

export interface RadioCardsProps<T extends string> {
  /** 无障碍名称，如「验证服务」 */
  label: string
  options: readonly RadioCardOption<T>[]
  value: T
  onChange: (value: T) => void
  /** 一行几列，默认 3；窄于 640 一律一列 */
  columns?: 2 | 3
  className?: string
}

/**
 * 方向键在单选卡片之间移动：→ ↓ 下一个、← ↑ 上一个（首尾循环），Home 第一个、End 最后一个；跳过 disabled。
 * 返回新下标；不是这几个键、或没有可用项时返回 -1。
 */
export function nextRadioIndex(current: number, key: string, disabled: readonly boolean[]): number {
  const n = disabled.length
  if (n === 0) return -1

  const enabledIndices: number[] = []
  for (let i = 0; i < n; i += 1) {
    if (!disabled[i]) enabledIndices.push(i)
  }
  if (enabledIndices.length === 0) return -1

  const pos = enabledIndices.indexOf(current)
  const safePos = pos >= 0 ? pos : 0

  if (key === 'ArrowRight' || key === 'ArrowDown') {
    return enabledIndices[(safePos + 1) % enabledIndices.length]!
  }
  if (key === 'ArrowLeft' || key === 'ArrowUp') {
    return enabledIndices[(safePos - 1 + enabledIndices.length) % enabledIndices.length]!
  }
  if (key === 'Home') return enabledIndices[0]!
  if (key === 'End') return enabledIndices[enabledIndices.length - 1]!
  return -1
}

export function RadioCards<T extends string>({
  label,
  options,
  value,
  onChange,
  columns = 3,
  className,
}: RadioCardsProps<T>) {
  const refs = useRef<(HTMLButtonElement | null)[]>([])
  const disabledFlags = options.map((o) => o.disabled === true)
  const currentIndex = options.findIndex((o) => o.value === value)

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const next = nextRadioIndex(currentIndex, event.key, disabledFlags)
    if (next < 0) return
    event.preventDefault()
    onChange(options[next]!.value)
    refs.current[next]?.focus()
  }

  return (
    <div
      role="radiogroup"
      aria-label={label}
      className={cx(css.cards, columns === 2 && css.two, className)}
      onKeyDown={onKeyDown}
    >
      {options.map((o, i) => {
        const on = o.value === value
        return (
          <button
            key={o.value}
            ref={(el) => {
              refs.current[i] = el
            }}
            type="button"
            role="radio"
            aria-checked={on}
            tabIndex={on ? 0 : -1}
            disabled={o.disabled}
            className={cx(css.card, on && css.on)}
            onClick={() => onChange(o.value)}
          >
            <i className={css.dot} aria-hidden="true" />
            <div>
              <b className={css.cardTitle}>{o.title}</b>
              {o.description != null && <small className={css.cardDescription}>{o.description}</small>}
            </div>
          </button>
        )
      })}
    </div>
  )
}
