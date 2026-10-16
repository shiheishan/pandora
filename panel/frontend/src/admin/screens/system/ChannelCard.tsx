import type { ReactNode } from 'react'
import type { Tone } from './logic'
import css from './notify.module.css'

const TONE_CLASS: Record<Tone, string> = { ok: css.tagOk, warn: css.tagWarn, muted: css.tagMuted }

/** 通知渠道卡（一行）：图标 + 标题/状态标签 + 一行描述；给了 onOpen 整卡可点（键盘可达）并在右侧显示「配置」，没给（站内信）时是不可点的 div */
export function ChannelCard({ icon, title, tag, dirty = false, desc, onOpen }: {
  icon?: ReactNode
  title: string
  /** 渠道状态标签；注册卡没有 */
  tag?: { label: string; tone: Tone }
  /** 有未保存修改时多一个橙色「未保存」标签 */
  dirty?: boolean
  desc: string
  onOpen?: () => void
}) {
  const body = (
    <>
      {icon != null && (
        <span aria-hidden="true" className={css.chIcon}>
          {icon}
        </span>
      )}
      <span className={css.chMain}>
        <span className={css.chTitleRow}>
          <span className={css.chTitle}>{title}</span>
          {tag != null && (
            <span className={`${css.chTag} ${TONE_CLASS[tag.tone]}`}>
              <span aria-hidden="true" className={css.chDot} />
              {tag.label}
            </span>
          )}
          {dirty && <span className={`${css.chTag} ${css.tagWarn}`}>未保存</span>}
        </span>
        <span className={css.chDesc}>{desc}</span>
      </span>
      {onOpen != null && (
        <span aria-hidden="true" className={css.configBtn}>
          配置
        </span>
      )}
    </>
  )
  if (onOpen == null) return <div className={css.chCard}>{body}</div>
  return (
    <button type="button" className={`${css.chCard} ${css.clickable}`} onClick={onOpen}>
      {body}
    </button>
  )
}

/** 弹窗头部的状态标签（与渠道卡同一套色调） */
export function StatusChip({ label, tone }: { label: string; tone: Tone }) {
  return (
    <span className={`${css.chTag} ${TONE_CLASS[tone]}`}>
      <span aria-hidden="true" className={css.chDot} />
      {label}
    </span>
  )
}
