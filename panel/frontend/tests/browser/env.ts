import { mkdirSync, readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

// ============================================================================
//  输入只从冒烟栈的状态目录读（smoke.env、gateway.env、seed.json），不写任何真实部署的值。
//  状态目录由 SMOKE_STATE 给出；产物（截图、trace、结果表）写到 BROWSER_OUT，缺省是状态目录下的 browser/
// ============================================================================

function readEnvFile(path: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of readFileSync(path, 'utf8').split('\n')) {
    const m = /^([A-Z0-9_]+)=(.*)$/.exec(line)
    if (m) out[m[1]!] = m[2]!
  }
  return out
}

function need(env: Record<string, string>, key: string): string {
  const v = env[key]
  if (!v) throw new Error(`状态目录缺少 ${key}`)
  return v
}

export const STATE = (() => {
  const dir = process.env.SMOKE_STATE
  if (!dir) throw new Error('先设 SMOKE_STATE=<冒烟栈状态目录>（run-smoke-stack.sh up 的第三个参数）')
  return resolve(dir)
})()

const smoke = readEnvFile(join(STATE, 'smoke.env'))

/** 门户与后台网关；后台在冒烟栈上直接挂在网关根上，生产的 nginx 前缀不在这里出现 */
export const PUB = need(smoke, 'SMOKE_PUBLIC_BASE')
export const ADM = need(smoke, 'SMOKE_ADMIN_BASE')
export const ADMIN_EMAIL = need(smoke, 'SMOKE_ADMIN_EMAIL')
export const ADMIN_PASSWORD = need(smoke, 'SMOKE_ADMIN_PASSWORD')
export const PG_CONTAINER = need(smoke, 'SMOKE_PG_CONTAINER')
export const PG_DB = need(smoke, 'SMOKE_PG_DB')

/** 冒烟种子（tests/smoke/seed.ts）建的节点池：里面有一个已上线的节点，套餐才发布得出去 */
export function smokePoolId(): string {
  const seed = JSON.parse(readFileSync(join(STATE, 'seed.json'), 'utf8')) as { pool_id?: string }
  if (!seed.pool_id) throw new Error('状态目录的 seed.json 没有 pool_id：先跑 tests/smoke/seed.ts')
  return seed.pool_id
}

export const OUT = resolve(process.env.BROWSER_OUT || join(STATE, 'browser'))
export const SHOTS = join(OUT, 'shots')
export const STEPS_FILE = join(OUT, 'steps.jsonl')
/** 本测试自己的种子：套餐、流量包、礼品卡模板、渠道测试密钥。0600，只活在 runner 上 */
export const WORLD_FILE = join(STATE, 'browser-world.json')

export function ensureOut(): void {
  mkdirSync(SHOTS, { recursive: true })
}
