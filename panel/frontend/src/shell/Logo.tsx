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
