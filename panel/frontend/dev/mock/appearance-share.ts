import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const FILE = join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'node_modules', '.cache', 'pandora-mock', 'appearance.json')

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
