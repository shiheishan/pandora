import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import type { Server } from 'node:http'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { ADMIN_MODULES } from '../dev/mock/admin/index.ts'
import { findRoute } from '../dev/mock/types.ts'
import { MODULES, NAV_GROUPS, type ModuleKey } from '../src/admin/modules'
import { close, loginAs, serve } from './mock-helpers'

// 后台模块登记点守卫（new-admin-module skill）：新模块在 admin/modules.ts 登记之后，
// 侧栏 NAV_GROUPS 与 dev 假后端也要登记（dev/mock/admin/index.ts 的 ADMIN_MODULES、
// dev/mock-api.ts 的 ADMIN_PERMISSIONS），否则侧栏没有入口，或 dev:admin 上该页整页 404。
// w9cert 合入时假后端漏过一次，由总协调手补（fdc0ae2）。SCREENS 与 SCREEN_CHUNKS 由类型和 prefetch.test.ts 管。

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
const literals = (files: string[], pattern: RegExp) => new Set(files.flatMap((f) => [...read(f).matchAll(pattern)].map((m) => m[1]!)))

// 页面里写死的接口地址：api.get('v1/x') / api.post(`v1/x/${id}/y`)；模板插值当成一个路径段，查询串去掉
const API_CALL = /\bapi\.(get|post|put|patch|delete)\(\s*(['`])(v1\/[^'`?]*)[^'`]*\2/g
function screenCalls(key: ModuleKey): Array<{ method: string; path: string }> {
  const dir = join(root, 'src/admin/screens', key)
  return sources(dir).flatMap((f) =>
    [...read(f).matchAll(API_CALL)].map((m) => ({ method: m[1]!.toUpperCase(), path: '/' + m[3]!.replace(/\$\{[^}]*\}/g, 'x') })),
  )
}

// 迁移里 INSERT INTO permissions 插进去的权限码（与 Go 的 TestRoutePermissionsExistInCatalog 同一口径）
function permissionCatalog(): Set<string> {
  const dir = join(root, '../migrations')
  const codes = new Set<string>()
  for (const name of readdirSync(dir).filter((n) => n.endsWith('.sql'))) {
    for (const stmt of read(join(dir, name)).match(/INSERT INTO permissions[\s\S]*?;/g) ?? []) {
      for (const m of stmt.matchAll(/'([a-z]+(?:\.[a-z_]+)+)'/g)) codes.add(m[1]!)
    }
  }
  return codes
}

const readCodes = keys.flatMap((k) => {
  const code = MODULES[k].read
  return code === null ? [] : typeof code === 'string' ? [code] : Object.values(code)
})
const screenCodes = literals(sources(join(root, 'src/admin')), /\bcan\('([a-z._]+)'\)/g)
const mockCodes = literals(sources(join(root, 'dev/mock/admin')), /requirePermission\('([a-z._]+)'\)/g)

describe('admin module registry · sidebar', () => {
  it('puts every module exactly once in the NAV_GROUPS group its MODULES entry names', () => {
    const placed = NAV_GROUPS.flatMap(([group, members]) => members.map((key) => `${key}@${group}`)).sort()
    expect(placed).toEqual(keys.map((key) => `${key}@${MODULES[key].group}`).sort())
  })
})

describe('admin module registry · dev mock', () => {
  let server: Server
  let granted: Set<string>
  beforeAll(async () => {
    let base: string
    ;({ server, base } = await serve('admin'))
    granted = new Set((await loginAs(base, MOCK_ACCOUNTS.admin)).permissions)
  })
  afterAll(() => close(server))

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

  it('grants the mock admin every permission the admin screens and mock routes check', () => {
    const needed = new Set([...readCodes, ...screenCodes, ...mockCodes])
    expect(needed.size).toBeGreaterThan(20)
    const missing = [...needed].filter((c) => !granted.has(c)).sort()
    expect(missing, '缺的码加进 dev/mock-api.ts 的 ADMIN_PERMISSIONS').toEqual([])
  })

  it('uses only permission codes that a migration inserts into the catalog', () => {
    const catalog = permissionCatalog()
    expect(catalog.has('iam.user.read'), 'permission catalog did not parse').toBe(true)
    const unknown = [...new Set([...readCodes, ...screenCodes, ...mockCodes, ...granted])].filter((c) => !catalog.has(c)).sort()
    expect(unknown, '前端或假后端用了迁移权限字典里没有的码').toEqual([])
  })
})
