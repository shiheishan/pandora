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
