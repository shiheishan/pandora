import { ArrowRight01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import type { ReactNode } from 'react'
import { href } from '../../../core/router'
import { Button, Skeleton } from '../../../ui'
import { modulePath } from '../../modules'
import type { Target } from './model'
import css from './parts.module.css'

// ---------------------------------------------------------------------------
// 仪表盘各卡共用的小件（样稿 admin-v4 #p-dash）：分节（标题在卡片外）、箭头链接、
// 卡内空状态 / 出错 / 骨架。样式只在 parts.module.css，各卡自己的样式放各自的 module。
// 卡片外壳用 ui 的 Card（样稿 .box / .bh / .bf），分段切换用 ui 的 Segmented。
// ---------------------------------------------------------------------------

export function targetHref(target: Target): string {
  return href(modulePath(target.module, target.tab), target.query)
}

/** 分节（样稿 .blk + .sh）：16px 标题 + 灰色小字 + 右侧操作，下面是卡片 */
export function Section({ id, title, sub, extra, children }: { id: string; title: string; sub?: ReactNode; extra?: ReactNode; children: ReactNode }) {
  return (
    <section className={css.section} aria-labelledby={id}>
      <div className={css.sectionHead}>
        <h2 id={id} className={css.sectionTitle}>
          {title}
        </h2>
        {sub != null && <span className={css.sub}>{sub}</span>}
        {extra != null && (
          <>
            <span className={css.spacer} />
            {extra}
          </>
        )}
      </div>
      {children}
    </section>
  )
}

/** 带右箭头的灰色文字链接（样稿 .link：「看订单 ›」「全部节点 ›」） */
export function ArrowLink({ to, children }: { to: Target; children: ReactNode }) {
  return (
    <a className={css.link} href={targetHref(to)}>
      {children}
      <HugeiconsIcon icon={ArrowRight01Icon} size={14} strokeWidth={1.8} aria-hidden="true" />
    </a>
  )
}

/** 卡内空状态：一句标题、一句说明、一个去处理的链接（各图的「还没有…」） */
export function CardEmpty({ title, text, link }: { title: string; text: string; link?: { label: string; to: Target } }) {
  return (
    <div className={css.empty}>
      <span className={css.emptyTitle}>{title}</span>
      <span className={css.emptyText}>{text}</span>
      {link && (
        <span className={css.emptyLink}>
          <ArrowLink to={link.to}>{link.label}</ArrowLink>
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

/** 卡内骨架：rows 行、每行 height 高 */
export function PanelSkeleton({ rows = 5, height = 14 }: { rows?: number; height?: number }) {
  return (
    <div className={css.skeletonStack} role="status" aria-label="加载中">
      {Array.from({ length: rows }, (_, i) => (
        <Skeleton key={i} height={height} />
      ))}
    </div>
  )
}
