import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const FILE = join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'node_modules', '.cache', 'pandora-mock', 'appearance.json')
const SITE_FILE = join(dirname(FILE), 'site.json')

/** 写入门户该看到的生效主题（已按门户读路径过滤；null = 没有生效主题） */
export function shareActiveTheme(theme: unknown): void {
  try {
    mkdirSync(dirname(FILE), { recursive: true })
    writeFileSync(FILE, JSON.stringify({ theme }))
  } catch {
    // 只是演示用的旁路：写不进去时门户照旧用内置默认
  }
}

/** undefined = 后台假后端从没写过（门户用自己的默认） */
export function readSharedTheme(): { theme: unknown } | undefined {
  try {
    return JSON.parse(readFileSync(FILE, 'utf8')) as { theme: unknown }
  } catch {
    return undefined
  }
}

/** 写入后台系统设置设定的站点名（门户 GET v1/appearance 的 site_name 取它） */
export function shareSiteName(name: string): void {
  try {
    mkdirSync(dirname(SITE_FILE), { recursive: true })
    writeFileSync(SITE_FILE, JSON.stringify({ name }))
  } catch {
    // 演示用的旁路：写不进去时门户照旧用内置默认
  }
}

/** 读后台假后端设的站点名；读不到或不是字符串返回 undefined（门户 GET v1/appearance 的 site_name 取后台假后端设的站点名，读不到时用缺省） */
export function readSharedSiteName(): string | undefined {
  try {
    const raw = JSON.parse(readFileSync(SITE_FILE, 'utf8')) as { name?: unknown }
    return typeof raw?.name === 'string' ? raw.name : undefined
  } catch {
    return undefined
  }
}
