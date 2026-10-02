/**
 * [INPUT]: 依赖 react 的 useEffect，依赖 ../core/theme 的 useTheme，依赖 ../styles/design-tokens 的 COLOR_TOKENS，依赖 ./queries 的 useAppearance
 * [OUTPUT]: 对外提供 THEMEABLE_TOKENS、pickThemeTokens、PortalBranding、portalBranding、useAppearanceTheme
 * [POS]: portal 的主题令牌应用：GET v1/appearance 的 theme.tokens 分 light / dark 两组，按当前明暗取一组经 CSSOM setProperty 写到 <html>，白名单外的键忽略；站点名写进 document.title；portalBranding 给 SiteBrand 取站点名、标语与 Logo
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useEffect } from 'react'
import { useTheme, type Theme } from '../core/theme'
import { COLOR_TOKENS } from '../styles/design-tokens'
import { useAppearance, type Appearance } from './queries'

// ---------------------------------------------------------------------------
// 后台可以新建主题并切换生效的那一套（推翻了 5.A「只保留默认 · 纸白」）：门户每次加载取生效主题，
// 默认 · 纸白的值与 tokens.css 相同，换成别的主题时这里写进去的就是那套颜色，刷新页面即生效。
// 走 CSSOM 而不是注入 <style>：CSP 的 style-src 没有 unsafe-inline。
// ---------------------------------------------------------------------------
export const THEMEABLE_TOKENS: ReadonlySet<string> = new Set(COLOR_TOKENS.map((t) => t.name))

export function pickThemeTokens(appearance: Appearance | undefined, theme: Theme): Array<[string, string]> {
  const group = appearance?.theme?.tokens?.[theme] ?? {}
  return Object.entries(group).filter(([name, value]) => THEMEABLE_TOKENS.has(name) && value.trim() !== '')
}

// ---------------------------------------------------------------------------
// 站点品牌：生效主题的 branding（站点名、标语、Logo）。站点名是默认的 Pandora 且没有 Logo 时
// 返回 siteName = null，外框照旧画设计稿的 pandora 字标；Logo 只认 data:image/（CSP img-src 'self' data:）
// ---------------------------------------------------------------------------
export interface PortalBranding {
  siteName: string | null
  tagline: string | null
  logo: string | null
}

export function portalBranding(appearance: Appearance | undefined): PortalBranding {
  const b = appearance?.theme?.branding ?? {}
  const name = b.site_name?.trim() || null
  const logo = b.logo?.startsWith('data:image/') ? b.logo : null
  return { siteName: name && (name !== 'Pandora' || logo) ? name : null, tagline: b.tagline?.trim() || null, logo }
}

export function useAppearanceTheme(): void {
  const { data } = useAppearance()
  const theme = useTheme()

  useEffect(() => {
    const root = document.documentElement
    const entries = pickThemeTokens(data, theme)
    entries.forEach(([name, value]) => root.style.setProperty(name, value))
    return () => entries.forEach(([name]) => root.style.removeProperty(name))
  }, [data, theme])

  const siteName = data?.theme?.branding.site_name
  useEffect(() => {
    if (siteName) document.title = siteName
  }, [siteName])
}
