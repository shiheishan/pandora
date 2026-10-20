import { cx } from './kit/cx'
import { portalBranding } from './appearance'
import { useAppearance } from './queries'
import css from './SiteBrand.module.css'

export function SiteBrand({ size, className, withTagline = false }: { size: number; className?: string; withTagline?: boolean }) {
  const b = portalBranding(useAppearance().data)
  const mark = (
    <span className={cx(css.brand, className)} style={{ fontSize: size }}>
      {b.logo ? <img className={css.img} src={b.logo} alt="" style={{ height: size }} /> : null}
      <span className={css.word}>{b.siteName ?? 'pandora'}</span>
      <span className={css.dot} aria-hidden="true" />
    </span>
  )
  if (!withTagline || !b.tagline) return mark
  return (
    <span className={css.stack}>
      {mark}
      <span className={css.tagline}>{b.tagline}</span>
    </span>
  )
}
