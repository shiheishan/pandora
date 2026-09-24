/**
 * [INPUT]: 依赖 ../../../core/api 的 isApiError，依赖 ../../../core/router 的 href，依赖 ../../../ui 的 Button / Skeleton，依赖 ../../modules 的 modulePath / Permissions，依赖 ./model 的 Target / Tone，依赖 ./Dash.module.css
 * [OUTPUT]: 对外提供 isForbidden、targetHref、Dot、CardError、PanelSkeleton、toneText
 * [POS]: admin/screens/dash 各卡片共用的小部件：卡片内错误行（带重试，404 由调用方先当无权限隐藏）、骨架、状态圆点、链接拼接；只在本目录用，不进 ui/
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { isApiError } from '../../../core/api'
import { href } from '../../../core/router'
import { Button, Skeleton } from '../../../ui'
import { modulePath, type Permissions } from '../../modules'
import css from './Dash.module.css'
import type { Target, Tone } from './model'

/** 缺权限的接口回 404：按「无权限或不存在」处理，整张卡不画，不当作报错 */
export function isForbidden(error: unknown): boolean {
  return isApiError(error, 'not_found')
}

export function targetHref(target: Target, perms: Permissions): string {
  return href(modulePath(target.module, target.tab, perms))
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
