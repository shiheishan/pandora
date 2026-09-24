/**
 * [INPUT]: 依赖 ./Logo.module.css
 * [OUTPUT]: 对外提供 Logo（环形标记 + 可选 pandora 字标）
 * [POS]: shell 的品牌标记，后台登录页、侧栏与门户顶栏共用；环的颜色随上下文（currentColor），点固定朱砂
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import css from './Logo.module.css'

export function Logo({ size = 22, wordmark = true, className }: { size?: number; wordmark?: boolean; className?: string }) {
  return (
    <span className={className ? `${css.logo} ${className}` : css.logo}>
      <svg viewBox="0 0 48 48" width={size} height={size} className={css.mark} aria-hidden="true">
        <circle cx="24" cy="24" r="15" transform="rotate(-15 24 24)" className={css.ring} />
        <circle cx="38.1" cy="9.9" r="4" className={css.dot} />
      </svg>
      {wordmark && <span className={css.word}>pandora</span>}
    </span>
  )
}
