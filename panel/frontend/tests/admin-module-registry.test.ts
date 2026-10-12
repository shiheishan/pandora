import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { ADMIN_MODULES } from '../dev/mock/admin/index.ts'
import { findRoute } from '../dev/mock/types.ts'
import { MODULES, NAV_GROUPS, type ModuleKey } from '../src/admin/modules'

// 后台模块登记点守卫：新模块在 admin/modules.ts 登记之后，
// 侧栏 NAV_GROUPS 与 dev 假后端也要登记（dev/mock/admin/index.ts 的 ADMIN_MODULES），
// 否则侧栏没有入口，或 dev:admin 上该页整页 404。
// w9cert 合入时假后端漏过一次，由总协调手补（fdc0ae2）。SCREENS 由类型管。

const root = fileURLToPath(new URL('..', import.meta.url))
const keys = Object.keys(MODULES) as ModuleKey[]

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) return sources(path)
    return /\.tsx?$/.test(name) && !/\.test\.tsx?$/.test(name) ? [path] : []
  })
}

const read = (path: string) => readFileSync(path, 'utf8')

// 页面里写死的接口地址：api.get('v1/x') / api.post(`v1/x/${id}/y`)；模板插值当成一个路径段，查询串去掉
const API_CALL = /\bapi\.(get|post|put|patch|delete)\(\s*(['`])(v1\/[^'`?]*)[^'`]*\2/g
function screenCalls(key: ModuleKey): Array<{ method: string; path: string }> {
  const dir = join(root, 'src/admin/screens', key)
  return sources(dir).flatMap((f) =>
    [...read(f).matchAll(API_CALL)].map((m) => ({ method: m[1]!.toUpperCase(), path: '/' + m[3]!.replace(/\$\{[^}]*\}/g, 'x') })),
  )
}

describe('admin module registry · sidebar', () => {
  it('puts every module exactly once in the NAV_GROUPS group its MODULES entry names', () => {
    const placed = NAV_GROUPS.flatMap(([group, members]) => members.map((key) => `${key}@${group}`)).sort()
    expect(placed).toEqual(keys.map((key) => `${key}@${MODULES[key].group}`).sort())
  })
})

describe('admin module registry · dev mock', () => {
  it.each(keys)('registers the mock module of %s in ADMIN_MODULES', async (key) => {
    const file = join(root, 'dev/mock/admin', `${key}.ts`)
    expect(existsSync(file), `dev/mock/admin/${key}.ts 不存在：假后端模块文件按 ModuleKey 命名`).toBe(true)
    const exported = Object.values((await import(`../dev/mock/admin/${key}.ts`)) as Record<string, unknown>)
    expect(
      exported.some((v) => (ADMIN_MODULES as readonly unknown[]).includes(v)),
      `dev/mock/admin/${key}.ts 导出的模块没有登记进 dev/mock/admin/index.ts 的 ADMIN_MODULES`,
    ).toBe(true)
  })

  it.each(keys)('serves every fixed API path the %s screen calls', (key) => {
    const calls = screenCalls(key)
    expect(calls.length, `${key} 页面里没找到 api.<方法>('v1/…') 调用，提取规则失效`).toBeGreaterThan(0)
    const missing = calls.filter((c) => !findRoute(ADMIN_MODULES.map((m) => m.routes), c.method, c.path)).map((c) => `${c.method} ${c.path}`)
    expect(missing, `${key} 页面调用、但假后端没有路由的接口`).toEqual([])
  })
})
