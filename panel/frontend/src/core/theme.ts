/**
 * [INPUT]: 依赖 react 的 useSyncExternalStore，依赖浏览器 localStorage 与 document.documentElement
 * [OUTPUT]: 对外提供 Theme 类型、THEME_STORAGE_KEY、parseTheme、currentTheme、setTheme、toggleTheme、subscribeTheme、useTheme
 * [POS]: core 的主题状态，接管 theme-boot.js 在首帧前写好的 <html data-theme>；门户头像菜单与后台顶栏的「深色模式」开关都调它
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useSyncExternalStore } from 'react'

// ---------------------------------------------------------------------------
// 真相在 <html data-theme> 上：CSS 只看它，theme-boot.js 在首帧前写它，
// 这里的读写都围绕它，不另存一份 React 状态，避免两处走偏。
// 只有 light / dark 两档，默认 light；设计稿没有「跟随系统」。
// ---------------------------------------------------------------------------
export type Theme = 'light' | 'dark'

export const THEME_STORAGE_KEY = 'pandora-theme'

/** 任何不是 'dark' 的值（缺失、旧值、被篡改）都按默认浅色。 */
export function parseTheme(value: string | null | undefined): Theme {
  return value === 'dark' ? 'dark' : 'light'
}

const listeners = new Set<() => void>()

function root(): HTMLElement {
  return document.documentElement
}

export function currentTheme(): Theme {
  return parseTheme(root().getAttribute('data-theme'))
}

function apply(theme: Theme): void {
  if (currentTheme() === theme && root().hasAttribute('data-theme')) return
  root().setAttribute('data-theme', theme)
  listeners.forEach((notify) => notify())
}

export function setTheme(theme: Theme): void {
  try {
    window.localStorage.setItem(THEME_STORAGE_KEY, theme)
  } catch {
    // 存不进去也照样切换，只是刷新后回到默认
  }
  apply(theme)
}

export function toggleTheme(): void {
  setTheme(currentTheme() === 'dark' ? 'light' : 'dark')
}

// 另一个标签页改了主题：storage 事件只在其它页面触发，这里跟着切
function onStorage(event: StorageEvent): void {
  if (event.key === THEME_STORAGE_KEY) apply(parseTheme(event.newValue))
}

export function subscribeTheme(notify: () => void): () => void {
  if (listeners.size === 0) window.addEventListener('storage', onStorage)
  listeners.add(notify)
  return () => {
    listeners.delete(notify)
    if (listeners.size === 0) window.removeEventListener('storage', onStorage)
  }
}

export function useTheme(): Theme {
  return useSyncExternalStore(subscribeTheme, currentTheme, () => 'light')
}
