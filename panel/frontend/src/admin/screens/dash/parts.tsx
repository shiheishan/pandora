import { ArrowRight02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useRef, type KeyboardEvent, type ReactNode } from 'react'
import { href } from '../../../core/router'
import { Button, Skeleton } from '../../../ui'
import { modulePath } from '../../modules'
import type { Target, Tone } from './model'
import css from './parts.module.css'

// ---------------------------------------------------------------------------
// 仪表盘各卡共用的小件：分区标题、卡片标题条、文字页签、卡内空状态 / 出错 / 骨架。
// 样式只在 parts.module.css，各卡自己的样式放各自的 module。
// ---------------------------------------------------------------------------

export function targetHref(target: Target): string {
  return href(modulePath(target.module, target.tab), target.query)
}

export function Dot({ tone }: { tone: Tone }) {
  return <span className={`${css.dot} ${css[tone]}`} aria-hidden="true" />
}

/** 状态色文字（KPI 副行、变化百分比、需要关注的 meta） */
export function toneText(tone: Tone): string | undefined {
  return tone === 'ok' ? css.toneOk : tone === 'danger' ? css.toneDanger : tone === 'warn' ? css.toneWarn : undefined
}

/** 分区标题：「待办」「经营」「运行」，旁边一行灰字 */
export function GroupHead({ id, title, hint }: { id: string; title: string; hint?: ReactNode }) {
  return (
    <div className={css.groupHead}>
      <h2 id={id} className={css.groupTitle}>
        {title}
      </h2>
      {hint != null && <span className={css.hint}>{hint}</span>}
    </div>
  )
}

/** 卡片标题条：标题 + 灰字 + 右侧操作，下面一条墨色线 */
export function PanelHead({ id, title, hint, children }: { id: string; title: string; hint?: ReactNode; children?: ReactNode }) {
  return (
    <div className={css.panelHead}>
      <h3 id={id} className={css.panelTitle}>
        {title}
      </h3>
      {hint != null && <span className={css.hint}>{hint}</span>}
      {children != null && (
        <>
          <span className={css.spacer} />
          <div className={css.panelTools}>{children}</div>
        </>
      )}
    </div>
  )
}

/** 标题条里的竖分隔 */
export function ToolSep() {
  return <span className={css.toolSep} aria-hidden="true" />
}

export interface TextTabOption<T extends string> {
  value: T
  label: string
}

/** 文字页签（设计稿：选中加粗 + 下划线）。radiogroup，方向键切换 */
export function TextTabs<T extends string>({ label, options, value, onChange }: { label: string; options: readonly TextTabOption<T>[]; value: T; onChange: (v: T) => void }) {
  const refs = useRef<(HTMLButtonElement | null)[]>([])
  const onKey = (e: KeyboardEvent, i: number) => {
    const step = e.key === 'ArrowRight' || e.key === 'ArrowDown' ? 1 : e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? -1 : 0
    if (!step) return
    e.preventDefault()
    const next = (i + step + options.length) % options.length
    onChange(options[next]!.value)
    refs.current[next]?.focus()
  }
  return (
    <div role="radiogroup" aria-label={label} className={css.textTabs}>
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
            className={on ? `${css.textTab} ${css.textTabOn}` : css.textTab}
            onClick={() => onChange(o.value)}
            onKeyDown={(e) => onKey(e, i)}
          >
            {o.label}
          </button>
        )
      })}
    </div>
  )
}

/** 带箭头的文字链接（「查看全部节点 →」「去配置套餐 →」） */
export function ArrowLink({ to, children, underline = false }: { to: Target; children: ReactNode; underline?: boolean }) {
  return (
    <a className={underline ? `${css.arrowLink} ${css.arrowLinkUnderline}` : css.arrowLink} href={targetHref(to)}>
      {children}
      <HugeiconsIcon icon={ArrowRight02Icon} size={12} strokeWidth={2} aria-hidden="true" />
    </a>
  )
}

/** 卡内空状态：一句标题、一句说明、一个去处理的链接（设计稿各图的「还没有…」） */
export function CardEmpty({ title, text, link }: { title: string; text: string; link?: { label: string; to: Target } }) {
  return (
    <div className={css.empty}>
      <span className={css.emptyTitle}>{title}</span>
      <span className={css.hint}>{text}</span>
      {link && (
        <span className={css.emptyLink}>
          <ArrowLink to={link.to} underline>
            {link.label}
          </ArrowLink>
        </span>
      )}
    </div>
  )
}

/** 卡片内的失败：说明哪块没取到 + 服务端文案 + 重试；其它卡片不受影响（DASH-01 局部失败） */
export function CardError({ what, error, onRetry }: { what: string; error: unknown; onRetry: () => void }) {
  const message = error instanceof Error ? error.message : '未知错误'
  return (
    <div className={css.error} role="alert">
      <span>
        {what}加载失败：{message}
      </span>
      <Button variant="secondary" size="xs" onClick={onRetry}>
        重试
      </Button>
    </div>
  )
}

export function PanelSkeleton({ rows = 5, height = 14 }: { rows?: number; height?: number }) {
  return (
    <div className={css.skeletonStack} role="status" aria-label="加载中">
      {Array.from({ length: rows }, (_, i) => (
        <Skeleton key={i} height={height} />
      ))}
    </div>
  )
}
