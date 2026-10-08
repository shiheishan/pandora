import { readdirSync, readFileSync } from 'node:fs'
import { join, relative } from 'node:path'
import ts from 'typescript'
import { describe, expect, it } from 'vitest'

// ---------------------------------------------------------------------------
// 叫法（购买模型原型 flow.md 第 7 节，已定稿）：渲染给用户的文字里，下面这些词一律 0 次。
// 「周期」改成「买多久：1 个月 / 3 个月 / 1 年」；「流量重置卡」是卡名，不含这些词。
// 扫门户全部源码里的字符串字面量、模板字符串与 JSX 文字（注释不算），测试与夹具除外。
// 服务端下发的文案（错误信息等）不在这里管。
//
// 唯一的例外（用户 10-08 定）：帮助里引用第三方 App 自己的按钮名、页面名，照它的中文界面原样写
// （v2rayNG 的「更新订阅」）。只放行 APP_BUTTON_FILE 里对象字面量 `button:` 属性的值，同一文件里的
// 其余文字、别的文件里的 `button:` 照样报错。
// ---------------------------------------------------------------------------
const BANNED = ['订阅', '导入', '抵扣', '折算', '剩余价值', '降级', '客户端', '落点', '约付', '周期', '重置订阅', '重置链接'] as const
const ROOT = new URL('../src/portal/', import.meta.url).pathname
/** 登记 App 按钮名的文件（common/app-update.ts 的 APP_UPDATE） */
const APP_BUTTON_FILE = 'screens/common/app-update.ts'

function files(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = join(dir, e.name)
    if (e.isDirectory()) return files(p)
    if (!/\.(ts|tsx)$/.test(e.name) || /\.test\.ts$/.test(e.name) || e.name === 'testing.ts') return []
    return [p]
  })
}

/** 对象字面量里 `button: '…'` 的值（App 按钮名的登记形状） */
const isAppButtonValue = (n: ts.Node) => ts.isPropertyAssignment(n.parent) && n.parent.initializer === n && ts.isIdentifier(n.parent.name) && n.parent.name.text === 'button'

/** 一段源码里所有会成为界面文字的片段：字符串、模板、JSX 文字；import 路径除外。appButtons 时跳过 `button:` 的值 */
function textsOf(path: string, code: string, appButtons: boolean): Array<{ line: number; text: string }> {
  const src = ts.createSourceFile(path, code, ts.ScriptTarget.Latest, true, path.endsWith('x') ? ts.ScriptKind.TSX : ts.ScriptKind.TS)
  const out: Array<{ line: number; text: string }> = []
  const visit = (n: ts.Node) => {
    if (ts.isImportDeclaration(n) || ts.isExportDeclaration(n)) return
    if (ts.isStringLiteral(n) || ts.isNoSubstitutionTemplateLiteral(n) || ts.isTemplateHead(n) || ts.isTemplateMiddle(n) || ts.isTemplateTail(n) || ts.isJsxText(n)) {
      if (!(appButtons && ts.isStringLiteral(n) && isAppButtonValue(n))) out.push({ line: src.getLineAndCharacterOfPosition(n.getStart()).line + 1, text: n.text })
    }
    ts.forEachChild(n, visit)
  }
  visit(src)
  return out
}

const texts = (path: string) => textsOf(path, readFileSync(path, 'utf8'), relative(ROOT, path) === APP_BUTTON_FILE)

describe('门户叫法', () => {
  it('界面文字里没有「订阅、导入、抵扣、折算、剩余价值、降级、客户端、落点、约付、周期、重置订阅、重置链接」', () => {
    const hits = files(ROOT).flatMap((f) =>
      texts(f)
        .filter((t) => BANNED.some((w) => t.text.includes(w)))
        .map((t) => `${relative(ROOT, f)}:${t.line} ${t.text}`),
    )
    expect(hits).toEqual([])
  })

  it('App 按钮名的例外只放行登记文件里 `button:` 的值', () => {
    const code = "const a = { button: '更新订阅', note: '就是更新你添加的那条链接' }\nconst b = '点更新订阅'\nconst c = { label: '更新订阅' }"
    const hits = (appButtons: boolean) => textsOf('x.ts', code, appButtons).filter((t) => BANNED.some((w) => t.text.includes(w))).map((t) => t.text)
    // 登记文件里：button 的值放行，同文件别处的「订阅」照样报
    expect(hits(true)).toEqual(['点更新订阅', '更新订阅'])
    // 别的文件：button 的值也报
    expect(hits(false)).toEqual(['更新订阅', '点更新订阅', '更新订阅'])
    // 登记文件里确实有被放行的按钮名（例外在用，不是空规则）
    const registered = textsOf(APP_BUTTON_FILE, readFileSync(join(ROOT, APP_BUTTON_FILE), 'utf8'), false).filter((t) => t.text === '更新订阅')
    expect(registered.length).toBeGreaterThan(0)
  })

  it('扫描本身能认出字符串、模板与 JSX 文字，不认注释', () => {
    const all = files(ROOT).flatMap(texts)
    expect(all.some((t) => t.text.includes('再买一份，分开用'))).toBe(true)
    expect(all.some((t) => t.text.includes('App 里显示为'))).toBe(true)
  })
})
