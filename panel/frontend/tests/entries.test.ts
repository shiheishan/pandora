/**
 * [INPUT]: 依赖 node:fs 读取 src/{admin,portal}/index.html 源文件
 * [OUTPUT]: 对外提供入口源文件契约测试
 * [POS]: 构建前的快速守卫，与 panel/web/app_test.go 对真实产物的检查同一组部署前提；后者是最终裁决，这里只让错误在 npm test 就暴露
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const APPS = ['admin', 'portal'] as const

const readEntry = (app: string) =>
  readFileSync(new URL(`../src/${app}/index.html`, import.meta.url), 'utf8')

describe.each(APPS)('%s entry', (app) => {
  const html = readEntry(app)

  it('declares its own pandora-app domain', () => {
    // 网关按这个标记确认嵌入的是哪一边的产物
    expect(html).toContain(`<meta name="pandora-app" content="${app}" />`)
  })

  it('has no inline script or style (CSP forbids unsafe-inline)', () => {
    const scripts = [...html.matchAll(/<script\b([^>]*)>/gi)]
    expect(scripts.length).toBeGreaterThan(0)
    for (const [, attrs] of scripts) {
      expect(attrs).toMatch(/\bsrc="\.\//)
    }
    expect(html).not.toMatch(/<style\b|\sstyle\s*=/i)
  })
})
