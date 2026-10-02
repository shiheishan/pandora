/**
 * [INPUT]: 依赖 ../shell/Logo，依赖 ./appearance 的 portalBranding，依赖 ./queries 的 useAppearance，依赖 ./SiteBrand.module.css
 * [OUTPUT]: 对外提供 SiteBrand（门户顶栏与登录页的站点品牌）
 * [POS]: portal 外框的品牌位：默认站点名且无 Logo 时就是设计稿的 Logo（环 + pandora 字标）；生效主题换了站点名或带 Logo 时，画 Logo 图（没有就画环）+ 站点名；withTagline 时在下方补标语（登录页用）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
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
