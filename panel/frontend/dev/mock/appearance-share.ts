/**
 * [INPUT]: 依赖 node:fs 的同步读写，依赖 node:path / node:url 定位工程的 node_modules/.cache
 * [OUTPUT]: 对外提供 shareActiveTheme、readSharedTheme
 * [POS]: dev/mock 的跨进程外观状态：dev:admin 与 dev:portal 是两个 vite 进程、各有一份内存，后台假接口（admin/content.ts）在激活或改动生效主题后把它写进 node_modules/.cache/pandora-mock/appearance.json，门户外壳（mock-api.ts 的 GET v1/appearance）每次请求读它——这样「后台激活、门户刷新即生效」在本机能演示。文件在 git 忽略的 node_modules 下，后台假后端启动时按内存初值重写一次
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
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
