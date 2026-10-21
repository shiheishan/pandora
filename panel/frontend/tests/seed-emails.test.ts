// 仓库公开：dev/ 与 tests/ 里的种子、样稿与夹具邮箱只用 RFC 2606 / 6761 保留域；
// 若写入真实服务商域名（gmail、qq、163 等），本守卫会失败。
import { readFile, readdir } from 'node:fs/promises'
import { join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const RESERVED = ['example.com', 'example.net', 'example.org', 'example', 'test', 'invalid', 'localhost'] as const

/** 精确匹配整个地址；仅样稿里「禁止 Gmail 别名」说明用的示例 */
const EXCEPTIONS: Record<string, string> = {
  'k.liu+1@gmail.com': '后台样稿「禁止 Gmail 别名」开关的说明示例',
}

const EMAIL_RE = /[A-Za-z0-9._%+*-]+@((?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,})\b/g

const SKIP_DIRS = new Set(['node_modules', 'test-results', 'playwright-report'])
const SCAN_EXT = new Set(['.ts', '.tsx', '.html', '.md', '.json'])

const FRONTEND = fileURLToPath(new URL('..', import.meta.url))
const SCAN_ROOTS = [
  fileURLToPath(new URL('../dev', import.meta.url)),
  fileURLToPath(new URL('.', import.meta.url)),
]

function domainAllowed(domain: string): boolean {
  const d = domain.toLowerCase()
  return RESERVED.some((suffix) => d === suffix || d.endsWith(`.${suffix}`))
}

async function collectFiles(dir: string, out: string[]): Promise<void> {
  const entries = await readdir(dir, { withFileTypes: true })
  for (const ent of entries) {
    const full = join(dir, ent.name)
    if (ent.isDirectory()) {
      if (SKIP_DIRS.has(ent.name)) continue
      await collectFiles(full, out)
      continue
    }
    if (!ent.isFile()) continue
    const dot = ent.name.lastIndexOf('.')
    const ext = dot >= 0 ? ent.name.slice(dot) : ''
    if (SCAN_EXT.has(ext)) out.push(full)
  }
}

function scanText(relPath: string, text: string, bad: string[]): void {
  const lines = text.split(/\r?\n/)
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]!
    for (const m of line.matchAll(EMAIL_RE)) {
      const addr = m[0]
      const domain = m[1]!
      if (EXCEPTIONS[addr]) continue
      if (!domainAllowed(domain)) bad.push(`${relPath}:${i + 1} ${addr}`)
    }
  }
}

describe('seed emails · reserved domains only', () => {
  it('dev/ and tests/ sources use only reserved email domains', async () => {
    const bad: string[] = []
    for (const root of SCAN_ROOTS) {
      const files: string[] = []
      await collectFiles(root, files)
      for (const file of files) {
        const rel = relative(FRONTEND, file).split('\\').join('/')
        const text = await readFile(file, 'utf8')
        scanText(rel, text, bad)
      }
    }
    expect(bad).toEqual([])
  })
})
