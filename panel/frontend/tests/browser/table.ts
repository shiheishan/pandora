import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { PATHS, PRODUCT_ISSUES } from './paths.ts'

// ============================================================================
//  结果表：读 steps.jsonl（每步一行，由 fixtures.ts 的 step 写），按 paths.ts 的顺序出 markdown，
//  格式照 panel-smoke.yml 的「E2E script table」。没跑到的步骤也占一行。
//  用法：node tests/browser/table.ts <产物目录>；表写到 <产物目录>/results.md 并打印
// ============================================================================

interface Row {
  id: string
  ok: boolean
  seen: string
  ms: number
  shot: string
}

const out = resolve(process.argv[2] ?? '')
if (!process.argv[2]) {
  console.error('用法: node tests/browser/table.ts <产物目录>')
  process.exit(2)
}

const file = join(out, 'steps.jsonl')
const rows = new Map<string, Row>()
if (existsSync(file)) {
  for (const line of readFileSync(file, 'utf8').split('\n')) {
    if (!line.trim()) continue
    const r = JSON.parse(line) as Row
    rows.set(r.id, r)
  }
}

const cell = (s: string) => s.replace(/\|/g, '\\|').replace(/`/g, "'").replace(/\n/g, ' ')
const lines: string[] = []
let failed = 0
for (const p of PATHS) {
  const r = rows.get(p.id)
  const issue = PRODUCT_ISSUES[p.id]
  let result: string
  if (r) result = r.ok ? '通过' : '失败'
  else if (issue) result = '失败（产品问题）'
  else result = '未执行'
  if (result !== '通过') failed += 1
  const seen = r ? r.seen : (issue ?? '')
  const extra = r ? ` · ${(r.ms / 1000).toFixed(1)}s · ${r.shot}` : ''
  lines.push(`| ${p.id} ${cell(p.title)} | ${cell(seen)}${extra} | ${cell(p.expect)} | ${result} |`)
}
for (const id of rows.keys()) if (!PATHS.some((p) => p.id === id)) lines.push(`| ${id}（不在清单里） | ${cell(rows.get(id)!.seen)} | — | 失败 |`)

const md = lines.join('\n') + '\n'
writeFileSync(join(out, 'results.md'), md)
process.stdout.write(md)
console.log(`\n${PATHS.length} 步，未通过 ${failed} 步`)
