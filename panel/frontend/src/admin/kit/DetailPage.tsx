import { ArrowLeft01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import type { ReactNode } from 'react'
import { Tabs } from '../../ui'
import css from './DetailPage.module.css'

export interface DetailPageTabs<T extends string> {
  /** 页签组的无障碍名称 */
  label: string
  /** badge：页签名后的计数（读屏单独读，不混进页签名） */
  items: readonly { value: T; label: ReactNode; badge?: ReactNode }[]
  value: T
  onChange: (value: T) => void
}

export interface DetailPageProps<T extends string = string> {
  /** 返回链接：href 是完整的 '#/…' 地址（调用方用 core/router 的 href(path, query) 生成，带上列表原来的查询串），label 是「用户列表」这类文案 */
  back: { href: string; label: string }
  title: ReactNode
  /** 标题前：头像（kit 的 Avatar size="lg"）或前缀 */
  lead?: ReactNode
  /** 标题下的副行：ID + CopyButton、StatusDot 等 */
  meta?: ReactNode
  /** 头部右侧操作（几个 Button size="sm"） */
  actions?: ReactNode
  /** 页签；地址由调用方管 */
  tabs?: DetailPageTabs<T>
  /** 右栏：300 宽、吸顶；窄于 960 变单栏，排在主栏之后 */
  side?: ReactNode
  children: ReactNode
}

/** 对应样稿 .dpage */
export function DetailPage<T extends string = string>({
  back,
  title,
  lead,
  meta,
  actions,
  tabs,
  side,
  children,
}: DetailPageProps<T>) {
  const hasSide = side != null

  const body = hasSide ? (
    <div className={css.grid}>
      <div className={css.main}>{children}</div>
      <aside className={css.side}>{side}</aside>
    </div>
  ) : (
    <div className={css.main}>{children}</div>
  )

  return (
    <div>
      <a className={css.back} href={back.href}>
        <HugeiconsIcon icon={ArrowLeft01Icon} size={15} strokeWidth={1.6} aria-hidden />
        {back.label}
      </a>
      <header className={css.head}>
        {lead != null && <div className={css.lead}>{lead}</div>}
        <div className={css.headText}>
          <h1 className={css.title}>{title}</h1>
          {meta != null && <div className={css.meta}>{meta}</div>}
        </div>
        {actions != null && <div className={css.actions}>{actions}</div>}
      </header>
      {tabs != null && (
        <Tabs label={tabs.label} items={tabs.items} value={tabs.value} onChange={tabs.onChange} className={css.nav} />
      )}
      <div className={tabs == null ? css.contentNoTabs : undefined}>{body}</div>
    </div>
  )
}
