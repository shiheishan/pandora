import { Logo } from '../shell/Logo'
import { portalBranding } from './appearance'
import { useAppearance } from './queries'
import css from './SiteBrand.module.css'

export function SiteBrand({ size, className, withTagline = false }: { size: number; className?: string; withTagline?: boolean }) {
  const b = portalBranding(useAppearance().data)
  const mark =
    b.siteName === null ? (
      <Logo size={size} className={className} />
    ) : (
      <span className={className ? `${css.brand} ${className}` : css.brand}>
        {b.logo ? <img className={css.img} src={b.logo} alt="" style={{ height: size }} /> : <Logo size={size} wordmark={false} />}
        <span className={css.word}>{b.siteName}</span>
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
