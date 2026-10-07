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

// ---------------------------------------------------------------------------
// 生效主题的本地缓存：theme-boot.js 在首帧前按它把颜色写到 <html>，自定义主题的站点
// 不再每次打开先画默认色、等 v1/appearance 回来再换色。键名与形状两边要一致
// （守卫：tests/theme-boot.test.ts）。只存颜色，不存 Logo 等大字段
// ---------------------------------------------------------------------------
export const APPEARANCE_CACHE_KEY = 'pandora-portal-appearance'

interface StorageLike {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
}

function defaultStorage(): StorageLike | null {
  try {
    return window.localStorage
  } catch {
    return null
  }
}

/** 要缓存的值：两套明暗颜色；默认主题（没有可写的颜色）返回 null，表示清掉缓存。 */
export function themeCacheValue(appearance: Appearance | undefined): string | null {
  const light = Object.fromEntries(pickThemeTokens(appearance, 'light'))
  const dark = Object.fromEntries(pickThemeTokens(appearance, 'dark'))
  if (Object.keys(light).length === 0 && Object.keys(dark).length === 0) return null
  return JSON.stringify({ v: 1, light, dark })
}

export function writeThemeCache(appearance: Appearance | undefined, storage: StorageLike | null = defaultStorage()): void {
  const value = themeCacheValue(appearance)
  try {
    if (value === null) storage?.removeItem(APPEARANCE_CACHE_KEY)
    else storage?.setItem(APPEARANCE_CACHE_KEY, value)
  } catch {
    // 存不进去：下次打开照旧先画默认色
  }
}

/** 读缓存里某一档的颜色，只留白名单里的键（与首帧前 theme-boot 写上去的一致）。 */
export function readThemeCache(theme: Theme, storage: StorageLike | null = defaultStorage()): Array<[string, string]> {
  try {
    const cached: unknown = JSON.parse(storage?.getItem(APPEARANCE_CACHE_KEY) ?? 'null')
    if (!cached || typeof cached !== 'object' || (cached as { v?: unknown }).v !== 1) return []
    const group = (cached as Record<string, unknown>)[theme]
    if (!group || typeof group !== 'object') return []
    return Object.entries(group as Record<string, unknown>).filter(
      (entry): entry is [string, string] => THEMEABLE_TOKENS.has(entry[0]) && typeof entry[1] === 'string' && entry[1].trim() !== '',
    )
  } catch {
    return []
  }
}

export function useAppearanceTheme(): void {
  const { data } = useAppearance()
  const theme = useTheme()

  useEffect(() => {
    const root = document.documentElement
    // 接口回来前沿用缓存（随明暗切换跟着换档），回来后以接口为准。theme-boot 按缓存写过的颜色
    // 不归这里管，先把白名单里这次不写的清掉，免得主题换回默认后残留旧色
    const entries = data ? pickThemeTokens(data, theme) : readThemeCache(theme)
    const writing = new Set(entries.map(([name]) => name))
    THEMEABLE_TOKENS.forEach((name) => {
      if (!writing.has(name)) root.style.removeProperty(name)
    })
    entries.forEach(([name, value]) => root.style.setProperty(name, value))
    return () => entries.forEach(([name]) => root.style.removeProperty(name))
  }, [data, theme])

  useEffect(() => {
    if (data) writeThemeCache(data)
  }, [data])

  const siteName = data?.theme?.branding.site_name
  useEffect(() => {
    if (siteName) document.title = siteName
  }, [siteName])
}
