/**
 * [INPUT]: 依赖 react 的 useEffect，依赖 ../core/theme 的 useTheme，依赖 ../styles/design-tokens 的 COLOR_TOKENS，依赖 ./queries 的 useAppearance
 * [OUTPUT]: 对外提供 THEMEABLE_TOKENS、pickThemeTokens、useAppearanceTheme
 * [POS]: portal 的主题令牌应用：GET v1/appearance 的 theme.tokens 分 light / dark 两组，按当前明暗取一组经 CSSOM setProperty 写到 <html>，白名单外的键忽略；站点名写进 document.title
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useEffect } from 'react'
import { useTheme, type Theme } from '../core/theme'
import { COLOR_TOKENS } from '../styles/design-tokens'
import { useAppearance, type Appearance } from './queries'

// ---------------------------------------------------------------------------
// 只有「默认 · 纸白」一个主题（契约 5.A / R19），它的值与 tokens.css 相同，
// 所以正常情况下这里写进去的都是同值；保留这条链路是为了后台改站点品牌色时门户即时生效。
// 走 CSSOM 而不是注入 <style>：CSP 的 style-src 没有 unsafe-inline。
// ---------------------------------------------------------------------------
export const THEMEABLE_TOKENS: ReadonlySet<string> = new Set(COLOR_TOKENS.map((t) => t.name))

export function pickThemeTokens(appearance: Appearance | undefined, theme: Theme): Array<[string, string]> {
  const group = appearance?.theme?.tokens?.[theme] ?? {}
  return Object.entries(group).filter(([name, value]) => THEMEABLE_TOKENS.has(name) && value.trim() !== '')
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
