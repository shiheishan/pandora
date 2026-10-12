import { href } from '../../../core/router'
import { Button, Skeleton } from '../../../ui'
import { modulePath } from '../../modules'
import css from './Dash.module.css'
import type { Target, Tone } from './model'

export function targetHref(target: Target): string {
  return href(modulePath(target.module, target.tab), target.query)
}

export function Dot({ tone }: { tone: Tone }) {
  return <span className={`${css.dot} ${css[tone]}`} aria-hidden="true" />
}

/** 状态色文字（KPI 副行、系统状态 meta） */
export function toneText(tone: Tone): string | undefined {
  return tone === 'ok' ? css.toneOk : tone === 'danger' ? css.toneDanger : tone === 'warn' ? css.toneWarn : undefined
}

/** 卡片内的失败：说明哪块没取到 + 服务端文案 + 重试；其它卡片不受影响（DASH-01 局部失败） */
export function CardError({ what, error, onRetry, boxed = false }: { what: string; error: unknown; onRetry: () => void; boxed?: boolean }) {
  const message = error instanceof Error ? error.message : '未知错误'
  return (
    <div className={boxed ? `${css.error} ${css.errorBox}` : css.error} role="alert">
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
