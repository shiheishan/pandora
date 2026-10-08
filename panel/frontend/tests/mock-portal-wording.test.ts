import { readdirSync, readFileSync } from 'node:fs'
import { join, relative } from 'node:path'
import ts from 'typescript'
import { describe, expect, it } from 'vitest'

// ---------------------------------------------------------------------------
// 叫法（购买模型原型第 7 节）：渲染给用户的文字里，下面这些词一律 0 次。
// 扫门户全部源码里的字符串字面量、模板字符串与 JSX 文字（注释不算），测试与夹具除外。
// 服务端下发的文案（错误信息等）不在这里管。
// ---------------------------------------------------------------------------
const BANNED = ['订阅', '导入', '抵扣', '折算', '剩余价值', '降级', '客户端', '落点'] as const
const ROOT = new URL('../src/portal/', import.meta.url).pathname

function files(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = join(dir, e.name)
    if (e.isDirectory()) return files(p)
    if (!/\.(ts|tsx)$/.test(e.name) || /\.test\.ts$/.test(e.name) || e.name === 'testing.ts') return []
    return [p]
  })
}

/** 一个文件里所有会成为界面文字的片段：字符串、模板、JSX 文字；import 路径除外 */
function texts(path: string): Array<{ line: number; text: string }> {
  const src = ts.createSourceFile(path, readFileSync(path, 'utf8'), ts.ScriptTarget.Latest, true, path.endsWith('x') ? ts.ScriptKind.TSX : ts.ScriptKind.TS)
  const out: Array<{ line: number; text: string }> = []
  const visit = (n: ts.Node) => {
    if (ts.isImportDeclaration(n) || ts.isExportDeclaration(n)) return
    if (ts.isStringLiteral(n) || ts.isNoSubstitutionTemplateLiteral(n) || ts.isTemplateHead(n) || ts.isTemplateMiddle(n) || ts.isTemplateTail(n) || ts.isJsxText(n)) {
      out.push({ line: src.getLineAndCharacterOfPosition(n.getStart()).line + 1, text: n.text })
    }
    ts.forEachChild(n, visit)
  }
  visit(src)
  return out
}

describe('门户叫法', () => {
  it('界面文字里没有「订阅、导入、抵扣、折算、剩余价值、降级、客户端、落点」', () => {
    const hits = files(ROOT).flatMap((f) =>
      texts(f)
        .filter((t) => BANNED.some((w) => t.text.includes(w)))
        .map((t) => `${relative(ROOT, f)}:${t.line} ${t.text}`),
    )
    expect(hits).toEqual([])
  })

  it('扫描本身能认出字符串、模板与 JSX 文字，不认注释', () => {
    const all = files(ROOT).flatMap(texts)
    expect(all.some((t) => t.text.includes('再买一份，分开用'))).toBe(true)
    expect(all.some((t) => t.text.includes('App 里显示为'))).toBe(true)
  })
})
